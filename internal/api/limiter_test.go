package api

import (
	"testing"
	"time"
)

func TestRateLimiterAllowsUpToLimit(t *testing.T) {
	now := time.Now()
	limiter := newRateLimiter(3, time.Minute)
	limiter.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if !limiter.Allow("127.0.0.1") {
			t.Fatalf("попытка %d должна быть разрешена", i+1)
		}
	}

	if limiter.Allow("127.0.0.1") {
		t.Fatal("четвёртая попытка должна быть отклонена")
	}
}

func TestRateLimiterIsPerKey(t *testing.T) {
	now := time.Now()
	limiter := newRateLimiter(1, time.Minute)
	limiter.now = func() time.Time { return now }

	if !limiter.Allow("10.0.0.1") {
		t.Fatal("первый ключ должен проходить")
	}
	if !limiter.Allow("10.0.0.2") {
		t.Fatal("второй ключ не должен упираться в лимит первого")
	}
	if limiter.Allow("10.0.0.1") {
		t.Fatal("первый ключ должен быть заблокирован")
	}
}

func TestRateLimiterResetsAfterWindow(t *testing.T) {
	now := time.Now()
	limiter := newRateLimiter(1, time.Minute)
	limiter.now = func() time.Time { return now }

	limiter.Allow("127.0.0.1")
	if limiter.Allow("127.0.0.1") {
		t.Fatal("внутри окна должно быть отклонено")
	}

	now = now.Add(2 * time.Minute)
	if !limiter.Allow("127.0.0.1") {
		t.Fatal("после окна должно снова разрешаться")
	}
}
