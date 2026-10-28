package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Nemanc123/discord_clone/internal/guild"
	"github.com/Nemanc123/discord_clone/internal/store"
)

type GuildHandlers struct {
	svc *guild.Service
}

func NewGuildHandlers(svc *guild.Service) *GuildHandlers {
	return &GuildHandlers{svc: svc}
}

type guildResponse struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	IconURL   *string `json:"icon_url,omitempty"`
	OwnerID   string  `json:"owner_id"`
	CreatedAt string  `json:"created_at"`
}

type roleResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Color       int32  `json:"color"`
	Position    int32  `json:"position"`
	Permissions int64  `json:"permissions"`
	IsDefault   bool   `json:"is_default"`
}

type channelResponse struct {
	ID       string  `json:"id"`
	Name     *string `json:"name,omitempty"`
	Type     string  `json:"type"`
	Topic    *string `json:"topic,omitempty"`
	ParentID *string `json:"parent_id,omitempty"`
	Position int32   `json:"position"`
}

type guildDetailResponse struct {
	Guild       guildResponse     `json:"guild"`
	Roles       []roleResponse    `json:"roles"`
	Channels    []channelResponse `json:"channels"`
	Permissions int64             `json:"permissions"`
}

func toGuildResponse(g store.Guild) guildResponse {
	return guildResponse{
		ID:        g.ID,
		Name:      g.Name,
		IconURL:   g.IconURL,
		OwnerID:   g.OwnerID,
		CreatedAt: g.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func toRoleResponse(r store.Role) roleResponse {
	return roleResponse{
		ID:          r.ID,
		Name:        r.Name,
		Color:       r.Color,
		Position:    r.Position,
		Permissions: r.Permissions,
		IsDefault:   r.IsDefault,
	}
}

func toChannelResponse(c store.Channel) channelResponse {
	return channelResponse{
		ID:       c.ID,
		Name:     c.Name,
		Type:     c.Type,
		Topic:    c.Topic,
		ParentID: c.ParentID,
		Position: c.Position,
	}
}

type createGuildRequest struct {
	Name string `json:"name"`
}

func (h *GuildHandlers) Create(w http.ResponseWriter, r *http.Request) {
	userID, ok := UserIDFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "нет активной сессии")
		return
	}

	var req createGuildRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "тело запроса должно быть JSON-объектом")
		return
	}

	detail, err := h.svc.CreateGuild(r.Context(), userID, req.Name)
	if err != nil {
		mapGuildError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, h.toDetail(r, userID, detail))
}

func (h *GuildHandlers) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := UserIDFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "нет активной сессии")
		return
	}

	guilds, err := h.svc.ListGuilds(r.Context(), userID)
	if err != nil {
		mapGuildError(w, err)
		return
	}

	out := make([]guildResponse, 0, len(guilds))
	for _, g := range guilds {
		out = append(out, toGuildResponse(g))
	}

	writeJSON(w, http.StatusOK, map[string]any{"guilds": out})
}

func (h *GuildHandlers) Get(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserIDFrom(r.Context())
	guildID := chi.URLParam(r, "guildID")

	detail, err := h.svc.DetailFor(r.Context(), userID, guildID)
	if err != nil {
		mapGuildError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, h.toDetail(r, userID, detail))
}

func (h *GuildHandlers) toDetail(r *http.Request, userID string, d guild.Detail) guildDetailResponse {
	roles := make([]roleResponse, 0, len(d.Roles))
	for _, role := range d.Roles {
		roles = append(roles, toRoleResponse(role))
	}

	channels := make([]channelResponse, 0, len(d.Channels))
	for _, ch := range d.Channels {
		channels = append(channels, toChannelResponse(ch))
	}

	guildPerms, err := h.svc.Permissions(r.Context(), d.Guild.ID, userID, "")
	if err != nil {
		// Права не критичны для отображения: клиент сможет перезапросить.
		slog.Warn("не удалось вычислить права", "err", err)
	}

	return guildDetailResponse{
		Guild:       toGuildResponse(d.Guild),
		Roles:       roles,
		Channels:    channels,
		Permissions: int64(guildPerms),
	}
}

