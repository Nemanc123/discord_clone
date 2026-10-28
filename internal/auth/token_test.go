package auth

import (
	"testing"
	"time"
)

func TestIssueAndParse(t *testing.T) {
	issuer := NewTokenIssuer("test-secret", 15*time.Minute)

	token, expiresAt, err := issuer.Issue("user-1", "session-1")
	if err != nil {
		t.Fatalf("выпуск: %v", err)
	}
	if token == "" {
		t.Fatal("пустой токен")
	}
	if !expiresAt.After(time.Now()) {
		t.Fatal("срок действия в прошлом")
	}

	claims, err := issuer.Parse(token)
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if claims.Subject != "user-1" {
		t.Errorf("sub = %q, ожидали user-1", claims.Subject)
	}
	if claims.SessionID != "session-1" {
		t.Errorf("sid = %q, ожидали session-1", claims.SessionID)
	}
}

func TestParseRejectsExpiredToken(t *testing.T) {
	issuer := NewTokenIssuer("test-secret", -time.Minute)

	token, _, err := issuer.Issue("user-1", "session-1")
	if err != nil {
		t.Fatalf("выпуск: %v", err)
	}

	if _, err := issuer.Parse(token); err == nil {
		t.Fatal("просроченный токен принят")
	}
}

func TestParseRejectsForeignSignature(t *testing.T) {
	issuerA := NewTokenIssuer("secret-a", time.Hour)
	issuerB := NewTokenIssuer("secret-b", time.Hour)

	token, _, err := issuerA.Issue("user-1", "session-1")
	if err != nil {
		t.Fatalf("выпуск: %v", err)
	}

	if _, err := issuerB.Parse(token); err == nil {
		t.Fatal("токен с чужой подписью принят")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	issuer := NewTokenIssuer("test-secret", time.Hour)

	for _, raw := range []string{"", "abc", "a.b.c", "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0."} {
		if _, err := issuer.Parse(raw); err == nil {
			t.Errorf("мусор %q принят за токен", raw)
		}
	}
}

func TestNewRefreshToken(t *testing.T) {
	first, hash, err := NewRefreshToken()
	if err != nil {
		t.Fatalf("генерация: %v", err)
	}
	if len(first) < 32 {
		t.Fatalf("токен слишком короткий: %d символов", len(first))
	}
	if len(hash) != 32 {
		t.Fatalf("хеш должен быть 32 байта (sha256), получили %d", len(hash))
	}

	second, _, err := NewRefreshToken()
	if err != nil {
		t.Fatalf("генерация: %v", err)
	}
	if first == second {
		t.Fatal("два токена совпали")
	}

	// Хеш должен быть детерминированным: иначе поиск сессии не сработает.
	if string(HashRefreshToken(first)) != string(hash) {
		t.Fatal("хеш одного и того же токена различается")
	}
}
