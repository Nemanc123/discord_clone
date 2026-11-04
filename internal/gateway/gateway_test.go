package gateway_test

// Сквозной тест шлюза: подключение, READY, heartbeat и доставка события
// о новом сообщении. Требует живой PostgreSQL.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Nemanc123/discord_clone/internal/auth"
	"github.com/Nemanc123/discord_clone/internal/chat"
	"github.com/Nemanc123/discord_clone/internal/gateway"
	"github.com/Nemanc123/discord_clone/internal/guild"
	"github.com/Nemanc123/discord_clone/internal/store"

	"net/http/httptest"
)

const testSecret = "gateway-test-secret"

type helloFrame struct {
	Op int `json:"op"`
	D  struct {
		HeartbeatInterval int64 `json:"heartbeat_interval"`
	} `json:"d"`
}

type dispatchFrame struct {
	Op int    `json:"op"`
	T  string `json:"t"`
	S  int64  `json:"s"`
	D  struct {
		Content   string `json:"content"`
		ChannelID string `json:"channel_id"`
	} `json:"d"`
}

func TestReadyHeartbeatAndFanout(t *testing.T) {
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
	detail, err := gs.CreateGuild(ctx, user, "Шлюз")
	if err != nil {
		t.Fatalf("сервер: %v", err)
	}
	var channelID string
	for _, ch := range detail.Channels {
		if ch.Type == "text" {
			channelID = ch.ID
		}
	}

	token, _, err := auth.NewTokenIssuer(testSecret, time.Hour).Issue(user, "test-session")
	if err != nil {
		t.Fatalf("токен: %v", err)
	}

	srv := httptest.NewServer(gateway.NewHandler(hub, authSvc, gs))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("подключение по ws: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// 1. HELLO
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var hello helloFrame
	if err := conn.ReadJSON(&hello); err != nil {
		t.Fatalf("чтение HELLO: %v", err)
	}
	if hello.Op != gateway.OpHello {
		t.Fatalf("ожидали HELLO (op 10), получили op %d", hello.Op)
	}
	if hello.D.HeartbeatInterval <= 0 {
		t.Fatal("интервал heartbeat не передан")
	}

	// 2. IDENTIFY
	if err := conn.WriteJSON(map[string]any{
		"op": gateway.OpIdentify,
		"d":  map[string]string{"token": token},
	}); err != nil {
		t.Fatalf("отправка IDENTIFY: %v", err)
	}

	// 3. READY
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ready dispatchFrame
	if err := conn.ReadJSON(&ready); err != nil {
		t.Fatalf("чтение READY: %v", err)
	}
	if ready.T != "READY" {
		t.Fatalf("ожидали READY, получили %q", ready.T)
	}

	// 4. Heartbeat → ACK.
	// Между READY и ACK может прийти PRESENCE_UPDATE (рассылка статуса),
	// поэтому читаем до нужного op, а не ровно один кадр.
	if err := conn.WriteJSON(map[string]any{"op": gateway.OpHeartbeat}); err != nil {
		t.Fatalf("отправка heartbeat: %v", err)
	}
	readUntilOp(t, conn, gateway.OpHeartbeatAck)

	// 5. Новое сообщение должно прийти в сокет.
	// Перед ним может прийти PRESENCE_UPDATE, поэтому ищем событие по имени.
	if _, err := chatSvc.Post(ctx, user, channelID, "привет из теста", nil); err != nil {
		t.Fatalf("отправка сообщения: %v", err)
	}

	event := readUntilEvent(t, conn, "MESSAGE_CREATE")
	if event.D.Content != "привет из теста" {
		t.Fatalf("контент = %q", event.D.Content)
	}
	if event.D.ChannelID != channelID {
		t.Fatalf("канал = %q, ожидали %q", event.D.ChannelID, channelID)
	}
	if event.S < 2 {
		t.Fatalf("последовательность события = %d, должна быть больше READY", event.S)
	}
}

func TestIdentifyRejectsBadToken(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL не задан")
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
	authSvc := auth.NewService(st, testSecret, 15*time.Minute, time.Hour)
	srv := httptest.NewServer(gateway.NewHandler(gateway.NewHub(), authSvc, gs))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	defer func() { _ = conn.Close() }()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var hello helloFrame
	if err := conn.ReadJSON(&hello); err != nil {
		t.Fatalf("чтение HELLO: %v", err)
	}

	if err := conn.WriteJSON(map[string]any{
		"op": gateway.OpIdentify,
		"d":  map[string]string{"token": "неверный.токен.значение"},
	}); err != nil {
		t.Fatalf("отправка: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatal("соединение должно быть закрыто при неверном токене")
	}
}

func makeUser(t *testing.T, ctx context.Context, st *store.Store) string {
	t.Helper()
	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("случайное имя: %v", err)
	}
	u, err := st.CreateUser(ctx, store.CreateUserParams{
		Username:     "u" + hex.EncodeToString(buf),
		PasswordHash: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",
	})
	if err != nil {
		t.Fatalf("пользователь: %v", err)
	}
	return u.ID
}
