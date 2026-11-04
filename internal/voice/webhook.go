package voice

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidWebhook — подпись или хэш тела не сошлись.
var ErrInvalidWebhook = errors.New("неверная подпись вебхука")

// WebhookEvent — минимум полей из уведомления LiveKit, который нам нужен.
type WebhookEvent struct {
	Event string `json:"event"`
	Room  struct {
		Name string `json:"name"`
		SID  string `json:"sid"`
	} `json:"room"`
	Participant struct {
		Identity string `json:"identity"`
		Name     string `json:"name"`
	} `json:"participant"`
}

// VerifyWebhook проверяет подпись уведомления LiveKit.
//
// LiveKit подписывает заголовок Authorization тем же секретом, что и
// токены, а в claim sha256 кладёт base64 от SHA-256 тела запроса.
// Сверять нужно оба: подпись доказывает авторство, хэш — что тело не
// подменили по дороге.
func VerifyWebhook(apiKey, apiSecret, rawToken string, body []byte) (WebhookEvent, error) {
	token := strings.TrimPrefix(rawToken, "Bearer ")
	if token == "" {
		return WebhookEvent{}, fmt.Errorf("%w: нет заголовка Authorization", ErrInvalidWebhook)
	}

	parsed, err := jwt.Parse(token, func(*jwt.Token) (any, error) {
		return []byte(apiSecret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !parsed.Valid {
		return WebhookEvent{}, fmt.Errorf("%w: подпись не прошла проверку", ErrInvalidWebhook)
	}

	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return WebhookEvent{}, fmt.Errorf("%w: нет claims", ErrInvalidWebhook)
	}

	issuer, _ := claims["iss"].(string)
	if issuer != apiKey {
		return WebhookEvent{}, fmt.Errorf("%w: чужой ключ", ErrInvalidWebhook)
	}

	sum := sha256.Sum256(body)
	want := base64.StdEncoding.EncodeToString(sum[:])
	got, _ := claims["sha256"].(string)

	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return WebhookEvent{}, fmt.Errorf("%w: хэш тела не совпал", ErrInvalidWebhook)
	}

	var event WebhookEvent
	if err := json.Unmarshal(body, &event); err != nil {
		return WebhookEvent{}, fmt.Errorf("%w: разбор тела: %v", ErrInvalidWebhook, err)
	}

	return event, nil
}
