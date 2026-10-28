package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims — содержимое access-токена.
//
// Внутри только то, что нужно для проверки: кто (sub) и какая сессия (sid).
// Права сюда не кладём: они меняются, а токен живёт 15 минут и не обновляется.
type Claims struct {
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

type TokenIssuer struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

func NewTokenIssuer(secret string, ttl time.Duration) *TokenIssuer {
	return &TokenIssuer{
		secret: []byte(secret),
		ttl:    ttl,
		now:    time.Now,
	}
}

// Issue создаёт access-токен и возвращает момент его истечения.
func (t *TokenIssuer) Issue(userID, sessionID string) (string, time.Time, error) {
	now := t.now()
	expiresAt := now.Add(t.ttl)

	claims := Claims{
		SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			Issuer:    "discord_clone",
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-10 * time.Second)),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(t.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("подпись токена: %w", err)
	}

	return signed, expiresAt, nil
}

// Parse проверяет подпись и срок действия.
func (t *TokenIssuer) Parse(raw string) (*Claims, error) {
	claims := &Claims{}

	_, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("неожиданный алгоритм подписи: %v", token.Header["alg"])
		}
		return t.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		return nil, fmt.Errorf("разбор токена: %w", err)
	}

	return claims, nil
}

// NewRefreshToken возвращает случайный opaque-токен и его хеш для БД.
//
// Refresh-токен не JWT: он не должен нести данных, его единственная задача —
// быть неугадываемым. В базе лежит только sha256, чтобы утечка БД
// не давала возможности войти.
func NewRefreshToken() (token string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("генерация refresh-токена: %w", err)
	}

	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, HashRefreshToken(token), nil
}

var ErrEmptyRefreshToken = errors.New("пустой refresh-токен")

func HashRefreshToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
