package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Nemanc123/discord_clone/internal/guild"
)

// PresenceSource — источник списка онлайн-участников. Это хаб шлюза.
// Интерфейс, чтобы HTTP-слой не зависел от внутренностей шлюза.
type PresenceSource interface {
	OnlineUsers(guildID string) []string
}

type PresenceHandlers struct {
	hub    PresenceSource
	guilds *guild.Service
}

func NewPresenceHandlers(hub PresenceSource, gs *guild.Service) *PresenceHandlers {
	return &PresenceHandlers{hub: hub, guilds: gs}
}

// List отдаёт, кто из участников сервера сейчас в сети.
func (h *PresenceHandlers) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := UserIDFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "нет активной сессии")
		return
	}

	guildID := chi.URLParam(r, "guildID")

	// Присутствие сервера видят только его участники.
	if err := h.guilds.RequireMember(r.Context(), userID, guildID); err != nil {
		if errors.Is(err, guild.ErrForbidden) {
			writeError(w, http.StatusForbidden, "forbidden", "вы не участник этого сервера")
			return
		}
		mapGuildError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"online": h.hub.OnlineUsers(guildID)})
}
