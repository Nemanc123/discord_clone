// Package config собирает настройки из переменных окружения.
//
// Значения по умолчанию рассчитаны на локальную разработку:
// база живёт в Docker на localhost:5432 с паролем devpass.
// На сервере всё задаётся через переменные окружения, умолчания не используются.
package config

import (
	"log/slog"
	"os"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr    string
	DatabaseURL string
	LogLevel    slog.Level

	JWTSecret  string
	AccessTTL  time.Duration
	RefreshTTL time.Duration

	RedisURL string

	StorageEndpoint  string
	StorageAccessKey string
	StorageSecretKey string
	StorageBucket    string

	LiveKitURL       string
	LiveKitAPIKey    string
	LiveKitAPISecret string
	LiveKitTokenTTL  time.Duration
}

func Load() Config {
	return Config{
		HTTPAddr:    env("HTTP_ADDR", ":8080"),
		DatabaseURL: env("DATABASE_URL", "postgres://dc:devpass@localhost:5432/discord_clone?sslmode=disable"),
		LogLevel:    parseLevel(env("LOG_LEVEL", "info")),
		JWTSecret:   env("JWT_SECRET", "dev-secret-change-me"),
		AccessTTL:   parseDuration("ACCESS_TTL", 15*time.Minute),
		RefreshTTL:  parseDuration("REFRESH_TTL", 720*time.Hour), // 30 дней
		RedisURL:    env("REDIS_URL", "redis://localhost:6379/0"),

		StorageEndpoint:  env("STORAGE_ENDPOINT", "localhost:8333"),
		StorageAccessKey: env("STORAGE_ACCESS_KEY", "dcstorage"),
		StorageSecretKey: env("STORAGE_SECRET_KEY", "devstorage123"),
		StorageBucket:    env("STORAGE_BUCKET", "discord-clone"),

		// --dev у LiveKit использует ровно эти ключи. На сервере — свои.
		LiveKitURL:       env("LIVEKIT_URL", "ws://localhost:7880"),
		LiveKitAPIKey:    env("LIVEKIT_API_KEY", "devkey"),
		LiveKitAPISecret: env("LIVEKIT_API_SECRET", "secret"),
		LiveKitTokenTTL:  parseDuration("LIVEKIT_TOKEN_TTL", 10*time.Minute),
	}
}

func parseDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		slog.Warn("не удалось разобрать длительность, использую значение по умолчанию", "key", key, "value", raw)
		return fallback
	}
	return d
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
