package api_test

// Сквозной тест HTTP-слоя: реальные обработчики, реальная база.
// Запускается там, где доступен PostgreSQL (локально в WSL или в CI).

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Nemanc123/discord_clone/internal/api"
	"github.com/Nemanc123/discord_clone/internal/auth"
	"github.com/Nemanc123/discord_clone/internal/chat"
	"github.com/Nemanc123/discord_clone/internal/gateway"
	"github.com/Nemanc123/discord_clone/internal/guild"
	"github.com/Nemanc123/discord_clone/internal/store"
)

type client struct {
	t     *testing.T
	base  string
	token string
}

func (c *client) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()

	var reader *bytes.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("кодирование тела: %v", err)
		}
		reader = bytes.NewReader(payload)
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatalf("запрос: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp.StatusCode, decoded
}

func (c *client) mustDo(method, path string, body any, want int) map[string]any {
	c.t.Helper()
	code, decoded := c.do(method, path, body)
	if code != want {
		c.t.Fatalf("%s %s: код %d, ожидали %d (ответ: %v)", method, path, code, want, decoded)
	}
	return decoded
}

func TestHTTPFlow(t *testing.T) {
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
	authSvc := auth.NewService(st, "e2e-secret", 15*time.Minute, time.Hour)
	gs := guild.NewService(st)
	hub := gateway.NewHub()
	hub.SetFilter(gateway.NewCachedFilter(gs, 10*time.Second))
	chatSvc := chat.NewService(st, gs, hub)

	router := api.NewRouter(api.Deps{
		Pool:     pool,
		Auth:     api.NewAuthHandlers(authSvc),
		Guild:    api.NewGuildHandlers(gs),
		Messages: api.NewMessageHandlers(chatSvc),
		Presence: api.NewPresenceHandlers(hub, gs),
	})

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	suffix := time.Now().UnixNano()
	alice := &client{t: t, base: srv.URL}
	bob := &client{t: t, base: srv.URL}

	// 1. Регистрация двух пользователей.
	alice.mustDo("POST", "/api/v1/auth/register", map[string]any{
		"username": randomName(suffix, "a"),
		"password": "very-long-password",
	}, http.StatusCreated)
	bob.mustDo("POST", "/api/v1/auth/register", map[string]any{
		"username": randomName(suffix, "b"),
		"password": "very-long-password",
	}, http.StatusCreated)

	// Токены получаем входом: так проверяется и /login.
	aliceLogin := alice.mustDo("POST", "/api/v1/auth/login", map[string]any{
		"login":    randomName(suffix, "a"),
		"password": "very-long-password",
	}, http.StatusOK)
	alice.token = aliceLogin["access_token"].(string)

	bobLogin := bob.mustDo("POST", "/api/v1/auth/login", map[string]any{
		"login":    randomName(suffix, "b"),
		"password": "very-long-password",
	}, http.StatusOK)
	bob.token = bobLogin["access_token"].(string)

	// 2. Сервер и канал.
	created := alice.mustDo("POST", "/api/v1/guilds", map[string]any{
		"name": "E2E сервер",
	}, http.StatusCreated)

	guildObj := created["guild"].(map[string]any)
	guildID := guildObj["id"].(string)

	var channelID string
	for _, raw := range created["channels"].([]any) {
		ch := raw.(map[string]any)
		if ch["type"] == "text" {
			channelID = ch["id"].(string)
			break
		}
	}
	if channelID == "" {
		t.Fatal("текстовый канал не найден")
	}

	// 3. Два сообщения.
	first := alice.mustDo("POST", "/api/v1/channels/"+channelID+"/messages",
		map[string]any{"content": "первое"}, http.StatusCreated)
	second := alice.mustDo("POST", "/api/v1/channels/"+channelID+"/messages",
		map[string]any{"content": "второе"}, http.StatusCreated)

	if first["seq"].(float64) >= second["seq"].(float64) {
		t.Fatal("seq должен возрастать")
	}

	// 4. Лента.
	history := alice.mustDo("GET", "/api/v1/channels/"+channelID+"/messages", nil, http.StatusOK)
	if got := len(history["messages"].([]any)); got != 2 {
		t.Fatalf("в ленте %d сообщений, ожидали 2", got)
	}

	// 5. Приглашение и вступление.
	invite := alice.mustDo("POST", "/api/v1/guilds/"+guildID+"/invites",
		map[string]any{"channel_id": channelID}, http.StatusCreated)
	bob.mustDo("POST", "/api/v1/invites/"+invite["code"].(string), map[string]any{}, http.StatusOK)

	// 6. Непрочитанные у bob.
	unread := bob.mustDo("GET", "/api/v1/guilds/"+guildID+"/unread", nil, http.StatusOK)
	if got := unreadForChannel(t, unread, channelID); got != 2 {
		t.Fatalf("непрочитано %d, ожидали 2", got)
	}

	// 7. Отметка о прочтении.
	bob.mustDo("POST", "/api/v1/channels/"+channelID+"/read",
		map[string]any{"seq": second["seq"]}, http.StatusNoContent)

	unread = bob.mustDo("GET", "/api/v1/guilds/"+guildID+"/unread", nil, http.StatusOK)
	if got := unreadForChannel(t, unread, channelID); got != 0 {
		t.Fatalf("после прочтения непрочитано %d", got)
	}

	// 8. Правка и удаление.
	edited := alice.mustDo("PATCH", "/api/v1/messages/"+first["id"].(string),
		map[string]any{"content": "первое (исправлено)"}, http.StatusOK)
	if edited["edited_at"] == nil {
		t.Fatal("edited_at не заполнен")
	}

	alice.mustDo("DELETE", "/api/v1/messages/"+second["id"].(string), nil, http.StatusNoContent)

	history = alice.mustDo("GET", "/api/v1/channels/"+channelID+"/messages", nil, http.StatusOK)
	if got := len(history["messages"].([]any)); got != 1 {
		t.Fatalf("после удаления в ленте %d сообщений, ожидали 1", got)
	}

	// 9. Presence без подключённых сокетов.
	presence := alice.mustDo("GET", "/api/v1/guilds/"+guildID+"/presence", nil, http.StatusOK)
	if got := len(presence["online"].([]any)); got != 0 {
		t.Fatalf("онлайн = %d, ожидали 0", got)
	}

	// 10. Посторонний не видит непрочитанные чужого сервера.
	stranger := &client{t: t, base: srv.URL}
	reg := stranger.mustDo("POST", "/api/v1/auth/register", map[string]any{
		"username": randomName(suffix, "c"),
		"password": "very-long-password",
	}, http.StatusCreated)
	stranger.token = reg["access_token"].(string)

	stranger.mustDo("GET", "/api/v1/guilds/"+guildID+"/unread", nil, http.StatusForbidden)
}

func randomName(seed int64, prefix string) string {
	// Допустимые символы: a-z, 0-9, _.
	return "e2e" + prefix + time.Unix(0, seed).Format("150405") + "x"
}

func unreadForChannel(t *testing.T, payload map[string]any, channelID string) int64 {
	t.Helper()

	items, ok := payload["channels"].([]any)
	if !ok {
		t.Fatalf("нет поля channels: %v", payload)
	}
	for _, raw := range items {
		item := raw.(map[string]any)
		if item["channel_id"] == channelID {
			return int64(item["unread"].(float64))
		}
	}
	t.Fatalf("канал %s не найден в ответе", channelID)
	return 0
}
