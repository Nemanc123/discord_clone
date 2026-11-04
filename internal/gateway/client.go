package gateway

import (
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 90 * time.Second
	sendBuffer = 64
)

// envelope — конверт события. Единый формат для всех кадров протокола.
type envelope struct {
	Op int    `json:"op"`
	T  string `json:"t,omitempty"`
	S  int64  `json:"s,omitempty"`
	D  any    `json:"d,omitempty"`
}

type Client struct {
	hub       *Hub
	conn      *websocket.Conn
	userID    string
	sessionID string
	guilds    []string
	send      chan []byte
	seq       atomic.Int64
	closed    atomic.Bool
}

func newClient(hub *Hub, conn *websocket.Conn, userID, sessionID string, guilds []string) *Client {
	return &Client{
		hub:       hub,
		conn:      conn,
		userID:    userID,
		sessionID: sessionID,
		guilds:    guilds,
		send:      make(chan []byte, sendBuffer),
	}
}

func (c *Client) guildIDs() []string { return c.guilds }

// dispatch кладёт событие в очередь записи.
// Если клиент не успевает читать — соединение закрывается:
// держать бесконечную очередь опаснее, чем разорвать связь.
func (c *Client) dispatch(eventType string, data any) {
	payload, err := json.Marshal(envelope{
		Op: OpDispatch,
		T:  eventType,
		S:  c.seq.Add(1),
		D:  data,
	})
	if err != nil {
		slog.Error("кодирование события", "err", err, "type", eventType)
		return
	}

	select {
	case c.send <- payload:
	default:
		slog.Warn("клиент не успевает читать, закрываю", "user_id", c.userID)
		c.close()
	}
}

func (c *Client) sendFrame(env envelope) {
	payload, err := json.Marshal(env)
	if err != nil {
		slog.Error("кодирование кадра", "err", err, "op", env.Op)
		return
	}
	select {
	case c.send <- payload:
	default:
		c.close()
	}
}

func (c *Client) close() {
	if c.closed.CompareAndSwap(false, true) {
		close(c.send)
	}
}

// writePump — единственная горутина, которая пишет в сокет.
// WebSocket не поддерживает параллельную запись из нескольких горутин.
func (c *Client) writePump() {
	ticker := time.NewTicker(pongWait / 3)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// readPump читает кадры клиента. Сейчас поддерживается только heartbeat:
// отправка сообщений идёт через HTTP, как в Discord.
func (c *Client) readPump() {
	defer func() {
		c.hub.Unregister(c)
		c.close()
		_ = c.conn.Close()
	}()

	c.conn.SetReadLimit(4096)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))

		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			slog.Debug("неразборчивый кадр", "err", err, "user_id", c.userID)
			continue
		}

		switch env.Op {
		case OpHeartbeat:
			c.sendFrame(envelope{Op: OpHeartbeatAck})
		case OpIdentify:
			// Повторный IDENTIFY на этом этапе не поддерживается.
			slog.Debug("повторный identify проигнорирован", "user_id", c.userID)
		default:
			slog.Debug("неизвестный op", "op", env.Op, "user_id", c.userID)
		}
	}
}
