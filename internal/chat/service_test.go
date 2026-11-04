package chat_test

// Интеграционные тесты сообщений. Требуют живой PostgreSQL:
// без DATABASE_URL пропускаются.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Nemanc123/discord_clone/internal/chat"
	"github.com/Nemanc123/discord_clone/internal/guild"
	"github.com/Nemanc123/discord_clone/internal/perms"
	"github.com/Nemanc123/discord_clone/internal/store"
)

type env struct {
	service   *chat.Service
	guilds    *guild.Service
	store     *store.Store
	owner     string
	member    string
	guildID   string
	channelID string
}

const testHash = "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA"

func newEnv(t *testing.T) env {
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
	svc := chat.NewService(st, gs, nil)

	owner := makeUser(t, ctx, st)
	member := makeUser(t, ctx, st)

	detail, err := gs.CreateGuild(ctx, owner, "Тестовый сервер")
	if err != nil {
		t.Fatalf("сервер: %v", err)
	}
	if err := st.AddMember(ctx, detail.Guild.ID, member); err != nil {
		t.Fatalf("участник: %v", err)
	}

	var channelID string
	for _, ch := range detail.Channels {
		if ch.Type == "text" {
			channelID = ch.ID
		}
	}
	if channelID == "" {
		t.Fatal("текстовый канал не найден")
	}

	return env{
		service:   svc,
		guilds:    gs,
		store:     st,
		owner:     owner,
		member:    member,
		guildID:   detail.Guild.ID,
		channelID: channelID,
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
		PasswordHash: testHash,
	})
	if err != nil {
		t.Fatalf("пользователь: %v", err)
	}
	return u.ID
}

func TestPostAndHistory(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	for _, text := range []string{"первое", "второе", "третье"} {
		if _, err := e.service.Post(ctx, e.owner, e.channelID, text, nil); err != nil {
			t.Fatalf("отправка %q: %v", text, err)
		}
	}

	msgs, err := e.service.History(ctx, e.member, e.channelID, 0, 10)
	if err != nil {
		t.Fatalf("история: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("сообщений %d, ожидали 3", len(msgs))
	}
	// История в хронологическом порядке.
	if msgs[0].Content != "первое" || msgs[2].Content != "третье" {
		t.Fatalf("порядок нарушен: %q ... %q", msgs[0].Content, msgs[2].Content)
	}
	if msgs[0].Seq >= msgs[2].Seq {
		t.Fatal("seq должен возрастать")
	}
}

func TestHistoryPagination(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if _, err := e.service.Post(ctx, e.owner, e.channelID, "msg", nil); err != nil {
			t.Fatalf("отправка: %v", err)
		}
	}

	first, err := e.service.History(ctx, e.owner, e.channelID, 0, 2)
	if err != nil {
		t.Fatalf("первая страница: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("на странице %d сообщений, ожидали 2", len(first))
	}

	second, err := e.service.History(ctx, e.owner, e.channelID, first[0].Seq, 2)
	if err != nil {
		t.Fatalf("вторая страница: %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("на второй странице %d сообщений, ожидали 2", len(second))
	}
	if second[1].Seq >= first[0].Seq {
		t.Fatal("вторая страница должна быть строго старше первой")
	}
}

func TestPostRequiresPermission(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	stranger := makeUser(t, ctx, e.store)

	if _, err := e.service.Post(ctx, stranger, e.channelID, "взлом", nil); !errors.Is(err, chat.ErrForbidden) {
		t.Fatalf("ожидали запрет, получили %v", err)
	}
}

func TestPostValidatesContent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	if _, err := e.service.Post(ctx, e.owner, e.channelID, "   ", nil); !errors.Is(err, chat.ErrValidation) {
		t.Fatalf("пустое сообщение должно отклоняться, получили %v", err)
	}

	long := make([]rune, chat.MaxContentLen+1)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := e.service.Post(ctx, e.owner, e.channelID, string(long), nil); !errors.Is(err, chat.ErrValidation) {
		t.Fatalf("слишком длинное сообщение должно отклоняться, получили %v", err)
	}
}

func TestEditOwnMessage(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	msg, err := e.service.Post(ctx, e.owner, e.channelID, "черновик", nil)
	if err != nil {
		t.Fatalf("отправка: %v", err)
	}

	updated, err := e.service.Edit(ctx, e.owner, msg.ID, "исправлено")
	if err != nil {
		t.Fatalf("правка: %v", err)
	}
	if updated.Content != "исправлено" {
		t.Fatalf("контент = %q", updated.Content)
	}
	if updated.EditedAt == nil {
		t.Fatal("edited_at не заполнен")
	}
}

func TestCannotEditForeignMessageWithoutPermission(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	msg, err := e.service.Post(ctx, e.owner, e.channelID, "авторское", nil)
	if err != nil {
		t.Fatalf("отправка: %v", err)
	}

	if _, err := e.service.Edit(ctx, e.member, msg.ID, "подмена"); !errors.Is(err, chat.ErrForbidden) {
		t.Fatalf("ожидали запрет, получили %v", err)
	}
}

func TestDeleteOwnMessage(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	msg, err := e.service.Post(ctx, e.owner, e.channelID, "удалить", nil)
	if err != nil {
		t.Fatalf("отправка: %v", err)
	}

	if err := e.service.Delete(ctx, e.owner, msg.ID); err != nil {
		t.Fatalf("удаление: %v", err)
	}

	msgs, err := e.service.History(ctx, e.owner, e.channelID, 0, 10)
	if err != nil {
		t.Fatalf("история: %v", err)
	}
	for _, m := range msgs {
		if m.ID == msg.ID {
			t.Fatal("удалённое сообщение осталось в ленте")
		}
	}
}

func TestReplyMustBeInSameChannel(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	other, err := e.guilds.CreateChannel(ctx, e.owner, e.guildID, guild.CreateChannelInput{
		Name: "второй", Type: "text",
	})
	if err != nil {
		t.Fatalf("канал: %v", err)
	}

	msg, err := e.service.Post(ctx, e.owner, e.channelID, "оригинал", nil)
	if err != nil {
		t.Fatalf("отправка: %v", err)
	}

	if _, err := e.service.Post(ctx, e.owner, other.ID, "ответ не туда", &msg.ID); !errors.Is(err, chat.ErrValidation) {
		t.Fatalf("ожидали ошибку валидации, получили %v", err)
	}
}

func TestMemberWithManageMessagesCanEdit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	msg, err := e.service.Post(ctx, e.owner, e.channelID, "чужое", nil)
	if err != nil {
		t.Fatalf("отправка: %v", err)
	}

	moderator, err := e.guilds.CreateRole(ctx, e.owner, e.guildID, guild.CreateRoleInput{
		Name:        "Модератор",
		Permissions: int64(perms.ManageMessages | perms.ViewChannel),
	})
	if err != nil {
		t.Fatalf("роль: %v", err)
	}
	if err := e.guilds.AssignRole(ctx, e.owner, e.guildID, e.member, moderator.ID); err != nil {
		t.Fatalf("выдача роли: %v", err)
	}

	if _, err := e.service.Edit(ctx, e.member, msg.ID, "исправлено модератором"); err != nil {
		t.Fatalf("модератор должен править: %v", err)
	}
}

