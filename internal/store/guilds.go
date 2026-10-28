package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrGuildNotFound   = errors.New("сервер не найден")
	ErrMemberNotFound  = errors.New("участник не найден")
	ErrChannelNotFound = errors.New("канал не найден")
	ErrRoleNotFound    = errors.New("роль не найдена")
	ErrInviteNotFound  = errors.New("приглашение не найдено")
	ErrInviteExhausted = errors.New("приглашение исчерпано")
)

type Guild struct {
	ID        string
	Name      string
	IconURL   *string
	OwnerID   string
	CreatedAt time.Time
}

type GuildMember struct {
	GuildID  string
	UserID   string
	Nickname *string
	JoinedAt time.Time
}

type Role struct {
	ID          string
	GuildID     string
	Name        string
	Color       int32
	Position    int32
	Permissions int64
	IsDefault   bool
	Hoist       bool
	Mentionable bool
}

type Channel struct {
	ID              string
	GuildID         *string
	ParentID        *string
	Type            string
	Name            *string
	Topic           *string
	Position        int32
	SlowmodeSeconds int32
	CreatedAt       time.Time
}

type ChannelOverwrite struct {
	TargetType string
	TargetID   string
	Allow      int64
	Deny       int64
}

func (s *Store) CreateGuild(ctx context.Context, name, ownerID string) (Guild, error) {
	const query = `
		INSERT INTO guilds (name, owner_id)
		VALUES ($1, $2::uuid)
		RETURNING id::text, name, icon_url, owner_id::text, created_at`

	var g Guild
	if err := s.db.QueryRow(ctx, query, name, ownerID).
		Scan(&g.ID, &g.Name, &g.IconURL, &g.OwnerID, &g.CreatedAt); err != nil {
		return Guild{}, fmt.Errorf("создание сервера: %w", err)
	}

	return g, nil
}

func (s *Store) GuildByID(ctx context.Context, id string) (Guild, error) {
	const query = `
		SELECT id::text, name, icon_url, owner_id::text, created_at
		  FROM guilds
		 WHERE id = $1::uuid AND deleted_at IS NULL`

	var g Guild
	err := s.db.QueryRow(ctx, query, id).
		Scan(&g.ID, &g.Name, &g.IconURL, &g.OwnerID, &g.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Guild{}, ErrGuildNotFound
	}
	if err != nil {
		return Guild{}, fmt.Errorf("сервер по id: %w", err)
	}

	return g, nil
}

func (s *Store) GuildsForUser(ctx context.Context, userID string) ([]Guild, error) {
	const query = `
		SELECT g.id::text, g.name, g.icon_url, g.owner_id::text, g.created_at
		  FROM guild_members gm
		  JOIN guilds g ON g.id = gm.guild_id
		 WHERE gm.user_id = $1::uuid AND g.deleted_at IS NULL
		 ORDER BY g.created_at`

	rows, err := s.db.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("серверы пользователя: %w", err)
	}
	defer rows.Close()

	var guilds []Guild
	for rows.Next() {
		var g Guild
		if err := rows.Scan(&g.ID, &g.Name, &g.IconURL, &g.OwnerID, &g.CreatedAt); err != nil {
			return nil, fmt.Errorf("чтение сервера: %w", err)
		}
		guilds = append(guilds, g)
	}

	return guilds, rows.Err()
}

func (s *Store) AddMember(ctx context.Context, guildID, userID string) error {
	const query = `
		INSERT INTO guild_members (guild_id, user_id)
		VALUES ($1::uuid, $2::uuid)
		ON CONFLICT DO NOTHING`

	if _, err := s.db.Exec(ctx, query, guildID, userID); err != nil {
		return fmt.Errorf("добавление участника: %w", err)
	}
	return nil
}

func (s *Store) MemberExists(ctx context.Context, guildID, userID string) (bool, error) {
	const query = `SELECT EXISTS (SELECT 1 FROM guild_members WHERE guild_id = $1::uuid AND user_id = $2::uuid)`

	var exists bool
	if err := s.db.QueryRow(ctx, query, guildID, userID).Scan(&exists); err != nil {
		return false, fmt.Errorf("проверка участника: %w", err)
	}
	return exists, nil
}

