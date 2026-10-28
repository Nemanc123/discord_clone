package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/Nemanc123/discord_clone/internal/store"
)

var (
	ErrValidation         = errors.New("неверные данные")
	ErrInvalidCredentials = errors.New("неверный логин или пароль")
	ErrSessionInvalid     = errors.New("сессия недействительна")
	ErrUserExists         = errors.New("пользователь уже существует")
)

const (
	MinPasswordLen = 10
	maxPasswordLen = 256
)

var usernameRe = regexp.MustCompile(`^[a-z0-9_.]{2,32}$`)

// dummyHash выравнивает время ответа при несуществующем пользователе.
// Без него «нет такого» отвечает заметно быстрее, чем «неверный пароль»,
// и по времени можно перечислять существующие аккаунты.
var dummyHash, _ = HashPassword("dummy-password-for-timing-equalisation")

type Service struct {
	store      *store.Store
	issuer     *TokenIssuer
	refreshTTL time.Duration
	now        func() time.Time
}

func NewService(st *store.Store, secret string, accessTTL, refreshTTL time.Duration) *Service {
	return &Service{
		store:      st,
		issuer:     NewTokenIssuer(secret, accessTTL),
		refreshTTL: refreshTTL,
		now:        time.Now,
	}
}

// Result — то, что получает клиент после успешного входа.
type Result struct {
	User             store.User
	AccessToken      string
	RefreshToken     string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
}

type RegisterInput struct {
	Username    string
	Email       string
	DisplayName string
	Password    string
	UserAgent   string
	IP          string
}

func (s *Service) Register(ctx context.Context, in RegisterInput) (Result, error) {
	username := strings.ToLower(strings.TrimSpace(in.Username))
	email := strings.ToLower(strings.TrimSpace(in.Email))
	password := in.Password

	if !usernameRe.MatchString(username) {
		return Result{}, fmt.Errorf("%w: имя пользователя — 2–32 символа, только a-z, 0-9, _. и -", ErrValidation)
	}
	if len(password) < MinPasswordLen {
		return Result{}, fmt.Errorf("%w: пароль короче %d символов", ErrValidation, MinPasswordLen)
	}
	if len(password) > maxPasswordLen {
		return Result{}, fmt.Errorf("%w: пароль длиннее %d символов", ErrValidation, maxPasswordLen)
	}
	if email != "" && (!strings.Contains(email, "@") || strings.HasPrefix(email, "@") || strings.HasSuffix(email, "@")) {
		return Result{}, fmt.Errorf("%w: некорректная почта", ErrValidation)
	}

	hash, err := HashPassword(password)
	if err != nil {
		return Result{}, err
	}

	params := store.CreateUserParams{
		Username:     username,
		PasswordHash: hash,
	}
	if email != "" {
		params.Email = &email
	}
	if display := strings.TrimSpace(in.DisplayName); display != "" {
		params.DisplayName = &display
	}

	user, err := s.store.CreateUser(ctx, params)
	if errors.Is(err, store.ErrUserExists) {
		return Result{}, ErrUserExists
	}
	if err != nil {
		return Result{}, err
	}

	return s.startSession(ctx, user, in.UserAgent, in.IP)
}

func (s *Service) Login(ctx context.Context, login, password, userAgent, ip string) (Result, error) {
	login = strings.TrimSpace(login)
	if login == "" || password == "" {
		return Result{}, ErrInvalidCredentials
	}

	user, err := s.store.UserByLogin(ctx, login)
	if errors.Is(err, store.ErrNotFound) {
		// Считаем хеш впустую, чтобы время ответа не выдавало отсутствие аккаунта.
		_, _ = VerifyPassword(password, dummyHash)
		return Result{}, ErrInvalidCredentials
	}
	if err != nil {
		return Result{}, err
	}

	ok, err := VerifyPassword(password, user.PasswordHash)
	if err != nil {
		return Result{}, fmt.Errorf("проверка пароля: %w", err)
	}
	if !ok {
		return Result{}, ErrInvalidCredentials
	}

	return s.startSession(ctx, user, userAgent, ip)
}

// Refresh проворачивает refresh-токен: старый отзывается, выдаётся новый.
func (s *Service) Refresh(ctx context.Context, refreshToken, userAgent, ip string) (Result, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return Result{}, ErrSessionInvalid
	}

	session, err := s.store.SessionByTokenHash(ctx, HashRefreshToken(refreshToken))
	if errors.Is(err, store.ErrNotFound) {
		return Result{}, ErrSessionInvalid
	}
	if err != nil {
		return Result{}, err
	}

	// Пришёл уже отозванный токен — значит его украли и используют повторно.
	// Правильная реакция: погасить все сессии пользователя.
	if session.RevokedAt != nil {
		slog.Warn("повторное использование отозванного refresh-токена",
			"user_id", session.UserID, "session_id", session.ID)
		_ = s.store.RevokeAllSessions(ctx, session.UserID)
		return Result{}, ErrSessionInvalid
	}

	if !session.ExpiresAt.After(s.now()) {
		return Result{}, ErrSessionInvalid
	}

	user, err := s.store.UserByID(ctx, session.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return Result{}, ErrSessionInvalid
	}
	if err != nil {
		return Result{}, err
	}

	if err := s.store.RevokeSession(ctx, session.ID); err != nil {
		return Result{}, err
	}

	return s.startSession(ctx, user, userAgent, ip)
}

func (s *Service) Logout(ctx context.Context, sessionID string) error {
	return s.store.RevokeSession(ctx, sessionID)
}

func (s *Service) LogoutAll(ctx context.Context, userID string) error {
	return s.store.RevokeAllSessions(ctx, userID)
}

// User отдаёт профиль по id — используется эндпоинтом /users/@me.
func (s *Service) User(ctx context.Context, id string) (store.User, error) {
	return s.store.UserByID(ctx, id)
}

// ParseAccessToken проверяет access-токен и возвращает его claims.
func (s *Service) ParseAccessToken(raw string) (*Claims, error) {
	return s.issuer.Parse(raw)
}

func (s *Service) startSession(ctx context.Context, user store.User, userAgent, ip string) (Result, error) {
	refreshToken, hash, err := NewRefreshToken()
	if err != nil {
		return Result{}, err
	}

	refreshExpiresAt := s.now().Add(s.refreshTTL)

	sessionID, err := s.store.CreateSession(ctx, store.CreateSessionParams{
		UserID:    user.ID,
		TokenHash: hash,
		UserAgent: userAgent,
		IP:        ip,
		ExpiresAt: refreshExpiresAt,
	})
	if err != nil {
		return Result{}, err
	}

	accessToken, accessExpiresAt, err := s.issuer.Issue(user.ID, sessionID)
	if err != nil {
		return Result{}, err
	}

	return Result{
		User:             user,
		AccessToken:      accessToken,
		RefreshToken:     refreshToken,
		AccessExpiresAt:  accessExpiresAt,
		RefreshExpiresAt: refreshExpiresAt,
	}, nil
}
