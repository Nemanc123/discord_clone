package auth

import (
	"strings"
	"testing"
)

const samplePassword = "correct horse battery staple"

func TestHashPasswordProducesDifferentHashes(t *testing.T) {
	first, err := HashPassword(samplePassword)
	if err != nil {
		t.Fatalf("хеш: %v", err)
	}
	second, err := HashPassword(samplePassword)
	if err != nil {
		t.Fatalf("хеш: %v", err)
	}

	if first == second {
		t.Fatal("два хеша одного пароля совпали — соль не работает")
	}
	if !strings.HasPrefix(first, "$argon2id$") {
		t.Fatalf("неожиданный формат хеша: %s", first)
	}
}

func TestVerifyPassword(t *testing.T) {
	hash, err := HashPassword(samplePassword)
	if err != nil {
		t.Fatalf("хеш: %v", err)
	}

	ok, err := VerifyPassword(samplePassword, hash)
	if err != nil {
		t.Fatalf("проверка: %v", err)
	}
	if !ok {
		t.Fatal("верный пароль не прошёл проверку")
	}

	ok, err = VerifyPassword(samplePassword+"x", hash)
	if err != nil {
		t.Fatalf("проверка: %v", err)
	}
	if ok {
		t.Fatal("неверный пароль прошёл проверку")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	cases := []string{
		"",
		"plain-text",
		"$argon2i$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",  // не argon2id
		"$argon2id$v=99$m=65536,t=3,p=4$c2FsdA$aGFzaA", // другая версия
		"$argon2id$v=19$broken$c2FsdA$aGFzaA",          // кривые параметры
		"$argon2id$v=19$m=65536,t=3,p=4$!!!$aGFzaA",    // битая соль
		"$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$",       // пустой хеш
	}

	for _, encoded := range cases {
		if _, err := VerifyPassword("любой", encoded); err == nil {
			t.Errorf("ожидали ошибку формата для %q", encoded)
		}
	}
}

func TestPasswordIsCaseSensitive(t *testing.T) {
	hash, err := HashPassword("Password123")
	if err != nil {
		t.Fatalf("хеш: %v", err)
	}

	ok, err := VerifyPassword("password123", hash)
	if err != nil {
		t.Fatalf("проверка: %v", err)
	}
	if ok {
		t.Fatal("пароль оказался регистронезависимым")
	}
}
