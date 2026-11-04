package api

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Nemanc123/discord_clone/internal/store"
	"github.com/Nemanc123/discord_clone/internal/voice"
)

// maxWebhookBody — предохранитель от гигантского тела. Уведомления LiveKit
// весят килобайты; мегабайта хватает с запасом.
const maxWebhookBody = 1 << 20

type VoiceHandlers struct {
	svc *voice.Service
}

func NewVoiceHandlers(svc *voice.Service) *VoiceHandlers {
	return &VoiceHandlers{svc: svc}
}

// Join выдаёт токен LiveKit для входа в голосовой канал.
func (h *VoiceHandlers) Join(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserIDFrom(r.Context())
	channelID := chi.URLParam(r, "channelID")

	result, err := h.svc.Join(r.Context(), userID, channelID)
	if err != nil {
		mapVoiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// Participants — кто сейчас сидит в канале.
func (h *VoiceHandlers) Participants(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserIDFrom(r.Context())
	channelID := chi.URLParam(r, "channelID")

	participants, err := h.svc.Participants(r.Context(), userID, channelID)
	if err != nil {
		mapVoiceError(w, err)
		return
	}

	// Явное представление: у store.VoiceParticipant нет JSON-тегов, и без
	// этого клиент получил бы UserID вместо user_id.
	out := make([]voiceParticipantResponse, 0, len(participants))
	for _, p := range participants {
		out = append(out, voiceParticipantResponse{
			UserID:      p.UserID,
			Username:    p.Username,
			DisplayName: p.DisplayName,
			JoinedAt:    p.JoinedAt,
			SelfMute:    p.SelfMute,
			SelfDeaf:    p.SelfDeaf,
			SelfVideo:   p.SelfVideo,
			SelfStream:  p.SelfStream,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"participants": out})
}

type voiceParticipantResponse struct {
	UserID      string    `json:"user_id"`
	Username    string    `json:"username"`
	DisplayName *string   `json:"display_name,omitempty"`
	JoinedAt    time.Time `json:"joined_at"`
	SelfMute    bool      `json:"self_mute"`
	SelfDeaf    bool      `json:"self_deaf"`
	SelfVideo   bool      `json:"self_video"`
	SelfStream  bool      `json:"self_stream"`
}

// Webhook принимает уведомления LiveKit. Аутентификации по access-токену
// здесь нет: запрос приходит от сервера LiveKit, а не от пользователя.
// Подлинность доказывает подпись в заголовке Authorization.
func (h *VoiceHandlers) Webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "не удалось прочитать тело")
		return
	}

	if err := h.svc.HandleWebhook(r.Context(), r.Header.Get("Authorization"), body); err != nil {
		if errors.Is(err, voice.ErrInvalidWebhook) {
			writeError(w, http.StatusUnauthorized, "invalid_signature", "подпись вебхука неверна")
			return
		}
		slog.Error("вебхук LiveKit", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "внутренняя ошибка")
		return
	}

	w.WriteHeader(http.StatusOK)
}

func mapVoiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, voice.ErrValidation):
		writeError(w, http.StatusBadRequest, "validation_error", err.Error())
	case errors.Is(err, voice.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "недостаточно прав для этого действия")
	case errors.Is(err, store.ErrChannelNotFound):
		writeError(w, http.StatusNotFound, "not_found", "канал не найден")
	default:
		slog.Error("внутренняя ошибка voice", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "внутренняя ошибка")
	}
}
