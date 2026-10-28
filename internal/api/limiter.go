package api

import (
	"sync"
	"time"
)

// rateLimiter — простейшее ограничение «N попыток за окно».
//
// Хранится в памяти процесса. Это значит, что при нескольких экземплярах
// сервера лимит умножается на их число. Для этапа 1 приемлемо; на этапе 3,
// когда появится Redis, счётчики переедут туда (см. docs/01-architecture.md).
type rateLimiter struct {
	mu     sync.Mutex
	hits   map[string]rateBucket
	limit  int
	window time.Duration
	now    func() time.Time
}

type rateBucket struct {
	count int
	reset time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		hits:   make(map[string]rateBucket),
		limit:  limit,
		window: window,
		now:    time.Now,
	}
}

// Allow сообщает, можно ли пропустить запрос с данным ключом.
func (l *rateLimiter) Allow(key string) bool {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.hits) > 10_000 {
		l.cleanup(now)
	}

	bucket, ok := l.hits[key]
	if !ok || now.After(bucket.reset) {
		l.hits[key] = rateBucket{count: 1, reset: now.Add(l.window)}
		return true
	}

	if bucket.count >= l.limit {
		return false
	}

	bucket.count++
	l.hits[key] = bucket
	return true
}

// cleanup вызывается редко и только когда карта разрослась:
// иначе она станет утечкой памяти на ботовых запросах.
func (l *rateLimiter) cleanup(now time.Time) {
	for key, bucket := range l.hits {
		if now.After(bucket.reset) {
			delete(l.hits, key)
		}
	}
}
