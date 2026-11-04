// Package guild — серверы, каналы, роли и проверка прав.
package guild

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Nemanc123/discord_clone/internal/perms"
	"github.com/Nemanc123/discord_clone/internal/store"
)

var (
	ErrForbidden  = errors.New("недостаточно прав")
	ErrValidation = errors.New("неверные данные")
	ErrHierarchy  = errors.New("нарушена иерархия ролей")
)

type Service struct {
	store *store.Store
}

func NewService(st *store.Store) *Service {
	return &Service{store: st}
}

// Detail — сервер целиком, как его видит участник.
type Detail struct {
	Guild    store.Guild
	Roles    []store.Role
	Channels []store.Channel
}

// CreateGuild создаёт сервер и сразу обставляет его обязательным минимумом:
// роль @everyone, владелец как участник, категория и два канала.
//
// Всё в одной транзакции: сервер без роли @everyone — сломанное состояние,
// из которого нет выхода через API.
func (s *Service) CreateGuild(ctx context.Context, ownerID, name string) (Detail, error) {
	name = strings.TrimSpace(name)
	if len(name) < 2 || len(name) > 100 {
		return Detail{}, fmt.Errorf("%w: имя сервера — от 2 до 100 символов", ErrValidation)
	}

	txStore, tx, err := s.store.Tx(ctx)
	if err != nil {
		return Detail{}, err
	}
	if tx != nil {
		defer func() { _ = tx.Rollback(ctx) }()
	}

	g, err := txStore.CreateGuild(ctx, name, ownerID)
	if err != nil {
		return Detail{}, err
	}

	if _, err := txStore.CreateRole(ctx, store.Role{
		GuildID:     g.ID,
		Name:        "@everyone",
		Position:    0,
		Permissions: int64(perms.DefaultEveryone),
		IsDefault:   true,
	}); err != nil {
		return Detail{}, err
	}

	if err := txStore.AddMember(ctx, g.ID, ownerID); err != nil {
		return Detail{}, err
	}

	categoryName := "Текст"
	category, err := txStore.CreateChannel(ctx, store.Channel{
		GuildID:  &g.ID,
		Type:     "category",
		Name:     &categoryName,
		Position: 0,
	})
	if err != nil {
		return Detail{}, err
	}

	generalName := "general"
	if _, err := txStore.CreateChannel(ctx, store.Channel{
		GuildID:  &g.ID,
		ParentID: &category.ID,
		Type:     "text",
		Name:     &generalName,
		Position: 1,
	}); err != nil {
		return Detail{}, err
	}

	voiceName := "Голосовой"
	if _, err := txStore.CreateChannel(ctx, store.Channel{
		GuildID:  &g.ID,
		Type:     "voice",
		Name:     &voiceName,
		Position: 2,
	}); err != nil {
		return Detail{}, err
	}

	if tx != nil {
		if err := tx.Commit(ctx); err != nil {
			return Detail{}, fmt.Errorf("коммит создания сервера: %w", err)
		}
	}

	return s.DetailFor(ctx, ownerID, g.ID)
}

// ListGuilds возвращает серверы, где пользователь состоит.
func (s *Service) ListGuilds(ctx context.Context, userID string) ([]store.Guild, error) {
	return s.store.GuildsForUser(ctx, userID)
}

// RequireMember проверяет, что пользователь состоит в сервере.
func (s *Service) RequireMember(ctx context.Context, userID, guildID string) error {
	member, err := s.store.MemberExists(ctx, guildID, userID)
	if err != nil {
		return err
	}
	if !member {
		return ErrForbidden
	}
	return nil
}

// DetailFor отдаёт сервер, только если пользователь в нём состоит.
func (s *Service) DetailFor(ctx context.Context, userID, guildID string) (Detail, error) {
	member, err := s.store.MemberExists(ctx, guildID, userID)
	if err != nil {
		return Detail{}, err
	}
	if !member {
		return Detail{}, ErrForbidden
	}

	g, err := s.store.GuildByID(ctx, guildID)
	if err != nil {
		return Detail{}, err
	}

	roles, err := s.store.RolesForGuild(ctx, guildID)
	if err != nil {
		return Detail{}, err
	}

	channels, err := s.store.ChannelsForGuild(ctx, guildID)
	if err != nil {
		return Detail{}, err
	}

	return Detail{Guild: g, Roles: roles, Channels: channels}, nil
}

