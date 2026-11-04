package gateway_test

// Сквозной тест RESUME: обрыв, сообщение в офлайне, переподключение и догон.
// Требует живой PostgreSQL.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"

	"net/http/httptest"

	"github.com/Nemanc123/discord_clone/internal/auth"
	"github.com/Nemanc123/discord_clone/internal/chat"
	"github.com/Nemanc123/discord_clone/internal/gateway"
	"github.com/Nemanc123/discord_clone/internal/guild"
	"github.com/Nemanc123/discord_clone/internal/store"
)

type readyPayload struct {
	Op int    `json:"op"`
	T  string `json:"t"`
	S  int64  `json:"s"`
	D  struct {
		SessionID string `json:"session_id"`
	} `json:"d"`
}

type anyFrame struct {
	Op int    `json:"op"`
	T  string `json:"t"`
	S  int64  `json:"s"`
	D  struct {
		Content string `json:"content"`
	} `json:"d"`
}

type resumeEnv struct {
	srv       *httptest.Server
	token     string
	userID    string
	channelID string
	chat      *chat.Service
	ctx       context.Context
}

func setupResume(t *testing.T) resumeEnv {
	t.Helper()

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL не задан — интеграционные тесты пропущены")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	t.Cleanup(pool.Close)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("транзакция: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

	st := store.New(tx)
	gs := guild.NewService(st)
	hub := gateway.NewHub()
	chatSvc := chat.NewService(st, gs, hub)
	authSvc := auth.NewService(st, testSecret, 15*time.Minute, time.Hour)

	user := makeUser(t, ctx, st)
	detail, err := gs.CreateGuild(ctx, user, "Resume")
	if err != nil {
		t.Fatalf("сервер: %v", err)
	}
	var channelID string
	for _, ch := range detail.Channels {
		if ch.Type == "text" {
			channelID = ch.ID
		}
	}

	token, _, err := auth.NewTokenIssuer(testSecret, time.Hour).Issue(user, "sess")
	if err != nil {
		t.Fatalf("токен: %v", err)
	}

	srv := httptest.NewServer(gateway.NewHandler(hub, authSvc, gs))
	t.Cleanup(srv.Close)

	return resumeEnv{srv: srv, token: token, userID: user, channelID: channelID, chat: chatSvc, ctx: ctx}
}

func dial(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	return conn
}

func readHello(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var hello helloFrame
	if err := conn.ReadJSON(&hello); err != nil {
		t.Fatalf("HELLO: %v", err)
	}
	if hello.Op != gateway.OpHello {
		t.Fatalf("ожидали HELLO, получили op %d", hello.Op)
	}
}

func identify(t *testing.T, conn *websocket.Conn, token string) readyPayload {
	t.Helper()
	if err := conn.WriteJSON(map[string]any{
		"op": gateway.OpIdentify,
		"d":  map[string]string{"token": token},
	}); err != nil {
		t.Fatalf("IDENTIFY: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ready readyPayload
	if err := conn.ReadJSON(&ready); err != nil {
		t.Fatalf("READY: %v", err)
	}
	if ready.T != "READY" || ready.D.SessionID == "" {
		t.Fatalf("некорректный READY: %+v", ready)
	}
	return ready
}

func TestResumeReplaysMessageReceivedWhileOffline(t *testing.T) {
	e := setupResume(t)

	conn := dial(t, e.srv)
	readHello(t, conn)
	ready := identify(t, conn, e.token)

	// Обрыв: сессия остаётся живой, соединение снимается.
	_ = conn.Close()

	// Даём серверу обработать закрытие.
	time.Sleep(150 * time.Millisecond)

	// Сообщение отправлено, пока клиент был офлайн. Оно должно попасть
	// в буфер сессии, а не потеряться.
	if _, err := e.chat.Post(e.ctx, e.userID, e.channelID, "пока тебя не было", nil); err != nil {
		t.Fatalf("отправка: %v", err)
	}

	// Переподключение и догон с seq после READY.
	conn2 := dial(t, e.srv)
	defer func() { _ = conn2.Close() }()
	readHello(t, conn2)

	if err := conn2.WriteJSON(map[string]any{
		"op": gateway.OpResume,
		"d": map[string]any{
			"token":      e.token,
			"session_id": ready.D.SessionID,
			"seq":        ready.S,
		},
	}); err != nil {
		t.Fatalf("RESUME: %v", err)
	}

	// Первым должен прийти пропущенный MESSAGE_CREATE.
	_ = conn2.SetReadDeadline(time.Now().Add(5 * time.Second))
	var replayed anyFrame
	if err := conn2.ReadJSON(&replayed); err != nil {
		t.Fatalf("догон: %v", err)
	}
	if replayed.T != "MESSAGE_CREATE" {
		t.Fatalf("ожидали MESSAGE_CREATE, получили %q", replayed.T)
	}
	if replayed.D.Content != "пока тебя не было" {
		t.Fatalf("контент = %q", replayed.D.Content)
	}
	if replayed.S <= ready.S {
		t.Fatalf("номер события %d должен быть больше %d", replayed.S, ready.S)
	}

	// Затем — подтверждение восстановления.
	_ = conn2.SetReadDeadline(time.Now().Add(5 * time.Second))
	var resumed anyFrame
	if err := conn2.ReadJSON(&resumed); err != nil {
		t.Fatalf("RESUMED: %v", err)
	}
	if resumed.T != "RESUMED" {
		t.Fatalf("ожидали RESUMED, получили %q", resumed.T)
	}
}

func TestResumeRejectsUnknownSession(t *testing.T) {
	e := setupResume(t)

	conn := dial(t, e.srv)
	defer func() { _ = conn.Close() }()
	readHello(t, conn)

	if err := conn.WriteJSON(map[string]any{
		"op": gateway.OpResume,
		"d": map[string]any{
			"token":      e.token,
			"session_id": "несуществующая-сессия",
			"seq":        1,
		},
	}); err != nil {
		t.Fatalf("RESUME: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var frame anyFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("ожидали INVALID_SESSION: %v", err)
	}
	if frame.Op != gateway.OpInvalidSession {
		t.Fatalf("ожидали op 9, получили op %d", frame.Op)
	}
}
