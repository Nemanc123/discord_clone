// Package store — доступ к PostgreSQL.
//
// Здесь только SQL. Никакой бизнес-логики: сервис решает «можно ли»,
// хранилище — «как это записать и прочитать».
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBTX — общий интерфейс пула и транзакции.
// Благодаря ему тесты могут работать в транзакции с откатом,
// а код не знает, где именно он выполняется.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var (
	ErrUserExists = errors.New("пользователь уже существует")
	ErrNotFound   = errors.New("не найдено")
)

type Store struct {
	db DBTX
}

func New(db DBTX) *Store {
	return &Store{db: db}
}

func NewWithPool(pool *pgxpool.Pool) *Store {
	return New(pool)
}

// Beginner — то, что умеет начинать транзакцию (пул умеет, транзакция — нет).
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Tx начинает транзакцию, если хранилище работает с пулом.
//
// Если хранилище уже внутри транзакции (так работают тесты), возвращает себя
// и nil: вложенных транзакций в PostgreSQL нет, есть только savepoint.
func (s *Store) Tx(ctx context.Context) (*Store, pgx.Tx, error) {
	beginner, ok := s.db.(Beginner)
	if !ok {
		return s, nil, nil
	}

	tx, err := beginner.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("начало транзакции: %w", err)
	}

	return New(tx), tx, nil
}

// User — то, что нужно сервису. Пароль отдаётся отдельно, чтобы его
// нельзя было случайно сериализовать в JSON-ответ.
type User struct {
	ID           string
	Username     string
	DisplayName  *string
	Email        *string
	PasswordHash string
	Status       string
	CreatedAt    time.Time
}

type CreateUserParams struct {
	Username     string
	DisplayName  *string
	Email        *string
	PasswordHash string
}

func (s *Store) CreateUser(ctx context.Context, p CreateUserParams) (User, error) {
	const query = `
		INSERT INTO users (username, display_name, email, password_hash)
		VALUES ($1::citext, $2, $3::citext, $4)
		RETURNING id::text, username::text, display_name, email::text,
		          password_hash, status, created_at`

	var u User
	err := s.db.QueryRow(ctx, query, p.Username, p.DisplayName, p.Email, p.PasswordHash).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.PasswordHash, &u.Status, &u.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return User{}, ErrUserExists
		}
		return User{}, fmt.Errorf("создание пользователя: %w", err)
	}

	return u, nil
}

// UserByLogin ищет по нику или почте: пользователь может войти любым.
func (s *Store) UserByLogin(ctx context.Context, login string) (User, error) {
	const query = `
		SELECT id::text, username::text, display_name, email::text,
		       password_hash, status, created_at
		  FROM users
		 WHERE (username = $1::citext OR email = $1::citext)
		   AND deleted_at IS NULL
		 LIMIT 1`

	var u User
	err := s.db.QueryRow(ctx, query, login).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.PasswordHash, &u.Status, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("поиск пользователя: %w", err)
	}

	return u, nil
}

func (s *Store) UserByID(ctx context.Context, id string) (User, error) {
	const query = `
		SELECT id::text, username::text, display_name, email::text,
		       password_hash, status, created_at
		  FROM users
		 WHERE id = $1::uuid AND deleted_at IS NULL`

	var u User
	err := s.db.QueryRow(ctx, query, id).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.PasswordHash, &u.Status, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("пользователь по id: %w", err)
	}

	return u, nil
}

type Session struct {
	ID        string
	UserID    string
	ExpiresAt time.Time
	RevokedAt *time.Time
}

type CreateSessionParams struct {
	UserID    string
	TokenHash []byte
	UserAgent string
	IP        string
	ExpiresAt time.Time
}

func (s *Store) CreateSession(ctx context.Context, p CreateSessionParams) (string, error) {
	const query = `
		INSERT INTO user_sessions (user_id, token_hash, user_agent, ip, expires_at)
		VALUES ($1::uuid, $2, NULLIF($3, ''), NULLIF($4, '')::inet, $5)
		RETURNING id::text`

	var id string
	if err := s.db.QueryRow(ctx, query, p.UserID, p.TokenHash, p.UserAgent, p.IP, p.ExpiresAt).Scan(&id); err != nil {
		return "", fmt.Errorf("создание сессии: %w", err)
	}

	return id, nil
}

func (s *Store) SessionByTokenHash(ctx context.Context, hash []byte) (Session, error) {
	const query = `
		SELECT id::text, user_id::text, expires_at, revoked_at
		  FROM user_sessions
		 WHERE token_hash = $1`

	var sess Session
	err := s.db.QueryRow(ctx, query, hash).
		Scan(&sess.ID, &sess.UserID, &sess.ExpiresAt, &sess.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("поиск сессии: %w", err)
	}

	return sess, nil
}

func (s *Store) RevokeSession(ctx context.Context, id string) error {
	const query = `UPDATE user_sessions SET revoked_at = now() WHERE id = $1::uuid AND revoked_at IS NULL`
	if _, err := s.db.Exec(ctx, query, id); err != nil {
		return fmt.Errorf("отзыв сессии: %w", err)
	}
	return nil
}

// RevokeAllSessions — выход со всех устройств и реакция на кражу refresh-токена.
func (s *Store) RevokeAllSessions(ctx context.Context, userID string) error {
	const query = `UPDATE user_sessions SET revoked_at = now() WHERE user_id = $1::uuid AND revoked_at IS NULL`
	if _, err := s.db.Exec(ctx, query, userID); err != nil {
		return fmt.Errorf("отзыв всех сессий: %w", err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
