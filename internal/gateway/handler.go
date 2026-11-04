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
)

type Handler struct {
	hub      *Hub
	auth     *auth.Service
	guilds   *guild.Service
	upgrader websocket.Upgrader
}

func NewHandler(hub *Hub, authSvc *auth.Service, gs *guild.Service) *Handler {
	return &Handler{
		hub:    hub,
		auth:   authSvc,
		guilds: gs,
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

type identifyFrame struct {
	Op int `json:"op"`
	D  struct {
		Token string `json:"token"`
	} `json:"d"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade уже отправил ответ клиенту.
		return
	}

	// HELLO: сообщаем интервал heartbeat.
	_ = conn.SetWriteDeadline(time.Now().Add(identifyWait))
	hello := envelope{
		Op: OpHello,
		D:  map[string]int64{"heartbeat_interval": int64(heartbeatInterval / time.Millisecond)},
	}
	if err := conn.WriteJSON(hello); err != nil {
		_ = conn.Close()
		return
	}

	// Ждём IDENTIFY. Без него соединение не живёт дольше identifyWait.
	_ = conn.SetReadDeadline(time.Now().Add(identifyWait))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		_ = conn.Close()
		return
	}

	var frame identifyFrame
	if err := json.Unmarshal(raw, &frame); err != nil || frame.Op != OpIdentify {
		closeWithCode(conn, 4003, "first frame must be IDENTIFY")
		return
	}

	claims, err := h.auth.ParseAccessToken(frame.D.Token)
	if err != nil {
		closeWithCode(conn, 4004, "invalid token")
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

	client := newClient(h.hub, conn, claims.Subject, claims.SessionID, guildIDs)
	h.hub.Register(client)

	ready := envelope{
		Op: OpDispatch,
		T:  "READY",
		S:  1,
		D: map[string]any{
			"session_id":         claims.SessionID,
			"user":               map[string]string{"id": claims.Subject},
			"guilds":             guildIDs,
			"heartbeat_interval": int64(heartbeatInterval / time.Millisecond),
		},
	}
	_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := conn.WriteJSON(ready); err != nil {
		h.hub.Unregister(client)
		_ = conn.Close()
		return
	}
	client.seq.Store(1)

	slog.Info("шлюз: клиент подключился",
		"user_id", claims.Subject, "guilds", len(guildIDs))

	go client.writePump()
	client.readPump()
}

func closeWithCode(conn *websocket.Conn, code int, reason string) {
	_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
	_ = conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason))
	_ = conn.Close()
}
