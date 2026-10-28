package auth_test

// Интеграционные тесты сервиса аутентификации.
//
// Требуют живой PostgreSQL. Если DATABASE_URL не задан — тесты пропускаются,
// чтобы `go test ./...` работал на машине без базы.
//
// Каждый тест работает в транзакции и откатывает её: база остаётся чистой,
// а тесты можно запускать параллельно с обычной работой.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Nemanc123/discord_clone/internal/auth"
	"github.com/Nemanc123/discord_clone/internal/store"
)

func newTestService(t *testing.T) *auth.Service {
	t.Helper()

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL не задан — интеграционные тесты пропущены")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("подключение к базе: %v", err)
	}
	t.Cleanup(pool.Close)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("транзакция: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

	return auth.NewService(store.New(tx), "test-secret", 15*time.Minute, 24*time.Hour)
}

// uniqueName даёт неповторяющееся имя, чтобы тесты не мешали друг другу.
func uniqueName(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("случайное имя: %v", err)
	}
	return "u" + hex.EncodeToString(buf)
}

func register(t *testing.T, svc *auth.Service, username, password string) auth.Result {
	t.Helper()
	res, err := svc.Register(context.Background(), auth.RegisterInput{
		Username: username,
		Password: password,
		Email:    username + "@example.com",
	})
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	return res
}

func TestRegisterAndLogin(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	name := uniqueName(t)

	res := register(t, svc, name, "very-long-password")
	if res.User.Username != name {
		t.Fatalf("username = %q, ожидали %q", res.User.Username, name)
	}
	if res.AccessToken == "" || res.RefreshToken == "" {
		t.Fatal("не выдали токены")
	}
	if res.User.PasswordHash == "" {
		t.Fatal("пароль не сохранён")
	}

	login, err := svc.Login(ctx, name, "very-long-password", "test-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("вход: %v", err)
	}
	if login.User.ID != res.User.ID {
		t.Fatal("вход вернул другого пользователя")
	}
	// Каждый вход создаёт новую сессию — токены обязаны различаться.
	if login.RefreshToken == res.RefreshToken {
		t.Fatal("refresh-токен повторно использован")
	}
}

func TestRegisterRejectsDuplicate(t *testing.T) {
	svc := newTestService(t)
	name := uniqueName(t)

	register(t, svc, name, "very-long-password")

	_, err := svc.Register(context.Background(), auth.RegisterInput{
		Username: name,
		Password: "another-long-password",
	})
	if !errors.Is(err, auth.ErrUserExists) {
		t.Fatalf("ожидали ErrUserExists, получили %v", err)
	}
}

func TestRegisterValidatesInput(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	cases := []auth.RegisterInput{
		{Username: "Bad Name", Password: "very-long-password"},
		{Username: "ok_name", Password: "short"},
		{Username: "ok_name2", Password: "very-long-password", Email: "not-an-email"},
	}

	for _, input := range cases {
		if _, err := svc.Register(ctx, input); !errors.Is(err, auth.ErrValidation) {
			t.Errorf("вход %+v: ожидали ErrValidation, получили %v", input, err)
		}
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	svc := newTestService(t)
	name := uniqueName(t)
	register(t, svc, name, "very-long-password")

	_, err := svc.Login(context.Background(), name, "wrong-password", "", "")
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("ожидали ErrInvalidCredentials, получили %v", err)
	}
}

func TestLoginRejectsUnknownUser(t *testing.T) {
	svc := newTestService(t)

	_, err := svc.Login(context.Background(), "no-such-user-"+uniqueName(t), "whatever-long", "", "")
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("ожидали ErrInvalidCredentials, получили %v", err)
	}
}

func TestRefreshRotatesToken(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	name := uniqueName(t)

	first := register(t, svc, name, "very-long-password")

	second, err := svc.Refresh(ctx, first.RefreshToken, "", "")
	if err != nil {
		t.Fatalf("обновление: %v", err)
	}
	if second.RefreshToken == first.RefreshToken {
		t.Fatal("refresh-токен не сменился")
	}

	// Старый токен больше не действует.
	if _, err := svc.Refresh(ctx, first.RefreshToken, "", ""); !errors.Is(err, auth.ErrSessionInvalid) {
		t.Fatalf("старый refresh-токен должен быть отклонён, получили %v", err)
	}
}

func TestRefreshRevokesAllOnTokenReuse(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	name := uniqueName(t)

	first := register(t, svc, name, "very-long-password")

	second, err := svc.Refresh(ctx, first.RefreshToken, "", "")
	if err != nil {
		t.Fatalf("обновление: %v", err)
	}

	// Повторное использование старого токена — признак кражи.
	if _, err := svc.Refresh(ctx, first.RefreshToken, "", ""); !errors.Is(err, auth.ErrSessionInvalid) {
		t.Fatalf("ожидали ErrSessionInvalid, получили %v", err)
	}

	// После этого должен быть мёртв и legit-токен из второй пары.
	if _, err := svc.Refresh(ctx, second.RefreshToken, "", ""); !errors.Is(err, auth.ErrSessionInvalid) {
		t.Fatalf("все сессии должны быть отозваны, получили %v", err)
	}
}

func TestLogoutInvalidatesRefresh(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	name := uniqueName(t)

	res := register(t, svc, name, "very-long-password")

	claims, err := svc.ParseAccessToken(res.AccessToken)
	if err != nil {
		t.Fatalf("разбор access-токена: %v", err)
	}

	if err := svc.Logout(ctx, claims.SessionID); err != nil {
		t.Fatalf("выход: %v", err)
	}

	if _, err := svc.Refresh(ctx, res.RefreshToken, "", ""); !errors.Is(err, auth.ErrSessionInvalid) {
		t.Fatalf("после выхода refresh должен быть недействителен, получили %v", err)
	}
}

func TestMeReturnsCurrentUser(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	name := uniqueName(t)

	res := register(t, svc, name, "very-long-password")

	claims, err := svc.ParseAccessToken(res.AccessToken)
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}

	user, err := svc.User(ctx, claims.Subject)
	if err != nil {
		t.Fatalf("профиль: %v", err)
	}
	if user.Username != name {
		t.Fatalf("username = %q, ожидали %q", user.Username, name)
	}
}
