package store

import (
	"context"
	"fmt"
	"time"
)

// VoiceSession — строка voice_sessions. Живая сессия — та, где LeftAt == nil.
type VoiceSession struct {
	ID        string
	ChannelID string
	UserID    string
	JoinedAt  time.Time
	LeftAt    *time.Time
}

// VoiceParticipant — участник голосового канала вместе с именем.
type VoiceParticipant struct {
	UserID      string
	Username    string
	DisplayName *string
	JoinedAt    time.Time
	SelfMute    bool
	SelfDeaf    bool
	SelfVideo   bool
	SelfStream  bool
}

// JoinVoiceSession открывает сессию. Повторный вызов для уже сидящего
// пользователя ничего не меняет: уникальный частичный индекс не даст дубля.
func (s *Store) JoinVoiceSession(ctx context.Context, channelID, userID string) error {
	const query = `
		INSERT INTO voice_sessions (channel_id, user_id)
		VALUES ($1::uuid, $2::uuid)
		ON CONFLICT (channel_id, user_id) WHERE left_at IS NULL DO NOTHING`

	if _, err := s.db.Exec(ctx, query, channelID, userID); err != nil {
		return fmt.Errorf("открытие голосовой сессии: %w", err)
	}
	return nil
}

// CloseOtherVoiceSessions закрывает живые сессии пользователя в других
// каналах: один человек не может сидеть в двух голосовых сразу.
func (s *Store) CloseOtherVoiceSessions(ctx context.Context, userID, exceptChannelID string) error {
	const query = `
		UPDATE voice_sessions
		   SET left_at = now()
		 WHERE user_id = $1::uuid
		   AND channel_id <> $2::uuid
		   AND left_at IS NULL`

	if _, err := s.db.Exec(ctx, query, userID, exceptChannelID); err != nil {
		return fmt.Errorf("закрытие старых голосовых сессий: %w", err)
	}
	return nil
}

func (s *Store) LeaveVoiceSession(ctx context.Context, channelID, userID string) error {
	const query = `
		UPDATE voice_sessions
		   SET left_at = now()
		 WHERE channel_id = $1::uuid
		   AND user_id = $2::uuid
		   AND left_at IS NULL`

	if _, err := s.db.Exec(ctx, query, channelID, userID); err != nil {
		return fmt.Errorf("закрытие голосовой сессии: %w", err)
	}
	return nil
}

// CloseVoiceChannel закрывает все живые сессии канала. Нужен, когда
// LiveKit сообщает room_finished: комната пуста или сервер её потерял.
func (s *Store) CloseVoiceChannel(ctx context.Context, channelID string) error {
	const query = `
		UPDATE voice_sessions
		   SET left_at = now()
		 WHERE channel_id = $1::uuid
		   AND left_at IS NULL`

	if _, err := s.db.Exec(ctx, query, channelID); err != nil {
		return fmt.Errorf("закрытие голосового канала: %w", err)
	}
	return nil
}

// VoiceParticipants — кто сейчас в канале, в порядке входа.
func (s *Store) VoiceParticipants(ctx context.Context, channelID string) ([]VoiceParticipant, error) {
	const query = `
		SELECT v.user_id::text, u.username, u.display_name, v.joined_at,
		       v.self_mute, v.self_deaf, v.self_video, v.self_stream
		  FROM voice_sessions v
		  JOIN users u ON u.id = v.user_id
		 WHERE v.channel_id = $1::uuid
		   AND v.left_at IS NULL
		 ORDER BY v.joined_at`

	rows, err := s.db.Query(ctx, query, channelID)
	if err != nil {
		return nil, fmt.Errorf("участники голосового канала: %w", err)
	}
	defer rows.Close()

	out := make([]VoiceParticipant, 0, 8)
	for rows.Next() {
		var p VoiceParticipant
		if err := rows.Scan(
			&p.UserID, &p.Username, &p.DisplayName, &p.JoinedAt,
			&p.SelfMute, &p.SelfDeaf, &p.SelfVideo, &p.SelfStream,
		); err != nil {
			return nil, fmt.Errorf("чтение участника голоса: %w", err)
		}
		out = append(out, p)
	}

	return out, rows.Err()
}
