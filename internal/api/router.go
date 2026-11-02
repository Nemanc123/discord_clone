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

func NewRouter(pool *pgxpool.Pool, authHandlers *AuthHandlers, guildHandlers *GuildHandlers) http.Handler {
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

	// Liveness: процесс жив. Используется systemd/docker/балансировщиком.
	r.Get("/healthz", liveness)

	// Readiness: живы ли зависимости. Если база недоступна — 503,
	// и балансировщик перестанет слать сюда трафик.
	r.Get("/readyz", readiness(pool))

	r.Route("/api/v1", func(r chi.Router) {
		// Публичные: доступны без токена.
		r.Post("/auth/register", authHandlers.Register)
		r.Post("/auth/login", authHandlers.Login)
		r.Post("/auth/refresh", authHandlers.Refresh)

		// Защищённые: требуют access-токен.
		r.Group(func(r chi.Router) {
			r.Use(authHandlers.RequireAuth)

			r.Post("/auth/logout", authHandlers.Logout)
			r.Post("/auth/logout-all", authHandlers.LogoutAll)
			r.Get("/users/@me", authHandlers.Me)

			// Серверы и каналы.
			r.Post("/guilds", guildHandlers.Create)
			r.Get("/guilds", guildHandlers.List)
			r.Get("/guilds/{guildID}", guildHandlers.Get)
			r.Post("/guilds/{guildID}/channels", guildHandlers.CreateChannel)
			r.Post("/guilds/{guildID}/roles", guildHandlers.CreateRole)
			r.Put("/guilds/{guildID}/members/{userID}/roles/{roleID}", guildHandlers.AssignRole)
			r.Delete("/guilds/{guildID}/members/{userID}/roles/{roleID}", guildHandlers.UnassignRole)
			r.Post("/guilds/{guildID}/invites", guildHandlers.CreateInvite)
			r.Post("/invites/{code}", guildHandlers.JoinByInvite)
		})
	})

	return r
}
