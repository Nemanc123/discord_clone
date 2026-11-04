package api_test

// Сквозной тест HTTP-слоя: реальные обработчики, реальная база.
// Запускается там, где доступен PostgreSQL (локально в WSL или в CI).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Nemanc123/discord_clone/internal/api"
	"github.com/Nemanc123/discord_clone/internal/auth"
	"github.com/Nemanc123/discord_clone/internal/chat"
	"github.com/Nemanc123/discord_clone/internal/gateway"
	"github.com/Nemanc123/discord_clone/internal/guild"
	"github.com/Nemanc123/discord_clone/internal/storage"
	"github.com/Nemanc123/discord_clone/internal/store"
	"github.com/Nemanc123/discord_clone/internal/voice"
)

const (
	e2eLiveKitKey    = "e2e-key"
	e2eLiveKitSecret = "e2e-livekit-secret"
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

// newTestServer собирает полный HTTP-слой на реальной базе во временной
// транзакции. Если хранилище передано — вложения включены.
func newTestServer(t *testing.T, files *storage.Storage) (*httptest.Server, *chat.Service) {
	t.Helper()

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
	if files != nil {
		chatSvc = chatSvc.WithStorage(files)
	}

	voiceSvc := voice.NewService(st, gs, voice.TokenIssuer{
		APIKey:    e2eLiveKitKey,
		APISecret: e2eLiveKitSecret,
		TTL:       5 * time.Minute,
	}, "ws://localhost:7880", nil)

	router := api.NewRouter(api.Deps{
		Pool:     pool,
		Auth:     api.NewAuthHandlers(authSvc),
		Guild:    api.NewGuildHandlers(gs),
		Messages: api.NewMessageHandlers(chatSvc),
		Presence: api.NewPresenceHandlers(hub, gs),
		Voice:    api.NewVoiceHandlers(voiceSvc),
	})

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	return srv, chatSvc
}

func TestHTTPFlow(t *testing.T) {
	srv, _ := newTestServer(t, nil)

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

// TestHTTPAttachments — полный путь файла: ссылка на загрузку → PUT в
// хранилище → сообщение со вложением → вложение в ленте.
func TestHTTPAttachments(t *testing.T) {
	endpoint := os.Getenv("STORAGE_ENDPOINT")
	if endpoint == "" {
		t.Skip("STORAGE_ENDPOINT не задан")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	files, err := storage.New(ctx, storage.Config{
		Endpoint:  endpoint,
		AccessKey: envOr("STORAGE_ACCESS_KEY", "dcstorage"),
		SecretKey: envOr("STORAGE_SECRET_KEY", "devstorage123"),
		Bucket:    envOr("STORAGE_BUCKET", "discord-clone-e2e"),
	})
	if err != nil {
		t.Skipf("хранилище недоступно: %v", err)
	}

	srv, _ := newTestServer(t, files)

	suffix := time.Now().UnixNano()
	alice := &client{t: t, base: srv.URL}

	reg := alice.mustDo("POST", "/api/v1/auth/register", map[string]any{
		"username": randomName(suffix, "f"),
		"password": "very-long-password",
	}, http.StatusCreated)
	alice.token = reg["access_token"].(string)

	created := alice.mustDo("POST", "/api/v1/guilds", map[string]any{
		"name": "E2E вложения",
	}, http.StatusCreated)

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

	// 1. Ссылка на загрузку.
	presign := alice.mustDo("POST", "/api/v1/channels/"+channelID+"/attachments/presign", map[string]any{
		"filename":     "заметка.txt",
		"content_type": "text/plain",
		"size_bytes":   len("содержимое файла"),
	}, http.StatusCreated)

	storageKey := presign["storage_key"].(string)
	uploadURL := presign["upload_url"].(string)
	if storageKey == "" || uploadURL == "" {
		t.Fatalf("пустой ответ presign: %v", presign)
	}

	// 2. Загрузка напрямую в хранилище, мимо сервера.
	body := []byte("содержимое файла")
	req, err := http.NewRequest(http.MethodPut, uploadURL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("запрос загрузки: %v", err)
	}
	req.Header.Set("Content-Type", "text/plain")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("загрузка: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("загрузка вернула %d", resp.StatusCode)
	}

	// 3. Сообщение с вложением: в ответе вложение должно быть сразу.
	posted := alice.mustDo("POST", "/api/v1/channels/"+channelID+"/messages", map[string]any{
		"content": "файл во вложении",
		"attachments": []map[string]any{{
			"storage_key":  storageKey,
			"filename":     "заметка.txt",
			"content_type": "text/plain",
			"size_bytes":   len(body),
		}},
	}, http.StatusCreated)

	attachments, ok := posted["attachments"].([]any)
	if !ok || len(attachments) != 1 {
		t.Fatalf("в ответе нет вложения: %v", posted)
	}
	if attachments[0].(map[string]any)["storage_key"] != storageKey {
		t.Fatalf("ключ вложения не совпал: %v", attachments[0])
	}

	// 4. Лента тоже отдаёт вложение.
	history := alice.mustDo("GET", "/api/v1/channels/"+channelID+"/messages", nil, http.StatusOK)
	messages := history["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("в ленте %d сообщений, ожидали 1", len(messages))
	}
	fromHistory, ok := messages[0].(map[string]any)["attachments"].([]any)
	if !ok || len(fromHistory) != 1 {
		t.Fatalf("в ленте нет вложения: %v", messages[0])
	}

	attachmentID := attachments[0].(map[string]any)["id"].(string)

	// 5. Скачивание: 302 на подписанную ссылку, файл отдаёт само хранилище.
	noRedirect := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	dlReq, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/attachments/"+attachmentID+"/download", nil)
	if err != nil {
		t.Fatalf("запрос скачивания: %v", err)
	}
	dlReq.Header.Set("Authorization", "Bearer "+alice.token)

	dlResp, err := noRedirect.Do(dlReq)
	if err != nil {
		t.Fatalf("скачивание: %v", err)
	}
	_ = dlResp.Body.Close()
	if dlResp.StatusCode != http.StatusFound {
		t.Fatalf("скачивание вернуло %d, ожидали 302", dlResp.StatusCode)
	}

	location := dlResp.Header.Get("Location")
	if location == "" {
		t.Fatal("нет Location в ответе скачивания")
	}

	fileResp, err := http.Get(location)
	if err != nil {
		t.Fatalf("получение файла по ссылке: %v", err)
	}
	defer func() { _ = fileResp.Body.Close() }()

	downloaded, err := io.ReadAll(fileResp.Body)
	if err != nil {
		t.Fatalf("чтение файла: %v", err)
	}
	if !bytes.Equal(downloaded, body) {
		t.Fatalf("содержимое не совпало: %q", downloaded)
	}

	// 6. Посторонний не получает ссылку.
	stranger := &client{t: t, base: srv.URL}
	regStranger := stranger.mustDo("POST", "/api/v1/auth/register", map[string]any{
		"username": randomName(suffix, "g"),
		"password": "very-long-password",
	}, http.StatusCreated)
	stranger.token = regStranger["access_token"].(string)
	stranger.mustDo("GET", "/api/v1/attachments/"+attachmentID+"/download", nil, http.StatusForbidden)

	// 7. Несуществующее вложение — 404.
	alice.mustDo("GET", "/api/v1/attachments/00000000-0000-0000-0000-000000000000/download", nil, http.StatusNotFound)

	// 8. Несуществующий ключ прикрепить нельзя.
	alice.mustDo("POST", "/api/v1/channels/"+channelID+"/messages", map[string]any{
		"content": "битая ссылка",
		"attachments": []map[string]any{{
			"storage_key":  "attachments/нет-такого/файл.txt",
			"filename":     "файл.txt",
			"content_type": "text/plain",
			"size_bytes":   10,
		}},
	}, http.StatusBadRequest)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// TestHTTPVoice — голос через HTTP: токен, список участников, вебхуки.
func TestHTTPVoice(t *testing.T) {
	srv, _ := newTestServer(t, nil)

	alice := &client{t: t, base: srv.URL}
	reg := alice.mustDo("POST", "/api/v1/auth/register", map[string]any{
		"username": randomName(time.Now().UnixNano(), "v"),
		"password": "very-long-password",
	}, http.StatusCreated)
	alice.token = reg["access_token"].(string)
	aliceID := reg["user"].(map[string]any)["id"].(string)

	created := alice.mustDo("POST", "/api/v1/guilds", map[string]any{
		"name": "E2E голос",
	}, http.StatusCreated)

	var voiceID string
	for _, raw := range created["channels"].([]any) {
		ch := raw.(map[string]any)
		if ch["type"] == "voice" {
			voiceID = ch["id"].(string)
			break
		}
	}
	if voiceID == "" {
		t.Fatal("голосовой канал не найден")
	}

	// 1. Токен на вход.
	joined := alice.mustDo("POST", "/api/v1/channels/"+voiceID+"/voice/join", map[string]any{}, http.StatusOK)
	if joined["room"] != voiceID {
		t.Fatalf("room = %v, ожидали %s", joined["room"], voiceID)
	}
	if joined["url"] != "ws://localhost:7880" {
		t.Fatalf("url = %v", joined["url"])
	}
	if joined["can_publish"] != true {
		t.Fatalf("can_publish = %v", joined["can_publish"])
	}

	token, _ := joined["token"].(string)
	if token == "" {
		t.Fatal("пустой токен")
	}
	parsed, err := jwt.Parse(token, func(*jwt.Token) (any, error) {
		return []byte(e2eLiveKitSecret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !parsed.Valid {
		t.Fatalf("токен не разобрался: %v", err)
	}

	// 2. Пока никто не зашёл — список пуст.
	list := alice.mustDo("GET", "/api/v1/channels/"+voiceID+"/voice", nil, http.StatusOK)
	if got := len(list["participants"].([]any)); got != 0 {
		t.Fatalf("участников %d, ожидали 0", got)
	}

	// 3. Вебхук с плохой подписью отклоняется.
	joinBody := e2eWebhookBody(t, "participant_joined", voiceID, aliceID)
	if code := e2eWebhook(t, srv.URL, "bad-token", joinBody); code != http.StatusUnauthorized {
		t.Fatalf("плохая подпись: код %d, ожидали 401", code)
	}

	// 4. Настоящий вебхук входа создаёт сессию.
	if code := e2eWebhook(t, srv.URL, e2eSignWebhook(t, joinBody), joinBody); code != http.StatusOK {
		t.Fatalf("вход: код %d, ожидали 200", code)
	}

	list = alice.mustDo("GET", "/api/v1/channels/"+voiceID+"/voice", nil, http.StatusOK)
	participants := list["participants"].([]any)
	if len(participants) != 1 {
		t.Fatalf("участников %d, ожидали 1", len(participants))
	}
	if participants[0].(map[string]any)["user_id"] != aliceID {
		t.Fatalf("не тот участник: %v", participants[0])
	}

	// 5. Посторонний не видит участников.
	stranger := &client{t: t, base: srv.URL}
	regStranger := stranger.mustDo("POST", "/api/v1/auth/register", map[string]any{
		"username": randomName(time.Now().UnixNano(), "w"),
		"password": "very-long-password",
	}, http.StatusCreated)
	stranger.token = regStranger["access_token"].(string)
	stranger.mustDo("GET", "/api/v1/channels/"+voiceID+"/voice", nil, http.StatusForbidden)

	// 6. Выход закрывает сессию.
	leaveBody := e2eWebhookBody(t, "participant_left", voiceID, aliceID)
	if code := e2eWebhook(t, srv.URL, e2eSignWebhook(t, leaveBody), leaveBody); code != http.StatusOK {
		t.Fatalf("выход: код %d, ожидали 200", code)
	}

	list = alice.mustDo("GET", "/api/v1/channels/"+voiceID+"/voice", nil, http.StatusOK)
	if got := len(list["participants"].([]any)); got != 0 {
		t.Fatalf("после выхода участников %d", got)
	}
}

func e2eSignWebhook(t *testing.T, body []byte) string {
	t.Helper()

	sum := sha256.Sum256(body)
	claims := jwt.MapClaims{
		"iss":    e2eLiveKitKey,
		"sha256": base64.StdEncoding.EncodeToString(sum[:]),
		"exp":    time.Now().Add(time.Minute).Unix(),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(e2eLiveKitSecret))
	if err != nil {
		t.Fatalf("подпись вебхука: %v", err)
	}
	return token
}

func e2eWebhookBody(t *testing.T, event, channelID, userID string) []byte {
	t.Helper()

	payload := map[string]any{
		"event": event,
		"room":  map[string]any{"name": channelID, "sid": "RM_e2e"},
	}
	if userID != "" {
		payload["participant"] = map[string]any{"identity": userID, "name": "e2e"}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("тело вебхука: %v", err)
	}
	return body
}

func e2eWebhook(t *testing.T, base, token string, body []byte) int {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, base+"/webhooks/livekit", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("запрос вебхука: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("вебхук: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}