func (s *Store) CreateRole(ctx context.Context, r Role) (Role, error) {
	const query = `
		INSERT INTO roles (guild_id, name, color, position, permissions, is_default, hoist, mentionable)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id::text, guild_id::text, name, color, position, permissions, is_default, hoist, mentionable`

	var out Role
	err := s.db.QueryRow(ctx, query,
		r.GuildID, r.Name, r.Color, r.Position, r.Permissions, r.IsDefault, r.Hoist, r.Mentionable,
	).Scan(&out.ID, &out.GuildID, &out.Name, &out.Color, &out.Position,
		&out.Permissions, &out.IsDefault, &out.Hoist, &out.Mentionable)
	if err != nil {
		return Role{}, fmt.Errorf("создание роли: %w", err)
	}

	return out, nil
}

func (s *Store) RolesForGuild(ctx context.Context, guildID string) ([]Role, error) {
	const query = `
		SELECT id::text, guild_id::text, name, color, position, permissions, is_default, hoist, mentionable
		  FROM roles
		 WHERE guild_id = $1::uuid
		 ORDER BY position DESC, name`

	rows, err := s.db.Query(ctx, query, guildID)
	if err != nil {
		return nil, fmt.Errorf("роли сервера: %w", err)
	}
	defer rows.Close()

	return scanRoles(rows)
}

func (s *Store) RolesForMember(ctx context.Context, guildID, userID string) ([]Role, error) {
	const query = `
		SELECT r.id::text, r.guild_id::text, r.name, r.color, r.position,
		       r.permissions, r.is_default, r.hoist, r.mentionable
		  FROM member_roles mr
		  JOIN roles r ON r.id = mr.role_id
		 WHERE mr.guild_id = $1::uuid AND mr.user_id = $2::uuid
		 ORDER BY r.position DESC`

	rows, err := s.db.Query(ctx, query, guildID, userID)
	if err != nil {
		return nil, fmt.Errorf("роли участника: %w", err)
	}
	defer rows.Close()

	return scanRoles(rows)
}

func scanRoles(rows pgx.Rows) ([]Role, error) {
	var roles []Role
	for rows.Next() {
		var r Role
		if err := rows.Scan(&r.ID, &r.GuildID, &r.Name, &r.Color, &r.Position,
			&r.Permissions, &r.IsDefault, &r.Hoist, &r.Mentionable); err != nil {
			return nil, fmt.Errorf("чтение роли: %w", err)
		}
		roles = append(roles, r)
	}
	return roles, rows.Err()
}

func (s *Store) AssignRole(ctx context.Context, guildID, userID, roleID string) error {
	const query = `
		INSERT INTO member_roles (guild_id, user_id, role_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid)
		ON CONFLICT DO NOTHING`

	if _, err := s.db.Exec(ctx, query, guildID, userID, roleID); err != nil {
		return fmt.Errorf("выдача роли: %w", err)
	}
	return nil
}

func (s *Store) UnassignRole(ctx context.Context, guildID, userID, roleID string) error {
	const query = `
		DELETE FROM member_roles
		 WHERE guild_id = $1::uuid AND user_id = $2::uuid AND role_id = $3::uuid`

	if _, err := s.db.Exec(ctx, query, guildID, userID, roleID); err != nil {
		return fmt.Errorf("снятие роли: %w", err)
	}
	return nil
}

// HighestRolePosition — самая высокая позиция среди ролей участника.
// Нужна для иерархии: нельзя выдать роль выше своей, нельзя тронуть равного.
func (s *Store) HighestRolePosition(ctx context.Context, guildID, userID string) (int32, error) {
	const query = `
		SELECT COALESCE(MAX(r.position), 0)
		  FROM member_roles mr
		  JOIN roles r ON r.id = mr.role_id
		 WHERE mr.guild_id = $1::uuid AND mr.user_id = $2::uuid`

	var pos int32
	if err := s.db.QueryRow(ctx, query, guildID, userID).Scan(&pos); err != nil {
		return 0, fmt.Errorf("высшая роль: %w", err)
	}
	return pos, nil
}

func (s *Store) CreateChannel(ctx context.Context, c Channel) (Channel, error) {
	const query = `
		INSERT INTO channels (guild_id, parent_id, type, name, topic, position)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6)
		RETURNING id::text, guild_id::text, parent_id::text, type, name, topic, position, slowmode_seconds, created_at`

	var out Channel
	err := s.db.QueryRow(ctx, query, c.GuildID, c.ParentID, c.Type, c.Name, c.Topic, c.Position).
		Scan(&out.ID, &out.GuildID, &out.ParentID, &out.Type, &out.Name,
			&out.Topic, &out.Position, &out.SlowmodeSeconds, &out.CreatedAt)
	if err != nil {
		return Channel{}, fmt.Errorf("создание канала: %w", err)
	}

	return out, nil
}

