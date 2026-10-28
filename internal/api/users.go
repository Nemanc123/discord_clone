package api

import (
	"net/http"

	"github.com/Nemanc123/discord_clone/internal/store"
)

// Me — профиль текущего пользователя. Первый защищённый эндпоинт:
// он же проверяет, что middleware и токены работают.
func (h *AuthHandlers) Me(w http.ResponseWriter, r *http.Request) {
	userID, ok := UserIDFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "нет активной сессии")
		return
	}

	user, err := h.svc.User(r.Context(), userID)
	if err == store.ErrNotFound {
		writeError(w, http.StatusUnauthorized, "unauthorized", "пользователь не найден")
		return
	}
	if err != nil {
		mapAuthError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, toUserResponse(user))
}
