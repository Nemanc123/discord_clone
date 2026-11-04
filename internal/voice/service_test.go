package voice_test

// Интеграционные тесты голоса. Требуют живой PostgreSQL:
// без DATABASE_URL пропускаются.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Nemanc123/discord_clone/internal/guild"
	"github.com/Nemanc123/discord_clone/internal/perms"
	"github.com/Nemanc123/discord_clone/internal/store"
	"github.com/Nemanc123/discord_clone/internal/voice"
)

type recordingPublisher struct {
	mu     sync.Mutex
	guild  []string
	events []voice.Event
}

func (r *recordingPublisher) PublishVoice(_ context.Context, guildID string, event voice.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.guild = append(r.guild, guildID)
	r.events = append(r.events, event)
}

func (r *recordingPublisher) last() voice.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) == 0 {
		return voice.Event{}
	}
	return r.events[len(r.events)-1]
}

type voiceEnv struct {
	service  *voice.Service
	guilds   *guild.Service
	store    *store.Store
	pub      *recordingPublisher
	owner    string
	member   string
	stranger string
	guildID  string
	voiceID  string
	textID   string
}

func newVoiceEnv(t *testing.T) voiceEnv {
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
	pub := &recordingPublisher{}
	svc := voice.NewService(st, gs, voice.TokenIssuer{
		APIKey:    "devkey",
		APISecret: "secret",
		TTL:       5 * time.Minute,
	}, "ws://localhost:7880", pub)

	owner := makeVoiceUser(t, ctx, st)
	member := makeVoiceUser(t, ctx, st)
	stranger := makeVoiceUser(t, ctx, st)

	detail, err := gs.CreateGuild(ctx, owner, "Голосовой сервер")
	if err != nil {
		t.Fatalf("сервер: %v", err)
	}
	if err := st.AddMember(ctx, detail.Guild.ID, member); err != nil {
		t.Fatalf("участник: %v", err)
	}

	var voiceID, textID string
	for _, ch := range detail.Channels {
		switch ch.Type {
		case "voice":
			voiceID = ch.ID
		case "text":
			textID = ch.ID
		}
	}
	if voiceID == "" || textID == "" {
		t.Fatal("каналы по умолчанию не найдены")
	}

	return voiceEnv{
		service:  svc,
		guilds:   gs,
		store:    st,
		pub:      pub,
		owner:    owner,
		member:   member,
		stranger: stranger,
		guildID:  detail.Guild.ID,
		voiceID:  voiceID,
		textID:   textID,
	}
}

func makeVoiceUser(t *testing.T, ctx context.Context, st *store.Store) string {
	t.Helper()

	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("случайное имя: %v", err)
	}
	u, err := st.CreateUser(ctx, store.CreateUserParams{
		Username:     "v" + hex.EncodeToString(buf),
		PasswordHash: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",
	})
	if err != nil {
		t.Fatalf("пользователь: %v", err)
	}
	return u.ID
}

func TestJoinVoiceChannel(t *testing.T) {
	e := newVoiceEnv(t)
	ctx := context.Background()

	result, err := e.service.Join(ctx, e.owner, e.voiceID)
	if err != nil {
		t.Fatalf("вход: %v", err)
	}
	if result.Room != e.voiceID {
		t.Fatalf("room = %q, ожидали %q", result.Room, e.voiceID)
	}
	if result.URL != "ws://localhost:7880" {
		t.Fatalf("url = %q", result.URL)
	}
	if !result.CanPublish {
		t.Fatal("владелец должен иметь право говорить")
	}
	if result.Token == "" {
		t.Fatal("пустой токен")
	}

	// Токен подписан секретом LiveKit — проверяем, что он валиден.
	claims := parseToken(t, result.Token, "secret")
	if claims["sub"] != e.owner {
		t.Fatalf("sub = %v", claims["sub"])
	}
}

func TestJoinVoiceRequiresVoiceChannel(t *testing.T) {
	e := newVoiceEnv(t)

	_, err := e.service.Join(context.Background(), e.owner, e.textID)
	if !errors.Is(err, voice.ErrValidation) {
		t.Fatalf("ожидали ErrValidation, получили %v", err)
	}
}

func TestJoinVoiceRequiresMembership(t *testing.T) {
	e := newVoiceEnv(t)

	_, err := e.service.Join(context.Background(), e.stranger, e.voiceID)
	if !errors.Is(err, voice.ErrForbidden) {
		t.Fatalf("ожидали ErrForbidden, получили %v", err)
	}
}