// Permissions вычисляет права участника.
//
// channelID == "" — права на уровне сервера (без переопределений канала).
func (s *Service) Permissions(ctx context.Context, guildID, userID, channelID string) (perms.Permission, error) {
	g, err := s.store.GuildByID(ctx, guildID)
	if err != nil {
		return 0, err
	}

	roles, err := s.store.RolesForGuild(ctx, guildID)
	if err != nil {
		return 0, err
	}

	var base perms.Permission
	var everyoneRoleID string
	for _, role := range roles {
		if role.IsDefault {
			base |= perms.Permission(role.Permissions)
			everyoneRoleID = role.ID
		}
	}

	memberRoles, err := s.store.RolesForMember(ctx, guildID, userID)
	if err != nil {
		return 0, err
	}

	assigned := make(map[string]bool, len(memberRoles))
	for _, role := range memberRoles {
		base |= perms.Permission(role.Permissions)
		assigned[role.ID] = true
	}

	// Владелец сервера всегда администратор — иначе он мог бы потерять
	// доступ к собственному серверу, выдав себе неверные роли.
	if g.OwnerID == userID {
		base |= perms.Administrator
	}

	if channelID == "" {
		return base, nil
	}

	overwrites, err := s.store.OverwritesForChannel(ctx, channelID)
	if err != nil {
		return 0, err
	}

	pc := perms.Context{Base: base}
	for _, o := range overwrites {
		ow := perms.Overwrite{Allow: perms.Permission(o.Allow), Deny: perms.Permission(o.Deny)}

		switch o.TargetType {
		case "role":
			switch {
			case o.TargetID == everyoneRoleID:
				pc.Everyone = ow
			case assigned[o.TargetID]:
				pc.Roles = append(pc.Roles, ow)
			}
		case "member":
			if o.TargetID == userID {
				member := ow
				pc.Member = &member
			}
		}
	}

	return perms.Compute(pc), nil
}

type CreateChannelInput struct {
	Name     string
	Type     string
	Topic    string
	ParentID *string
}

func (s *Service) CreateChannel(ctx context.Context, actorID, guildID string, in CreateChannelInput) (store.Channel, error) {
	if err := s.requireGuildPermission(ctx, actorID, guildID, perms.ManageChannels); err != nil {
		return store.Channel{}, err
	}

	name := strings.TrimSpace(in.Name)
	switch in.Type {
	case "text", "announcement", "forum":
		if name == "" || len(name) > 100 {
			return store.Channel{}, fmt.Errorf("%w: имя канала — от 1 до 100 символов", ErrValidation)
		}
		name = strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	case "voice":
		if name == "" || len(name) > 100 {
			return store.Channel{}, fmt.Errorf("%w: имя канала — от 1 до 100 символов", ErrValidation)
		}
	case "category":
		if name == "" || len(name) > 100 {
			return store.Channel{}, fmt.Errorf("%w: имя категории — от 1 до 100 символов", ErrValidation)
		}
	default:
		return store.Channel{}, fmt.Errorf("%w: неизвестный тип канала %q", ErrValidation, in.Type)
	}

	channel := store.Channel{
		GuildID:  &guildID,
		ParentID: in.ParentID,
		Type:     in.Type,
		Name:     &name,
	}

	if in.Topic != "" {
		if len(in.Topic) > 1024 {
			return store.Channel{}, fmt.Errorf("%w: тема длиннее 1024 символов", ErrValidation)
		}
		channel.Topic = &in.Topic
	}

	return s.store.CreateChannel(ctx, channel)
}

type CreateRoleInput struct {
	Name        string
	Color       int32
	Permissions int64
}

func (s *Service) CreateRole(ctx context.Context, actorID, guildID string, in CreateRoleInput) (store.Role, error) {
	if err := s.requireGuildPermission(ctx, actorID, guildID, perms.ManageRoles); err != nil {
		return store.Role{}, err
	}

	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 100 {
		return store.Role{}, fmt.Errorf("%w: имя роли — от 1 до 100 символов", ErrValidation)
	}
	if in.Color < 0 || in.Color > 16777215 {
		return store.Role{}, fmt.Errorf("%w: цвет вне диапазона 0..16777215", ErrValidation)
	}

	// Нельзя выдать роли права, которых нет у тебя самого: иначе любой
	// с ManageRoles мгновенно повысил бы себя до администратора.
	// Администратор — исключение: у него есть всё по определению.
	actorPerms, err := s.Permissions(ctx, guildID, actorID, "")
	if err != nil {
		return store.Role{}, err
	}
	granted := perms.Permission(in.Permissions)
	if !actorPerms.IsAdmin() && granted&^actorPerms != 0 {
		return store.Role{}, fmt.Errorf("%w: нельзя выдать права, которых нет у вас", ErrForbidden)
	}

	highest, err := s.store.HighestRolePosition(ctx, guildID, actorID)
	if err != nil {
		return store.Role{}, err
	}

	return s.store.CreateRole(ctx, store.Role{
		GuildID:     guildID,
		Name:        name,
		Color:       in.Color,
		Position:    highest + 1, // новая роль всегда выше всех у создателя
		Permissions: in.Permissions,
	})
}