func (s *Store) ChannelsForGuild(ctx context.Context, guildID string) ([]Channel, error) {
	const query = `
		SELECT id::text, guild_id::text, parent_id::text, type, name, topic,
		       position, slowmode_seconds, created_at
		  FROM channels
		 WHERE guild_id = $1::uuid AND deleted_at IS NULL
		 ORDER BY position, name`

	rows, err := s.db.Query(ctx, query, guildID)
	if err != nil {
		return nil, fmt.Errorf("каналы сервера: %w", err)
	}
	defer rows.Close()

	var channels []Channel
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.GuildID, &c.ParentID, &c.Type, &c.Name,
			&c.Topic, &c.Position, &c.SlowmodeSeconds, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("чтение канала: %w", err)
		}
		channels = append(channels, c)
	}

	return channels, rows.Err()
}

func (s *Store) ChannelByID(ctx context.Context, channelID string) (Channel, error) {
	const query = `
		SELECT id::text, guild_id::text, parent_id::text, type, name, topic,
		       position, slowmode_seconds, created_at
		  FROM channels
		 WHERE id = $1::uuid AND deleted_at IS NULL`

	var c Channel
	err := s.db.QueryRow(ctx, query, channelID).
		Scan(&c.ID, &c.GuildID, &c.ParentID, &c.Type, &c.Name,
			&c.Topic, &c.Position, &c.SlowmodeSeconds, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Channel{}, ErrChannelNotFound
	}
	if err != nil {
		return Channel{}, fmt.Errorf("канал по id: %w", err)
	}

	return c, nil
}

func (s *Store) OverwritesForChannel(ctx context.Context, channelID string) ([]ChannelOverwrite, error) {
	const query = `
		SELECT target_type, target_id::text, allow, deny
		  FROM channel_overwrites
		 WHERE channel_id = $1::uuid`

	rows, err := s.db.Query(ctx, query, channelID)
	if err != nil {
		return nil, fmt.Errorf("переопределения канала: %w", err)
	}
	defer rows.Close()

	var out []ChannelOverwrite
	for rows.Next() {
		var o ChannelOverwrite
		if err := rows.Scan(&o.TargetType, &o.TargetID, &o.Allow, &o.Deny); err != nil {
			return nil, fmt.Errorf("чтение переопределения: %w", err)
		}
		out = append(out, o)
	}

	return out, rows.Err()
}

// UpsertOverwrite создаёт или заменяет переопределение прав на канал.
func (s *Store) UpsertOverwrite(ctx context.Context, channelID, targetType, targetID string, allow, deny int64) error {
	const query = `
		INSERT INTO channel_overwrites (channel_id, target_type, target_id, allow, deny)
		VALUES ($1::uuid, $2, $3::uuid, $4, $5)
		ON CONFLICT (channel_id, target_type, target_id)
		DO UPDATE SET allow = EXCLUDED.allow, deny = EXCLUDED.deny`

	if _, err := s.db.Exec(ctx, query, channelID, targetType, targetID, allow, deny); err != nil {
		return fmt.Errorf("переопределение прав: %w", err)
	}
	return nil
}

func (s *Store) CreateInvite(ctx context.Context, code, guildID, channelID, inviterID string, maxUses *int32) error {
	const query = `
		INSERT INTO invites (code, guild_id, channel_id, inviter_id, max_uses)
		VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5)`

	if _, err := s.db.Exec(ctx, query, code, guildID, channelID, inviterID, maxUses); err != nil {
		return fmt.Errorf("создание приглашения: %w", err)
	}
	return nil
}

type Invite struct {
	Code      string
	GuildID   string
	ChannelID string
	Uses      int32
	MaxUses   *int32
	ExpiresAt *time.Time
}

func (s *Store) InviteByCode(ctx context.Context, code string) (Invite, error) {
	const query = `
		SELECT code, guild_id::text, channel_id::text, uses, max_uses, expires_at
		  FROM invites
		 WHERE code = $1`

	var i Invite
	err := s.db.QueryRow(ctx, query, code).
		Scan(&i.Code, &i.GuildID, &i.ChannelID, &i.Uses, &i.MaxUses, &i.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invite{}, ErrInviteNotFound
	}
	if err != nil {
		return Invite{}, fmt.Errorf("приглашение: %w", err)
	}

	return i, nil
}

func (s *Store) IncrementInviteUses(ctx context.Context, code string) error {
	const query = `UPDATE invites SET uses = uses + 1 WHERE code = $1`
	if _, err := s.db.Exec(ctx, query, code); err != nil {
		return fmt.Errorf("счётчик приглашения: %w", err)
	}
	return nil
}