func TestJoinVoiceRequiresConnect(t *testing.T) {
	e := newVoiceEnv(t)
	ctx := context.Background()

	// Забираем CONNECT у @everyone через overwrite канала.
	detail, err := e.guilds.DetailFor(ctx, e.owner, e.guildID)
	if err != nil {
		t.Fatalf("детали сервера: %v", err)
	}

	var everyoneID string
	for _, role := range detail.Roles {
		if role.IsDefault {
			everyoneID = role.ID
			break
		}
	}
	if everyoneID == "" {
		t.Fatal("роль @everyone не найдена")
	}

	if err := e.store.UpsertOverwrite(ctx, e.voiceID, "role", everyoneID, 0, int64(perms.Connect)); err != nil {
		t.Fatalf("overwrite: %v", err)
	}

	_, err = e.service.Join(ctx, e.member, e.voiceID)
	if !errors.Is(err, voice.ErrForbidden) {
		t.Fatalf("ожидали ErrForbidden, получили %v", err)
	}
}

func TestWebhookTracksParticipants(t *testing.T) {
	e := newVoiceEnv(t)
	ctx := context.Background()

	// Вход через вебхук: именно он, а не выдача токена, создаёт сессию.
	joinBody := webhookJSON(t, "participant_joined", e.voiceID, e.owner)
	if err := e.service.HandleWebhook(ctx, signWebhook(t, "devkey", "secret", joinBody), joinBody); err != nil {
		t.Fatalf("вебхук входа: %v", err)
	}

	participants, err := e.service.Participants(ctx, e.member, e.voiceID)
	if err != nil {
		t.Fatalf("участники: %v", err)
	}
	if len(participants) != 1 || participants[0].UserID != e.owner {
		t.Fatalf("участники = %+v", participants)
	}

	if event := e.pub.last(); event.Type != "VOICE_STATE_UPDATE" {
		t.Fatalf("событие = %q", event.Type)
	}

	leaveBody := webhookJSON(t, "participant_left", e.voiceID, e.owner)
	if err := e.service.HandleWebhook(ctx, signWebhook(t, "devkey", "secret", leaveBody), leaveBody); err != nil {
		t.Fatalf("вебхук выхода: %v", err)
	}

	participants, err = e.service.Participants(ctx, e.member, e.voiceID)
	if err != nil {
		t.Fatalf("участники: %v", err)
	}
	if len(participants) != 0 {
		t.Fatalf("после выхода участники = %+v", participants)
	}
}

func TestWebhookMovesBetweenChannels(t *testing.T) {
	e := newVoiceEnv(t)
	ctx := context.Background()

	second, err := e.guilds.CreateChannel(ctx, e.owner, e.guildID, guild.CreateChannelInput{
		Name: "Второй голосовой",
		Type: "voice",
	})
	if err != nil {
		t.Fatalf("второй канал: %v", err)
	}

	first := webhookJSON(t, "participant_joined", e.voiceID, e.owner)
	if err := e.service.HandleWebhook(ctx, signWebhook(t, "devkey", "secret", first), first); err != nil {
		t.Fatalf("вход в первый: %v", err)
	}

	move := webhookJSON(t, "participant_joined", second.ID, e.owner)
	if err := e.service.HandleWebhook(ctx, signWebhook(t, "devkey", "secret", move), move); err != nil {
		t.Fatalf("переход: %v", err)
	}

	oldRoom, err := e.store.VoiceParticipants(ctx, e.voiceID)
	if err != nil {
		t.Fatalf("старый канал: %v", err)
	}
	if len(oldRoom) != 0 {
		t.Fatalf("в старом канале остались: %+v", oldRoom)
	}

	newRoom, err := e.store.VoiceParticipants(ctx, second.ID)
	if err != nil {
		t.Fatalf("новый канал: %v", err)
	}
	if len(newRoom) != 1 {
		t.Fatalf("в новом канале %d участников", len(newRoom))
	}
}

func TestWebhookRoomFinishedClearsSessions(t *testing.T) {
	e := newVoiceEnv(t)
	ctx := context.Background()

	join := webhookJSON(t, "participant_joined", e.voiceID, e.owner)
	if err := e.service.HandleWebhook(ctx, signWebhook(t, "devkey", "secret", join), join); err != nil {
		t.Fatalf("вход: %v", err)
	}

	finished := webhookJSON(t, "room_finished", e.voiceID, "")
	if err := e.service.HandleWebhook(ctx, signWebhook(t, "devkey", "secret", finished), finished); err != nil {
		t.Fatalf("закрытие комнаты: %v", err)
	}

	participants, err := e.store.VoiceParticipants(ctx, e.voiceID)
	if err != nil {
		t.Fatalf("участники: %v", err)
	}
	if len(participants) != 0 {
		t.Fatalf("комната закрыта, но остались: %+v", participants)
	}
}

func webhookJSON(t *testing.T, event, channelID, userID string) []byte {
	t.Helper()

	payload := map[string]any{
		"event": event,
		"room": map[string]any{
			"name": channelID,
			"sid":  "RM_test",
		},
	}
	if userID != "" {
		payload["participant"] = map[string]any{
			"identity": userID,
			"name":     "test",
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("тело вебхука: %v", err)
	}
	return body
}
