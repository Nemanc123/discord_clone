package gateway_test

// Помощники для чтения потока кадров: presence добавляет события в поток,
// поэтому тесты не должны рассчитывать на «ровно следующий кадр».

import (
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// readUntilOp читает кадры, пока не встретит нужный op.
func readUntilOp(t *testing.T, conn *websocket.Conn, want int) {
	t.Helper()

	for i := 0; i < 20; i++ {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

		var frame struct {
			Op int `json:"op"`
		}
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatalf("ожидали op %d, получили ошибку: %v", want, err)
		}
		if frame.Op == want {
			return
		}
	}

	t.Fatalf("не дождались op %d за 20 кадров", want)
}

// readUntilEvent читает кадры, пока не встретит событие с нужным именем.
func readUntilEvent(t *testing.T, conn *websocket.Conn, want string) dispatchFrame {
	t.Helper()

	for i := 0; i < 20; i++ {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

		var frame dispatchFrame
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatalf("ожидали событие %q, получили ошибку: %v", want, err)
		}
		if frame.T == want {
			return frame
		}
	}

	t.Fatalf("не дождались события %q за 20 кадров", want)
	return dispatchFrame{}
}
