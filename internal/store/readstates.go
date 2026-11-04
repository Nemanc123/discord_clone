package store

import (
	"context"
	"fmt"
)

// Unread — сколько непрочитанных в канале.
type Unread struct {
	ChannelID string
	Count     int64
}

// MarkRead двигает отметку вперёд. Назад — никогда: если клиент пришлёт
// меньший seq (например, из-за гонки), отметка не откатится.
func (s *Store) MarkRead(ctx context.Context, userID, channelID string, seq int64) error {
	const query = `
		INSERT INTO read_states (user_id, channel_id, last_read_seq, updated_at)
		VALUES ($1::uuid, $2::uuid, $3, now())
		ON CONFLICT (user_id, channel_id) DO UPDATE
		   SET last_read_seq = GREATEST(read_states.last_read_seq, EXCLUDED.last_read_seq),
		       updated_at = now()`

	if _, err := s.db.Exec(ctx, query, userID, channelID, seq); err != nil {
		return fmt.Errorf("отметка о прочтении: %w", err)
	}
	return nil
}

// UnreadForGuild считает непрочитанные по всем каналам сервера.
//
// Свои сообщения не считаются: человек не должен видеть бейдж из-за того,
// что написал сам. Системные (author_id IS NULL) считаются — их пишет сервер.
func (s *Store) UnreadForGuild(ctx context.Context, userID, guildID string) ([]Unread, error) {
	const query = `
		SELECT c.id::text,
		       (SELECT count(*)
		          FROM messages m
		         WHERE m.channel_id = c.id
		           AND m.seq > COALESCE(rs.last_read_seq, 0)
		           AND m.deleted_at IS NULL
		           AND (m.author_id IS NULL OR m.author_id <> $1::uuid)) AS unread
		  FROM channels c
		  LEFT JOIN read_states rs
		         ON rs.channel_id = c.id AND rs.user_id = $1::uuid
		 WHERE c.guild_id = $2::uuid
		   AND c.deleted_at IS NULL
		   AND c.type IN ('text', 'announcement', 'forum')
		 ORDER BY c.position, c.name`

	rows, err := s.db.Query(ctx, query, userID, guildID)
	if err != nil {
		return nil, fmt.Errorf("непрочитанные: %w", err)
	}
	defer rows.Close()

	var out []Unread
	for rows.Next() {
		var u Unread
		if err := rows.Scan(&u.ChannelID, &u.Count); err != nil {
			return nil, fmt.Errorf("чтение непрочитанных: %w", err)
		}
		out = append(out, u)
	}

	return out, rows.Err()
}