type createChannelRequest struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Topic    string  `json:"topic"`
	ParentID *string `json:"parent_id"`
}

func (h *GuildHandlers) CreateChannel(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserIDFrom(r.Context())
	guildID := chi.URLParam(r, "guildID")

	var req createChannelRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "тело запроса должно быть JSON-объектом")
		return
	}

	channel, err := h.svc.CreateChannel(r.Context(), userID, guildID, guild.CreateChannelInput{
		Name:     req.Name,
		Type:     req.Type,
		Topic:    req.Topic,
		ParentID: req.ParentID,
	})
	if err != nil {
		mapGuildError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, toChannelResponse(channel))
}

type createRoleRequest struct {
	Name        string `json:"name"`
	Color       int32  `json:"color"`
	Permissions int64  `json:"permissions"`
}

func (h *GuildHandlers) CreateRole(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserIDFrom(r.Context())
	guildID := chi.URLParam(r, "guildID")

	var req createRoleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "тело запроса должно быть JSON-объектом")
		return
	}

	role, err := h.svc.CreateRole(r.Context(), userID, guildID, guild.CreateRoleInput{
		Name:        req.Name,
		Color:       req.Color,
		Permissions: req.Permissions,
	})
	if err != nil {
		mapGuildError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, toRoleResponse(role))
}

func (h *GuildHandlers) AssignRole(w http.ResponseWriter, r *http.Request) {
	h.changeRole(w, r, true)
}

func (h *GuildHandlers) UnassignRole(w http.ResponseWriter, r *http.Request) {
	h.changeRole(w, r, false)
}

func (h *GuildHandlers) changeRole(w http.ResponseWriter, r *http.Request, assign bool) {
	userID, _ := UserIDFrom(r.Context())
	guildID := chi.URLParam(r, "guildID")
	targetUserID := chi.URLParam(r, "userID")
	roleID := chi.URLParam(r, "roleID")

	var err error
	if assign {
		err = h.svc.AssignRole(r.Context(), userID, guildID, targetUserID, roleID)
	} else {
		err = h.svc.UnassignRole(r.Context(), userID, guildID, targetUserID, roleID)
	}
	if err != nil {
		mapGuildError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type createInviteRequest struct {
	ChannelID string `json:"channel_id"`
	MaxUses   *int32 `json:"max_uses"`
}

func (h *GuildHandlers) CreateInvite(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserIDFrom(r.Context())
	guildID := chi.URLParam(r, "guildID")

	var req createInviteRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "тело запроса должно быть JSON-объектом")
		return
	}

	code, err := h.svc.CreateInvite(r.Context(), userID, guildID, req.ChannelID, req.MaxUses)
	if err != nil {
		mapGuildError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"code": code})
}

func (h *GuildHandlers) JoinByInvite(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserIDFrom(r.Context())
	code := chi.URLParam(r, "code")

	guildID, err := h.svc.JoinByInvite(r.Context(), userID, code)
	if err != nil {
		mapGuildError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"guild_id": guildID})
}

// mapGuildError переводит доменные ошибки в HTTP-статусы.
func mapGuildError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, guild.ErrValidation):
		writeError(w, http.StatusBadRequest, "validation_error", err.Error())
	case errors.Is(err, guild.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, guild.ErrHierarchy):
		writeError(w, http.StatusForbidden, "hierarchy_violation", err.Error())
	case errors.Is(err, store.ErrGuildNotFound), errors.Is(err, store.ErrChannelNotFound),
		errors.Is(err, store.ErrRoleNotFound), errors.Is(err, store.ErrInviteNotFound):
		writeError(w, http.StatusNotFound, "not_found", "объект не найден")
	case errors.Is(err, store.ErrInviteExhausted):
		writeError(w, http.StatusGone, "invite_exhausted", "приглашение больше не действует")
	default:
		slog.Error("внутренняя ошибка guild", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "внутренняя ошибка")
	}
}
