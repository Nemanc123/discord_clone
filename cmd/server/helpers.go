package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// newNodeID даёт идентификатор процесса, чтобы узел не получал собственные
// события обратно через Redis.
func newNodeID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "node"
	}
	return hex.EncodeToString(buf)
}

// connectRedis возвращает nil, если Redis недоступен: сервер продолжает
// работать без межузловой рассылки, только с локальным хабом.
func connectRedis(ctx context.Context, rawURL string) *redis.Client {
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		slog.Warn("redis: разбор адреса", "err", err)
		return nil
	}

	client := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if err := client.Ping(pingCtx).Err(); err != nil {
		slog.Warn("redis недоступен: события только внутри узла", "err", err)
		_ = client.Close()
		return nil
	}

	slog.Info("redis подключён")
	return client
}
