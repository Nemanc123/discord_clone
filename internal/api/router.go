// Package api — HTTP-слой.
//
// Маршрутизация отделена от обработчиков: router знает «какие пути куда»,
// обработчики — «что делать».
package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Deps — всё, что нужно маршрутизатору. Структура вместо длинного списка
// аргументов: добавить обработчик — добавить поле, а не менять сигнатуру.
type Deps struct {
	Pool     *pgxpool.Pool
	Auth     *AuthHandlers
	Guild    *GuildHandlers
	Messages *MessageHandlers
	Presence *PresenceHandlers
	Gateway  http.Handler
	Voice    *VoiceHandlers
}

func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	// middleware.RealIP намеренно НЕ используется: он подменяет r.RemoteAddr
	// значением X-Forwarded-For от кого угодно, и тогда лимит попыток входа
	// обходится подделкой заголовка (staticcheck SA1019, GHSA-3fxj-6jh8-hvhx).
	// Когда появится балансировщик, добавим свой middleware, который доверяет
	// X-Forwarded-For только известным адресам прокси.
	r.Use(middleware.Recoverer) // паника в обработчике не роняет процесс
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(requestLogger)

	// Liveness: процесс жив.
	r.Get("/healthz", liveness)
	// Readiness: живы ли зависимости.
	r.Get("/readyz", readiness(d.Pool))

	// WebSocket-шлюз. Аутентификация происходит внутри рукопожатия
	// (IDENTIFY), поэтому middleware RequireAuth здесь не нужен.
	if d.Gateway != nil {
		r.Handle("/gateway", d.Gateway)
	}

	// Вебхуки LiveKit. Аутентификация — подпись в заголовке, не access-токен.
	if d.Voice != nil {
		r.Post("/webhooks/livekit", d.Voice.Webhook)
	}

	r.Route("/api/v1", func(r chi.Router) {
		// Публичные: доступны без токена.
		r.Post("/auth/register", d.Auth.Register)
		r.Post("/auth/login", d.Auth.Login)
		r.Post("/auth/refresh", d.Auth.Refresh)

		// Защищённые: требуют access-токен.
		r.Group(func(r chi.Router) {
			r.Use(d.Auth.RequireAuth)

			r.Post("/auth/logout", d.Auth.Logout)
			r.Post("/auth/logout-all", d.Auth.LogoutAll)
			r.Get("/users/@me", d.Auth.Me)

			// Серверы и каналы.
			r.Post("/guilds", d.Guild.Create)
			r.Get("/guilds", d.Guild.List)
			r.Get("/guilds/{guildID}", d.Guild.Get)
			r.Post("/guilds/{guildID}/channels", d.Guild.CreateChannel)
			r.Post("/guilds/{guildID}/roles", d.Guild.CreateRole)
			r.Put("/guilds/{guildID}/members/{userID}/roles/{roleID}", d.Guild.AssignRole)
			r.Delete("/guilds/{guildID}/members/{userID}/roles/{roleID}", d.Guild.UnassignRole)
			r.Post("/guilds/{guildID}/invites", d.Guild.CreateInvite)
			r.Post("/invites/{code}", d.Guild.JoinByInvite)

			// Сообщения.
			r.Post("/channels/{channelID}/messages", d.Messages.Create)
			r.Get("/channels/{channelID}/messages", d.Messages.List)
			r.Post("/channels/{channelID}/attachments/presign", d.Messages.Presign)
			r.Get("/attachments/{attachmentID}/download", d.Messages.DownloadAttachment)
			r.Patch("/messages/{messageID}", d.Messages.Update)
			r.Delete("/messages/{messageID}", d.Messages.Delete)
			r.Post("/channels/{channelID}/read", d.Messages.MarkRead)
			r.Get("/guilds/{guildID}/unread", d.Messages.Unread)
			r.Get("/guilds/{guildID}/presence", d.Presence.List)

			// Голос.
			if d.Voice != nil {
				r.Post("/channels/{channelID}/voice/join", d.Voice.Join)
				r.Get("/channels/{channelID}/voice", d.Voice.Participants)
			}
		})
	})

	return r
}
