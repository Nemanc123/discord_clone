package api

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Nemanc123/discord_clone/internal/auth"
	"github.com/Nemanc123/discord_clone/internal/store"
)

type AuthHandlers struct {
	svc     *auth.Service
	limiter *rateLimiter
}

func NewAuthHandlers(svc *auth.Service) *AuthHandlers {
	// 5 попыток входа в минуту на IP — защита от перебора пароля.
	return &AuthHandlers{
		svc:     svc,
		limiter: newRateLimiter(5, time.Minute),
	}
}

type registerRequest struct {
	Username    string `json:"username"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
}

type loginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type userResponse struct {
	ID          string  `json:"id"`
	Username    string  `json:"username"`
	DisplayName *string `json:"display_name,omitempty"`
	Email       *string `json:"email,omitempty"`
	Status      string  `json:"status"`
	CreatedAt   string  `json:"created_at"`
}

type authResponse struct {
	User         userResponse `json:"user"`
	AccessToken  string       `json:"access_token"`
	RefreshToken string       `json:"refresh_token"`
	TokenType    string       `json:"token_type"`
	ExpiresIn    int          `json:"expires_in"`
}

func toUserResponse(u store.User) userResponse {
	return userResponse{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Email:       u.Email,
		Status:      u.Status,
		CreatedAt:   u.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func toAuthResponse(res auth.Result) authResponse {
	return authResponse{
		User:         toUserResponse(res.User),
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		TokenType:    "Bearer",
		ExpiresIn:    int(time.Until(res.AccessExpiresAt).Seconds()),
	}
}

func (h *AuthHandlers) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "тело запроса должно быть JSON-объектом")
		return
	}

	res, err := h.svc.Register(r.Context(), auth.RegisterInput{
		Username:    req.Username,
		Email:       req.Email,
		DisplayName: req.DisplayName,
		Password:    req.Password,
		UserAgent:   r.UserAgent(),
		IP:          clientIP(r),
	})
	if err != nil {
		mapAuthError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, toAuthResponse(res))
}

func (h *AuthHandlers) Login(w http.ResponseWriter, r *http.Request) {
	if !h.limiter.Allow(clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "слишком много попыток входа, попробуйте через минуту")
		return
	}

	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "тело запроса должно быть JSON-объектом")
		return
	}

	res, err := h.svc.Login(r.Context(), req.Login, req.Password, r.UserAgent(), clientIP(r))
	if err != nil {
		mapAuthError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, toAuthResponse(res))
}

func (h *AuthHandlers) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "тело запроса должно быть JSON-объектом")
		return
	}

	res, err := h.svc.Refresh(r.Context(), req.RefreshToken, r.UserAgent(), clientIP(r))
	if err != nil {
		mapAuthError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, toAuthResponse(res))
}

func (h *AuthHandlers) Logout(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := SessionIDFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "нет активной сессии")
		return
	}

	if err := h.svc.Logout(r.Context(), sessionID); err != nil {
		mapAuthError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *AuthHandlers) LogoutAll(w http.ResponseWriter, r *http.Request) {
	userID, ok := UserIDFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "нет активной сессии")
		return
	}

	if err := h.svc.LogoutAll(r.Context(), userID); err != nil {
		mapAuthError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// clientIP берёт адрес после middleware.RealIP: если сервер стоит за прокси,
// там уже будет X-Forwarded-For, а не адрес балансировщика.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// bearerToken вытаскивает токен из заголовка. Пустая строка — заголовка нет.
func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}
