// Package auth — аутентификация: пароли, токены, сессии.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Параметры argon2id.
//
// memory = 64 МиБ на один хеш — это осознанно дорого: именно стоимость
// защищает от перебора на видеокартах. 3 итерации и 4 потока — значения
// из рекомендаций OWASP и от эталонной реализации.
const (
	argonMemory  = 64 * 1024
	argonTime    = 3
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// ErrInvalidHash означает, что строка хеша повреждена или в неизвестном формате.
// Это не «неверный пароль», а «данные испорчены» — разные ситуации.
var ErrInvalidHash = errors.New("invalid password hash format")

// HashPassword возвращает хеш в формате:
//
//	$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
//
// Параметры зашиты в саму строку, поэтому их можно менять в будущем —
// старые хеши продолжат проверяться своими параметрами.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("генерация соли: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword сравнивает пароль с хешем за постоянное время.
//
// ConstantTimeCompare обязателен: обычное сравнение массивов байт
// завершается на первом различии, и по времени ответа можно подбирать хеш.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, ErrInvalidHash
	}
	if version != argon2.Version {
		return false, ErrInvalidHash
	}

	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrInvalidHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, ErrInvalidHash
	}

	// Пустая соль или пустой хеш — повреждённая строка. Без этой проверки
	// argon2 вернул бы пустой ключ, ConstantTimeCompare сравнил бы два пустых
	// слайса и вернул «пароль верный» для любого ввода.
	if len(salt) == 0 || len(want) == 0 {
		return false, ErrInvalidHash
	}

	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(want)))

	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