func (s *Service) AssignRole(ctx context.Context, actorID, guildID, targetUserID, roleID string) error {
	if err := s.requireGuildPermission(ctx, actorID, guildID, perms.ManageRoles); err != nil {
		return err
	}

	targetRole, err := s.roleByID(ctx, guildID, roleID)
	if err != nil {
		return err
	}
	if targetRole.IsDefault {
		return fmt.Errorf("%w: роль @everyone выдать нельзя", ErrValidation)
	}

	if err := s.checkHierarchy(ctx, guildID, actorID, targetUserID, targetRole.Position); err != nil {
		return err
	}

	return s.store.AssignRole(ctx, guildID, targetUserID, roleID)
}

func (s *Service) UnassignRole(ctx context.Context, actorID, guildID, targetUserID, roleID string) error {
	if err := s.requireGuildPermission(ctx, actorID, guildID, perms.ManageRoles); err != nil {
		return err
	}

	targetRole, err := s.roleByID(ctx, guildID, roleID)
	if err != nil {
		return err
	}
	if targetRole.IsDefault {
		return fmt.Errorf("%w: роль @everyone снять нельзя", ErrValidation)
	}

	if err := s.checkHierarchy(ctx, guildID, actorID, targetUserID, targetRole.Position); err != nil {
		return err
	}

	return s.store.UnassignRole(ctx, guildID, targetUserID, roleID)
}

// checkHierarchy не даёт трогать тех, кто выше или равен по должности.
func (s *Service) checkHierarchy(ctx context.Context, guildID, actorID, targetUserID string, rolePosition int32) error {
	g, err := s.store.GuildByID(ctx, guildID)
	if err != nil {
		return err
	}
	if g.OwnerID == actorID {
		return nil // владелец выше всех
	}

	actorPos, err := s.store.HighestRolePosition(ctx, guildID, actorID)
	if err != nil {
		return err
	}
	if actorPos <= rolePosition {
		return fmt.Errorf("%w: нельзя управлять ролью не ниже своей", ErrHierarchy)
	}

	targetPos, err := s.store.HighestRolePosition(ctx, guildID, targetUserID)
	if err != nil {
		return err
	}
	if targetPos >= actorPos {
		return fmt.Errorf("%w: нельзя менять роли участника не ниже вас", ErrHierarchy)
	}

	return nil
}

func (s *Service) roleByID(ctx context.Context, guildID, roleID string) (store.Role, error) {
	roles, err := s.store.RolesForGuild(ctx, guildID)
	if err != nil {
		return store.Role{}, err
	}
	for _, role := range roles {
		if role.ID == roleID {
			return role, nil
		}
	}
	return store.Role{}, store.ErrRoleNotFound
}

func (s *Service) requireGuildPermission(ctx context.Context, userID, guildID string, want perms.Permission) error {
	member, err := s.store.MemberExists(ctx, guildID, userID)
	if err != nil {
		return err
	}
	if !member {
		return ErrForbidden
	}

	p, err := s.Permissions(ctx, guildID, userID, "")
	if err != nil {
		return err
	}
	if !p.Has(want) {
		return fmt.Errorf("%w: нужно право %b", ErrForbidden, int64(want))
	}

	return nil
}

func (s *Service) CreateInvite(ctx context.Context, actorID, guildID, channelID string, maxUses *int32) (string, error) {
	if err := s.requireGuildPermission(ctx, actorID, guildID, perms.CreateInvite); err != nil {
		return "", err
	}

	code, err := randomCode()
	if err != nil {
		return "", err
	}

	if err := s.store.CreateInvite(ctx, code, guildID, channelID, actorID, maxUses); err != nil {
		return "", err
	}

	return code, nil
}

// JoinByInvite добавляет пользователя на сервер по коду.
func (s *Service) JoinByInvite(ctx context.Context, userID, code string) (string, error) {
	invite, err := s.store.InviteByCode(ctx, code)
	if err != nil {
		return "", err
	}

	if invite.MaxUses != nil && invite.Uses >= *invite.MaxUses {
		return "", store.ErrInviteExhausted
	}

	if err := s.store.AddMember(ctx, invite.GuildID, userID); err != nil {
		return "", err
	}
	if err := s.store.IncrementInviteUses(ctx, code); err != nil {
		return "", err
	}

	return invite.GuildID, nil
}
