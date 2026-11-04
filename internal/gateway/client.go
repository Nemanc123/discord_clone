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

// envelope — конверт кадра. Единый формат для всех сообщений протокола.
type envelope struct {
	Op int    `json:"op"`
	T  string `json:"t,omitempty"`
	S  int64  `json:"s,omitempty"`
	D  any    `json:"d,omitempty"`
}

// Client — одно активное соединение. Буфер событий живёт в Session,
// поэтому потеря соединения не теряет события.
type Client struct {
	hub     *Hub
	conn    *websocket.Conn
	session *Session
	send    chan []byte
	closed  atomic.Bool
}

func newClient(hub *Hub, conn *websocket.Conn, session *Session) *Client {
	return &Client{
		hub:     hub,
		conn:    conn,
		session: session,
		send:    make(chan []byte, sendBuffer),
	}
}

// push ставит готовый кадр в очередь записи.
func (c *Client) push(payload []byte) {
	select {
	case c.send <- payload:
	default:
		// Клиент не успевает читать: закрываем соединение, но сессия
		// остаётся — после переподключения события догонятся через RESUME.
		slog.Warn("клиент не успевает читать, закрываю", "user_id", c.session.UserID)
		c.close()
	}
}

// sendFrame отправляет служебный кадр, который не попадает в буфер
// (например, HEARTBEAT_ACK).
func (c *Client) sendFrame(env envelope) {
	payload, err := json.Marshal(env)
	if err != nil {
		slog.Error("кодирование кадра", "err", err, "op", env.Op)
		return
	}
	c.push(payload)
}

func (c *Client) close() {
	if c.closed.CompareAndSwap(false, true) {
		close(c.send)
	}
}

// writePump — единственная горутина, пишущая в сокет.
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

// readPump читает кадры клиента. Отправка сообщений идёт через HTTP,
// поэтому из входящих поддерживается только heartbeat.
func (c *Client) readPump() {
	defer func() {
		// Сессию не удаляем: снимаем только соединение, чтобы события
		// продолжали копиться до переподключения.
		c.hub.Detach(c.session)
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
			slog.Debug("неразборчивый кадр", "err", err, "user_id", c.session.UserID)
			continue
		}

		switch env.Op {
		case OpHeartbeat:
			c.sendFrame(envelope{Op: OpHeartbeatAck})
		default:
			slog.Debug("неизвестный op", "op", env.Op, "user_id", c.session.UserID)
		}
	}
}
