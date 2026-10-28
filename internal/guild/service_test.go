package guild_test

// Интеграционные тесты серверов и прав. Требуют живой PostgreSQL:
// без DATABASE_URL пропускаются, чтобы `go test ./...` работал без базы.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Nemanc123/discord_clone/internal/guild"
	"github.com/Nemanc123/discord_clone/internal/perms"
	"github.com/Nemanc123/discord_clone/internal/store"
)

type env struct {
	service *guild.Service
	store   *store.Store
	owner   string
	member  string
}

func newEnv(t *testing.T) env {
	t.Helper()

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL не задан — интеграционные тесты пропущены")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("подключение к базе: %v", err)
	}
	t.Cleanup(pool.Close)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("транзакция: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

	st := store.New(tx)

	return env{
		service: guild.NewService(st),
		store:   st,
		owner:   makeUser(t, ctx, st),
		member:  makeUser(t, ctx, st),
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
		t.Fatalf("создание пользователя: %v", err)
	}

	return u.ID
}

func TestCreateGuildCreatesDefaults(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	detail, err := e.service.CreateGuild(ctx, e.owner, "Тестовый сервер")
	if err != nil {
		t.Fatalf("создание сервера: %v", err)
	}

	if detail.Guild.OwnerID != e.owner {
		t.Fatal("владелец не совпадает")
	}

	var hasDefault bool
	for _, r := range detail.Roles {
		if r.IsDefault {
			hasDefault = true
			if perms.Permission(r.Permissions).Has(perms.Administrator) {
				t.Fatal("@everyone не должен быть администратором")
			}
		}
	}
	if !hasDefault {
		t.Fatal("роль @everyone не создана")
	}

	if len(detail.Channels) != 3 {
		t.Fatalf("ожидали 3 канала (категория, текст, голос), получили %d", len(detail.Channels))
	}

	member, err := e.store.MemberExists(ctx, detail.Guild.ID, e.owner)
	if err != nil {
		t.Fatalf("проверка участника: %v", err)
	}
	if !member {
		t.Fatal("владелец не добавлен в участники")
	}
}

func TestOwnerHasAdministrator(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	detail, err := e.service.CreateGuild(ctx, e.owner, "Права владельца")
	if err != nil {
		t.Fatalf("создание: %v", err)
	}

	p, err := e.service.Permissions(ctx, detail.Guild.ID, e.owner, "")
	if err != nil {
		t.Fatalf("права: %v", err)
	}
	if !p.Has(perms.Administrator) {
		t.Fatal("владелец должен быть администратором")
	}
	if !p.Has(perms.ManageGuild) {
		t.Fatal("администратор должен иметь ManageGuild")
	}
}

func TestStrangerHasNoAccess(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	detail, err := e.service.CreateGuild(ctx, e.owner, "Закрытый сервер")
	if err != nil {
		t.Fatalf("создание: %v", err)
	}

	if _, err := e.service.DetailFor(ctx, e.member, detail.Guild.ID); !errors.Is(err, guild.ErrForbidden) {
		t.Fatalf("посторонний не должен видеть сервер, получили %v", err)
	}
}

func TestMemberGetsEveryonePermissions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	detail, err := e.service.CreateGuild(ctx, e.owner, "Сервер с участником")
	if err != nil {
		t.Fatalf("создание: %v", err)
	}
	if err := e.store.AddMember(ctx, detail.Guild.ID, e.member); err != nil {
		t.Fatalf("добавление участника: %v", err)
	}

	p, err := e.service.Permissions(ctx, detail.Guild.ID, e.member, "")
	if err != nil {
		t.Fatalf("права: %v", err)
	}
	if !p.Has(perms.ViewChannel) || !p.Has(perms.SendMessages) {
		t.Fatal("участник должен иметь базовые права")
	}
	if p.Has(perms.Administrator) || p.Has(perms.ManageRoles) {
		t.Fatal("участник не должен иметь управляющих прав")
	}
}

func TestOverwriteDeniesMessages(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	detail, err := e.service.CreateGuild(ctx, e.owner, "Канал только для чтения")
	if err != nil {
		t.Fatalf("создание: %v", err)
	}
	if err := e.store.AddMember(ctx, detail.Guild.ID, e.member); err != nil {
		t.Fatalf("добавление: %v", err)
	}

	var textChannel, everyoneRole string
	for _, ch := range detail.Channels {
		if ch.Type == "text" {
			textChannel = ch.ID
		}
	}
	for _, r := range detail.Roles {
		if r.IsDefault {
			everyoneRole = r.ID
		}
	}

	if err := e.store.UpsertOverwrite(ctx, textChannel, "role", everyoneRole,
		0, int64(perms.SendMessages)); err != nil {
		t.Fatalf("переопределение: %v", err)
	}

	p, err := e.service.Permissions(ctx, detail.Guild.ID, e.member, textChannel)
	if err != nil {
		t.Fatalf("права: %v", err)
	}
	if p.Has(perms.SendMessages) {
		t.Fatal("запрет писать не сработал")
	}
	if !p.Has(perms.ViewChannel) {
		t.Fatal("запрет писать не должен снимать право читать")
	}
}

