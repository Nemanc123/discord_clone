package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Nemanc123/discord_clone/internal/auth"
	"github.com/Nemanc123/discord_clone/internal/guild"
)

const (
	// 41250 мс — как в Discord: клиент шлёт heartbeat раз в ~41 секунду.
	heartbeatInterval = 41250 * time.Millisecond
	identifyWait      = 10 * time.Second

	closeInvalidSession = 4004
)

type Handler struct {
	hub      *Hub
	auth     *auth.Service
	guilds   *guild.Service
	sessions *SessionStore
	upgrader websocket.Upgrader
}

func NewHandler(hub *Hub, authSvc *auth.Service, gs *guild.Service) *Handler {
	return &Handler{
		hub:      hub,
		auth:     authSvc,
		guilds:   gs,
		sessions: NewSessionStore(),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			// Проверка Origin отключена осознанно: клиент — нативное
			// приложение Flutter, у него нет Origin. Когда появится
			// веб-клиент, здесь нужно будет ограничить список источников.
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
}

type handshakeFrame struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
}

type identifyData struct {
	Token string `json:"token"`
}

type resumeData struct {
	Token     string `json:"token"`
	SessionID string `json:"session_id"`
	Seq       int64  `json:"seq"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	_ = conn.SetWriteDeadline(time.Now().Add(identifyWait))
	hello := envelope{
		Op: OpHello,
		D:  map[string]int64{"heartbeat_interval": int64(heartbeatInterval / time.Millisecond)},
	}
	if err := conn.WriteJSON(hello); err != nil {
		_ = conn.Close()
		return
	}

	_ = conn.SetReadDeadline(time.Now().Add(identifyWait))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		_ = conn.Close()
		return
	}

	var frame handshakeFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		closeWithCode(conn, 4003, "first frame must be IDENTIFY or RESUME")
		return
	}

	switch frame.Op {
	case OpIdentify:
		h.handleIdentify(r, conn, frame.D)
	case OpResume:
		h.handleResume(r, conn, frame.D)
	default:
		closeWithCode(conn, 4003, "first frame must be IDENTIFY or RESUME")
	}
}

func (h *Handler) handleIdentify(r *http.Request, conn *websocket.Conn, raw json.RawMessage) {
	var data identifyData
	if err := json.Unmarshal(raw, &data); err != nil {
		closeWithCode(conn, 4003, "bad IDENTIFY payload")
		return
	}

	claims, err := h.auth.ParseAccessToken(data.Token)
	if err != nil {
		closeWithCode(conn, closeInvalidSession, "invalid token")
		return
	}

	guilds, err := h.guilds.ListGuilds(r.Context(), claims.Subject)
	if err != nil {
		slog.Error("шлюз: список серверов", "err", err, "user_id", claims.Subject)
		closeWithCode(conn, 4000, "internal error")
		return
	}

	guildIDs := make([]string, 0, len(guilds))
	for _, g := range guilds {
		guildIDs = append(guildIDs, g.ID)
	}

	session := h.sessions.Create(claims.Subject, guildIDs)

	ready := map[string]any{
		"session_id":         session.ID,
		"user":               map[string]string{"id": session.UserID},
		"guilds":             guildIDs,
		"heartbeat_interval": int64(heartbeatInterval / time.Millisecond),
	}

	payload, err := session.appendDispatch("READY", ready)
	if err != nil {
		_ = conn.Close()
		return
	}

	_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		_ = conn.Close()
		return
	}

	client := newClient(h.hub, conn, session)
	h.hub.Attach(session, client)

	slog.Info("шлюз: клиент подключился", "user_id", session.UserID, "guilds", len(guildIDs))

	go client.writePump()
	client.readPump()
}

func (h *Handler) handleResume(r *http.Request, conn *websocket.Conn, raw json.RawMessage) {
	var data resumeData
	if err := json.Unmarshal(raw, &data); err != nil {
		closeWithCode(conn, 4003, "bad RESUME payload")
		return
	}

	claims, err := h.auth.ParseAccessToken(data.Token)
	if err != nil {
		closeWithCode(conn, closeInvalidSession, "invalid token")
		return
	}

	session, ok := h.sessions.Get(data.SessionID)
	if !ok {
		// Сессии нет или она истекла: клиент должен сделать полный IDENTIFY.
		_ = conn.WriteJSON(envelope{Op: OpInvalidSession})
		closeWithCode(conn, closeInvalidSession, "unknown session")
		return
	}

	// Чужую сессию подхватить нельзя, даже с валидным токеном.
	if session.UserID != claims.Subject {
		_ = conn.WriteJSON(envelope{Op: OpInvalidSession})
		closeWithCode(conn, closeInvalidSession, "session belongs to another user")
		return
	}

	events, ok := session.eventsAfter(data.Seq)
	if !ok {
		_ = conn.WriteJSON(envelope{Op: OpInvalidSession})
		closeWithCode(conn, closeInvalidSession, "sequence too old")
		return
	}

	h.sessions.Touch(session)

	// Догоняем пропущенное напрямую в сокет: writePump ещё не запущен.
	_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
	for _, payload := range events {
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			_ = conn.Close()
			return
		}
	}

	resumed, err := session.appendDispatch("RESUMED", map[string]any{})
	if err != nil {
		_ = conn.Close()
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, resumed); err != nil {
		_ = conn.Close()
		return
	}

	client := newClient(h.hub, conn, session)
	h.hub.Attach(session, client)

	slog.Info("шлюз: сессия восстановлена",
		"user_id", session.UserID, "session_id", session.ID, "replayed", len(events))

	go client.writePump()
	client.readPump()
}

func closeWithCode(conn *websocket.Conn, code int, reason string) {
	_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
	_ = conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason))
	_ = conn.Close()
}
