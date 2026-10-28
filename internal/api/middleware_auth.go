package api

import (
	"context"
	"net/http"
)

type contextKey string

const (
	ctxUserID    contextKey = "user_id"
	ctxSessionID contextKey = "session_id"
)

// RequireAuth проверяет access-токен и кладёт user_id и session_id в контекст.
//
// Важно: здесь проверяется только подпись и срок. Существование сессии не
// проверяется — access-токен живёт 15 минут, и запрос в БД на каждый вызов
// съел бы весь смысл JWT. Отзыв сессии действует на refresh и на logout.
func (h *AuthHandlers) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := bearerToken(r)
		if raw == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized",
				"требуется заголовок Authorization: Bearer <access_token>")
			return
		}

		claims, err := h.svc.ParseAccessToken(raw)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid_token",
				"токен недействителен или истёк")
			return
		}

		ctx := context.WithValue(r.Context(), ctxUserID, claims.Subject)
		ctx = context.WithValue(ctx, ctxSessionID, claims.SessionID)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func UserIDFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(ctxUserID).(string)
	return id, ok && id != ""
}

func SessionIDFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(ctxSessionID).(string)
	return id, ok && id != ""
}
