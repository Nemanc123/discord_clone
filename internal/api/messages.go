package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Nemanc123/discord_clone/internal/chat"
	"github.com/Nemanc123/discord_clone/internal/store"
)

type MessageHandlers struct {
	svc *chat.Service
}

func NewMessageHandlers(svc *chat.Service) *MessageHandlers {
	return &MessageHandlers{svc: svc}
}

type createMessageRequest struct {
	Content   string  `json:"content"`
	ReplyToID *string `json:"reply_to_id"`
}

type updateMessageRequest struct {
	Content string `json:"content"`
}

func (h *MessageHandlers) Create(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserIDFrom(r.Context())
	channelID := chi.URLParam(r, "channelID")

	var req createMessageRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "тело запроса должно быть JSON-объектом")
		return
	}

	msg, err := h.svc.Post(r.Context(), userID, channelID, req.Content, req.ReplyToID)
	if err != nil {
		mapChatError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, chat.ToView(msg))
}

func (h *MessageHandlers) List(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserIDFrom(r.Context())
	channelID := chi.URLParam(r, "channelID")

	var before int64
	if raw := r.URL.Query().Get("before"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "validation_error", "before должен быть неотрицательным числом")
			return
		}
		before = parsed
	}

	var limit int32
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "validation_error", "limit должен быть положительным числом")
			return
		}
		limit = int32(parsed)
	}

	msgs, err := h.svc.History(r.Context(), userID, channelID, before, limit)
	if err != nil {
		mapChatError(w, err)
		return
	}

	views := make([]chat.MessageView, 0, len(msgs))
	for _, m := range msgs {
		views = append(views, chat.ToView(m))
	}

	writeJSON(w, http.StatusOK, map[string]any{"messages": views})
}

func (h *MessageHandlers) Update(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserIDFrom(r.Context())
	messageID := chi.URLParam(r, "messageID")

	var req updateMessageRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "тело запроса должно быть JSON-объектом")
		return
	}

	msg, err := h.svc.Edit(r.Context(), userID, messageID, req.Content)
	if err != nil {
		mapChatError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, chat.ToView(msg))
}

func (h *MessageHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserIDFrom(r.Context())
	messageID := chi.URLParam(r, "messageID")

	if err := h.svc.Delete(r.Context(), userID, messageID); err != nil {
		mapChatError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type markReadRequest struct {
	Seq int64 `json:"seq"`
}

func (h *MessageHandlers) MarkRead(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserIDFrom(r.Context())
	channelID := chi.URLParam(r, "channelID")

	var req markReadRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "тело запроса должно быть JSON-объектом")
		return
	}

	if err := h.svc.MarkRead(r.Context(), userID, channelID, req.Seq); err != nil {
		mapChatError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type unreadResponse struct {
	ChannelID string `json:"channel_id"`
	Unread    int64  `json:"unread"`
}

func (h *MessageHandlers) Unread(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserIDFrom(r.Context())
	guildID := chi.URLParam(r, "guildID")

	items, err := h.svc.Unread(r.Context(), userID, guildID)
	if err != nil {
		mapChatError(w, err)
		return
	}

	out := make([]unreadResponse, 0, len(items))
	for _, item := range items {
		out = append(out, unreadResponse{ChannelID: item.ChannelID, Unread: item.Count})
	}

	writeJSON(w, http.StatusOK, map[string]any{"channels": out})
}

func mapChatError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, chat.ErrValidation):
		writeError(w, http.StatusBadRequest, "validation_error", err.Error())
	case errors.Is(err, chat.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "недостаточно прав для этого действия")
	case errors.Is(err, store.ErrMessageNotFound):
		writeError(w, http.StatusNotFound, "not_found", "сообщение не найдено")
	case errors.Is(err, store.ErrChannelNotFound):
		writeError(w, http.StatusNotFound, "not_found", "канал не найден")
	default:
		slog.Error("внутренняя ошибка chat", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "внутренняя ошибка")
	}
}