func unreadFor(t *testing.T, items []store.Unread, channelID string) int64 {
	t.Helper()
	for _, item := range items {
		if item.ChannelID == channelID {
			return item.Count
		}
	}
	t.Fatalf("канал %s не найден в счётчиках", channelID)
	return 0
}

func TestUnreadIgnoresOwnMessages(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	for _, text := range []string{"первое", "второе"} {
		if _, err := e.service.Post(ctx, e.owner, e.channelID, text, nil); err != nil {
			t.Fatalf("отправка: %v", err)
		}
	}
	if _, err := e.service.Post(ctx, e.member, e.channelID, "третье", nil); err != nil {
		t.Fatalf("отправка: %v", err)
	}

	// У владельца непрочитано только чужое сообщение.
	items, err := e.service.Unread(ctx, e.owner, e.guildID)
	if err != nil {
		t.Fatalf("непрочитанные: %v", err)
	}
	if got := unreadFor(t, items, e.channelID); got != 1 {
		t.Fatalf("непрочитано %d, ожидали 1 (свои не считаются)", got)
	}
}

func TestMarkReadResetsUnread(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	if _, err := e.service.Post(ctx, e.owner, e.channelID, "сообщение", nil); err != nil {
		t.Fatalf("отправка: %v", err)
	}

	msgs, err := e.service.History(ctx, e.member, e.channelID, 0, 10)
	if err != nil {
		t.Fatalf("история: %v", err)
	}
	last := msgs[len(msgs)-1].Seq

	if err := e.service.MarkRead(ctx, e.member, e.channelID, last); err != nil {
		t.Fatalf("отметка: %v", err)
	}

	items, err := e.service.Unread(ctx, e.member, e.guildID)
	if err != nil {
		t.Fatalf("непрочитанные: %v", err)
	}
	if got := unreadFor(t, items, e.channelID); got != 0 {
		t.Fatalf("после прочтения непрочитано %d, ожидали 0", got)
	}
}

func TestMarkReadNeverGoesBackwards(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	first, err := e.service.Post(ctx, e.owner, e.channelID, "первое", nil)
	if err != nil {
		t.Fatalf("отправка: %v", err)
	}
	second, err := e.service.Post(ctx, e.owner, e.channelID, "второе", nil)
	if err != nil {
		t.Fatalf("отправка: %v", err)
	}

	if err := e.service.MarkRead(ctx, e.member, e.channelID, second.Seq); err != nil {
		t.Fatalf("отметка: %v", err)
	}
	// Пытаемся откатить отметку назад.
	if err := e.service.MarkRead(ctx, e.member, e.channelID, first.Seq); err != nil {
		t.Fatalf("отметка: %v", err)
	}

	items, err := e.service.Unread(ctx, e.member, e.guildID)
	if err != nil {
		t.Fatalf("непрочитанные: %v", err)
	}
	if got := unreadFor(t, items, e.channelID); got != 0 {
		t.Fatalf("отметка откатилась назад: непрочитано %d", got)
	}
}

func TestUnreadRequiresMembership(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	stranger := makeUser(t, ctx, e.store)

	if _, err := e.service.Unread(ctx, stranger, e.guildID); !errors.Is(err, chat.ErrForbidden) {
		t.Fatalf("ожидали запрет, получили %v", err)
	}
}
