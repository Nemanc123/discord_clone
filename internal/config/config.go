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
)

type Config struct {
	HTTPAddr    string
	DatabaseURL string
	LogLevel    slog.Level
}

func Load() Config {
	return Config{
		HTTPAddr:    env("HTTP_ADDR", ":8080"),
		DatabaseURL: env("DATABASE_URL", "postgres://dc:devpass@localhost:5432/discord_clone?sslmode=disable"),
		LogLevel:    parseLevel(env("LOG_LEVEL", "info")),
	}
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
