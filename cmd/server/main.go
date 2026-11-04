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
	"github.com/Nemanc123/discord_clone/internal/chat"
	"github.com/Nemanc123/discord_clone/internal/config"
	"github.com/Nemanc123/discord_clone/internal/db"
	"github.com/Nemanc123/discord_clone/internal/fanout"
	"github.com/Nemanc123/discord_clone/internal/gateway"
	"github.com/Nemanc123/discord_clone/internal/guild"
	"github.com/Nemanc123/discord_clone/internal/storage"
	"github.com/Nemanc123/discord_clone/internal/store"
	"github.com/Nemanc123/discord_clone/internal/voice"
)

// voicePublisher переводит события голоса в общий формат шлюза, чтобы
// они шли теми же путями, что и сообщения: фильтр по VIEW_CHANNEL,
// Redis fan-out между узлами.
type voicePublisher struct {
	pub chat.Publisher
}

func (p voicePublisher) PublishVoice(ctx context.Context, guildID string, event voice.Event) {
	p.pub.Publish(ctx, guildID, chat.Event{
		Type:      event.Type,
		ChannelID: event.ChannelID,
		Data:      event.Data,
	})
}

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

	guildService := guild.NewService(store.NewWithPool(pool))
	guildHandlers := api.NewGuildHandlers(guildService)

	hub := gateway.NewHub()
	hub.SetFilter(gateway.NewCachedFilter(guildService, 10*time.Second))

	nodeID := newNodeID()

	// Publisher по умолчанию — только хаб. Если Redis доступен, добавляем
	// поверх него pub/sub, чтобы события видели другие экземпляры сервера.
	var publisher chat.Publisher = hub
	if rdb := connectRedis(ctx, cfg.RedisURL); rdb != nil {
		defer func() { _ = rdb.Close() }()
		publisher = fanout.NewPublisher(hub, rdb, nodeID)
		go fanout.NewSubscriber(hub, rdb, nodeID).Run(ctx)
	}

	chatService := chat.NewService(store.NewWithPool(pool), guildService, publisher)

	// Хранилище файлов необязательно: без него сервер работает, но вложения
	// отвечают ошибкой «хранилище не настроено».
	if st, err := storage.New(ctx, storage.Config{
		Endpoint:  cfg.StorageEndpoint,
		AccessKey: cfg.StorageAccessKey,
		SecretKey: cfg.StorageSecretKey,
		Bucket:    cfg.StorageBucket,
	}); err != nil {
		slog.Warn("хранилище недоступно: вложения отключены", "err", err)
	} else {
		chatService = chatService.WithStorage(st)
		slog.Info("хранилище файлов подключено", "endpoint", cfg.StorageEndpoint)
	}

	messageHandlers := api.NewMessageHandlers(chatService)
	presenceHandlers := api.NewPresenceHandlers(hub, guildService)
	gatewayHandler := gateway.NewHandler(hub, authService, guildService)

	if cfg.LiveKitAPISecret == "secret" {
		slog.Warn("используются ключи LiveKit для разработки (devkey/secret)")
	}

	voiceService := voice.NewService(
		store.NewWithPool(pool),
		guildService,
		voice.TokenIssuer{
			APIKey:    cfg.LiveKitAPIKey,
			APISecret: cfg.LiveKitAPISecret,
			TTL:       cfg.LiveKitTokenTTL,
		},
		cfg.LiveKitURL,
		voicePublisher{pub: publisher},
	)

	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: api.NewRouter(api.Deps{
			Pool:     pool,
			Auth:     authHandlers,
			Guild:    guildHandlers,
			Messages: messageHandlers,
			Presence: presenceHandlers,
			Gateway:  gatewayHandler,
			Voice:    api.NewVoiceHandlers(voiceService),
		}),
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