func TestCreateChannelRequiresPermission(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	detail, err := e.service.CreateGuild(ctx, e.owner, "Каналы")
	if err != nil {
		t.Fatalf("создание: %v", err)
	}
	if err := e.store.AddMember(ctx, detail.Guild.ID, e.member); err != nil {
		t.Fatalf("добавление: %v", err)
	}

	_, err = e.service.CreateChannel(ctx, e.member, detail.Guild.ID, guild.CreateChannelInput{
		Name: "новый", Type: "text",
	})
	if !errors.Is(err, guild.ErrForbidden) {
		t.Fatalf("ожидали запрет, получили %v", err)
	}

	ch, err := e.service.CreateChannel(ctx, e.owner, detail.Guild.ID, guild.CreateChannelInput{
		Name: "Новый Канал", Type: "text",
	})
	if err != nil {
		t.Fatalf("владелец должен создавать каналы: %v", err)
	}
	if ch.Name == nil || *ch.Name != "новый-канал" {
		t.Fatalf("имя не нормализовано: %v", ch.Name)
	}
}

func TestCannotGrantPermissionsYouLack(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	detail, err := e.service.CreateGuild(ctx, e.owner, "Эскалация")
	if err != nil {
		t.Fatalf("создание: %v", err)
	}
	if err := e.store.AddMember(ctx, detail.Guild.ID, e.member); err != nil {
		t.Fatalf("добавление: %v", err)
	}

	// Даём участнику право управлять ролями — но не администратора.
	moderator, err := e.service.CreateRole(ctx, e.owner, detail.Guild.ID, guild.CreateRoleInput{
		Name:        "Модератор",
		Permissions: int64(perms.ManageRoles | perms.ViewChannel | perms.SendMessages),
	})
	if err != nil {
		t.Fatalf("создание роли: %v", err)
	}
	if err := e.service.AssignRole(ctx, e.owner, detail.Guild.ID, e.member, moderator.ID); err != nil {
		t.Fatalf("выдача роли: %v", err)
	}

	// Теперь он может создавать роли, но не может выдать больше, чем имеет.
	_, err = e.service.CreateRole(ctx, e.member, detail.Guild.ID, guild.CreateRoleInput{
		Name:        "Админ",
		Permissions: int64(perms.Administrator),
	})
	if !errors.Is(err, guild.ErrForbidden) {
		t.Fatalf("ожидали запрет на выдачу Administrator, получили %v", err)
	}
}

func TestCannotAssignRoleAboveOwn(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	detail, err := e.service.CreateGuild(ctx, e.owner, "Иерархия")
	if err != nil {
		t.Fatalf("создание: %v", err)
	}
	if err := e.store.AddMember(ctx, detail.Guild.ID, e.member); err != nil {
		t.Fatalf("добавление: %v", err)
	}

	moderator, err := e.service.CreateRole(ctx, e.owner, detail.Guild.ID, guild.CreateRoleInput{
		Name:        "Модератор",
		Permissions: int64(perms.ManageRoles),
	})
	if err != nil {
		t.Fatalf("создание роли: %v", err)
	}
	if err := e.service.AssignRole(ctx, e.owner, detail.Guild.ID, e.member, moderator.ID); err != nil {
		t.Fatalf("выдача роли: %v", err)
	}

	// Роль владельца выше: модератор не должен её выдать.
	var adminRoleID string
	for _, r := range detail.Roles {
		if !r.IsDefault {
			adminRoleID = r.ID
		}
	}
	if adminRoleID == "" {
		t.Skip("нет роли выше модератора")
	}

	// Создаём заведомо более высокую роль от имени владельца.
	high, err := e.service.CreateRole(ctx, e.owner, detail.Guild.ID, guild.CreateRoleInput{
		Name:        "Старший",
		Permissions: int64(perms.ManageRoles),
	})
	if err != nil {
		t.Fatalf("создание высокой роли: %v", err)
	}

	err = e.service.AssignRole(ctx, e.member, detail.Guild.ID, e.owner, high.ID)
	if !errors.Is(err, guild.ErrHierarchy) {
		t.Fatalf("ожидали нарушение иерархии, получили %v", err)
	}
}

func TestJoinByInvite(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	detail, err := e.service.CreateGuild(ctx, e.owner, "По приглашению")
	if err != nil {
		t.Fatalf("создание: %v", err)
	}

	var textChannel string
	for _, ch := range detail.Channels {
		if ch.Type == "text" {
			textChannel = ch.ID
		}
	}

	code, err := e.service.CreateInvite(ctx, e.owner, detail.Guild.ID, textChannel, nil)
	if err != nil {
		t.Fatalf("создание приглашения: %v", err)
	}
	if len(code) != 8 {
		t.Fatalf("код приглашения длиной %d, ожидали 8", len(code))
	}

	joinedGuildID, err := e.service.JoinByInvite(ctx, e.member, code)
	if err != nil {
		t.Fatalf("вступление: %v", err)
	}
	if joinedGuildID != detail.Guild.ID {
		t.Fatal("вступили не на тот сервер")
	}

	member, err := e.store.MemberExists(ctx, detail.Guild.ID, e.member)
	if err != nil {
		t.Fatalf("проверка: %v", err)
	}
	if !member {
		t.Fatal("после вступления участник не появился")
	}

	if _, err := e.service.JoinByInvite(ctx, e.member, "НЕСУЩЕСТВУЕТ"); !errors.Is(err, store.ErrInviteNotFound) {
		t.Fatalf("ожидали «приглашение не найдено», получили %v", err)
	}
}
