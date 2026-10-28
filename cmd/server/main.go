// Точка входа сервера.
//
// Запуск:
//
//	go run ./cmd/server
//
// Переменные окружения:
//
//	HTTP_ADDR     адрес и порт (по умолчанию :8080)
//	DATABASE_URL  строка подключения к PostgreSQL
//	LOG_LEVEL     debug | info | warn | error
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Nemanc123/discord_clone/internal/api"
	"github.com/Nemanc123/discord_clone/internal/auth"
	"github.com/Nemanc123/discord_clone/internal/config"
	"github.com/Nemanc123/discord_clone/internal/db"
	"github.com/Nemanc123/discord_clone/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped with error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))
	slog.SetDefault(logger)

	// Контекст отменяется по Ctrl+C или SIGTERM — так работает graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()
	slog.Info("database connected")

	if cfg.JWTSecret == "dev-secret-change-me" {
		slog.Warn("JWT_SECRET не задан, используется значение для разработки")
	}

	authService := auth.NewService(store.NewWithPool(pool), cfg.JWTSecret, cfg.AccessTTL, cfg.RefreshTTL)
	authHandlers := api.NewAuthHandlers(authService)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.NewRouter(pool, authHandlers),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// ListenAndServe блокирует поток, поэтому живёт в отдельной горутине.
	errCh := make(chan error, 1)
	go func() {
		slog.Info("server listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	}

	// Даём открытым запросам 10 секунд, чтобы завершиться.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	slog.Info("stopped")
	return nil
}
