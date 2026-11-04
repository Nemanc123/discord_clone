package voice_test

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Nemanc123/discord_clone/internal/voice"
)

func parseToken(t *testing.T, token, secret string) jwt.MapClaims {
	t.Helper()

	parsed, err := jwt.Parse(token, func(*jwt.Token) (any, error) {
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		t.Fatalf("разбор токена: %v", err)
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatal("claims не MapClaims")
	}
	return claims
}

func TestIssueTokenGrants(t *testing.T) {
	issuer := voice.TokenIssuer{APIKey: "devkey", APISecret: "secret", TTL: 5 * time.Minute}

	token, err := issuer.Issue("user-1", "Алиса", "channel-1", true)
	if err != nil {
		t.Fatalf("токен: %v", err)
	}

	claims := parseToken(t, token, "secret")
	if claims["iss"] != "devkey" {
		t.Fatalf("iss = %v", claims["iss"])
	}
	if claims["sub"] != "user-1" {
		t.Fatalf("sub = %v", claims["sub"])
	}
	if claims["name"] != "Алиса" {
		t.Fatalf("name = %v", claims["name"])
	}

	video, ok := claims["video"].(map[string]any)
	if !ok {
		t.Fatalf("нет grant video: %v", claims["video"])
	}
	if video["room"] != "channel-1" {
		t.Fatalf("room = %v", video["room"])
	}
	if video["roomJoin"] != true || video["canSubscribe"] != true {
		t.Fatalf("grant неполный: %v", video)
	}
	if video["canPublish"] != true {
		t.Fatalf("canPublish = %v, ожидали true", video["canPublish"])
	}
}

func TestIssueTokenWithoutSpeak(t *testing.T) {
	issuer := voice.TokenIssuer{APIKey: "devkey", APISecret: "secret"}

	token, err := issuer.Issue("user-1", "Гость", "channel-1", false)
	if err != nil {
		t.Fatalf("токен: %v", err)
	}

	claims := parseToken(t, token, "secret")
	video := claims["video"].(map[string]any)
	if video["canPublish"] != false {
		t.Fatalf("canPublish = %v, ожидали false", video["canPublish"])
	}
}

func TestIssueTokenRequiresKeys(t *testing.T) {
	issuer := voice.TokenIssuer{}

	if _, err := issuer.Issue("u", "n", "r", true); err == nil {
		t.Fatal("ожидали ошибку без ключей")
	}
}
