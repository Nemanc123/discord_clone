package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/Nemanc123/discord_clone/internal/auth"
	"github.com/Nemanc123/discord_clone/internal/store"
)

// apiError — единый формат ошибки. Клиенту нужен машинный код,
// а не текст сообщения: текст может меняться и переводиться.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorEnvelope struct {
	Error apiError `json:"error"`
}

const maxRequestBody = 1 << 20 // 1 МиБ

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorEnvelope{Error: apiError{Code: code, Message: message}})
}

// decodeJSON читает тело с ограничением размера и запретом неизвестных полей.
// LimitReader защищает от запроса на гигабайт, DisallowUnknownFields —
// от опечаток вроде "passwrod", которые иначе прошли бы молча.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("в теле запроса больше одного JSON-объекта")
	}

	return nil
}

// mapAuthError переводит доменную ошибку в HTTP-статус.
func mapAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrValidation):
		writeError(w, http.StatusBadRequest, "validation_error", err.Error())
	case errors.Is(err, auth.ErrUserExists), errors.Is(err, store.ErrUserExists):
		writeError(w, http.StatusConflict, "user_exists", "такой логин или почта уже заняты")
	case errors.Is(err, auth.ErrInvalidCredentials):
		// Намеренно одинаковый ответ на «нет такого» и «неверный пароль».
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "неверный логин или пароль")
	case errors.Is(err, auth.ErrSessionInvalid):
		writeError(w, http.StatusUnauthorized, "invalid_session", "сессия недействительна, войдите заново")
	default:
		slog.Error("внутренняя ошибка auth", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "внутренняя ошибка")
	}
}
