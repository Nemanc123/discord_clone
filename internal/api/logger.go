package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// requestLogger пишет одну строку на каждый запрос.
// Логи структурные (JSON): их можно фильтровать и агрегировать,
// в отличие от «println».
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(wrapped, r)

		slog.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", wrapped.Status(),
			"bytes", wrapped.BytesWritten(),
			"dur_ms", time.Since(start).Milliseconds(),
			"req_id", middleware.GetReqID(r.Context()),
		)
	})
}
