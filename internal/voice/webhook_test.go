package voice_test

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Nemanc123/discord_clone/internal/voice"
)

func signWebhook(t *testing.T, apiKey, secret string, body []byte) string {
	t.Helper()

	sum := sha256.Sum256(body)
	claims := jwt.MapClaims{
		"iss":    apiKey,
		"sha256": base64.StdEncoding.EncodeToString(sum[:]),
		"exp":    time.Now().Add(time.Minute).Unix(),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("подпись вебхука: %v", err)
	}
	return token
}

const webhookBody = `{"event":"participant_joined","room":{"name":"channel-1","sid":"RM_1"},"participant":{"identity":"user-1","name":"Алиса"}}`

func TestVerifyWebhookAcceptsValid(t *testing.T) {
	body := []byte(webhookBody)
	token := signWebhook(t, "devkey", "secret", body)

	event, err := voice.VerifyWebhook("devkey", "secret", token, body)
	if err != nil {
		t.Fatalf("проверка: %v", err)
	}
	if event.Event != "participant_joined" {
		t.Fatalf("event = %q", event.Event)
	}
	if event.Room.Name != "channel-1" || event.Participant.Identity != "user-1" {
		t.Fatalf("поля разобраны неверно: %+v", event)
	}
}

func TestVerifyWebhookRejectsTamperedBody(t *testing.T) {
	token := signWebhook(t, "devkey", "secret", []byte(webhookBody))

	tampered := []byte(`{"event":"participant_left","room":{"name":"channel-1"},"participant":{"identity":"user-1"}}`)

	_, err := voice.VerifyWebhook("devkey", "secret", token, tampered)
	if !errors.Is(err, voice.ErrInvalidWebhook) {
		t.Fatalf("ожидали ErrInvalidWebhook, получили %v", err)
	}
}

func TestVerifyWebhookRejectsWrongKey(t *testing.T) {
	body := []byte(webhookBody)
	token := signWebhook(t, "other-key", "secret", body)

	_, err := voice.VerifyWebhook("devkey", "secret", token, body)
	if !errors.Is(err, voice.ErrInvalidWebhook) {
		t.Fatalf("ожидали ErrInvalidWebhook, получили %v", err)
	}
}

func TestVerifyWebhookRejectsMissingToken(t *testing.T) {
	_, err := voice.VerifyWebhook("devkey", "secret", "", []byte(webhookBody))
	if !errors.Is(err, voice.ErrInvalidWebhook) {
		t.Fatalf("ожидали ErrInvalidWebhook, получили %v", err)
	}
}
