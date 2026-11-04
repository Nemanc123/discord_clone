package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrMessageNotFound = errors.New("сообщение не найдено")

type Message struct {
	ID        string
	Seq       int64
	ChannelID string
	AuthorID  *string
	Content   string
	Type      string
	ReplyToID *string
	CreatedAt time.Time
	EditedAt  *time.Time
	DeletedAt *time.Time
}

const messageColumns = `id::text, seq, channel_id::text, author_id::text, content,
	type, reply_to_id::text, created_at, edited_at, deleted_at`

func scanMessage(row pgx.Row) (Message, error) {
	var m Message
	err := row.Scan(&m.ID, &m.Seq, &m.ChannelID, &m.AuthorID, &m.Content,
		&m.Type, &m.ReplyToID, &m.CreatedAt, &m.EditedAt, &m.DeletedAt)
	return m, err
}

type CreateMessageParams struct {
	ChannelID string
	AuthorID  string
	Content   string
	Type      string
	ReplyToID *string
}

func (s *Store) CreateMessage(ctx context.Context, p CreateMessageParams) (Message, error) {
	const query = `
		INSERT INTO messages (channel_id, author_id, content, type, reply_to_id)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5::uuid)
		RETURNING ` + messageColumns

	m, err := scanMessage(s.db.QueryRow(ctx, query,
		p.ChannelID, p.AuthorID, p.Content, p.Type, p.ReplyToID))
	if err != nil {
		return Message{}, fmt.Errorf("создание сообщения: %w", err)
	}
	return m, nil
}

// MessagesBefore отдаёт ленту назад от курсора. before == 0 — с самого конца.
// Сортировка по seq DESC: так работает LIMIT вместе с индексом
// (channel_id, seq DESC).
func (s *Store) MessagesBefore(ctx context.Context, channelID string, before int64, limit int32) ([]Message, error) {
	const query = `
		SELECT ` + messageColumns + `
		  FROM messages
		 WHERE channel_id = $1::uuid
		   AND deleted_at IS NULL
		   AND ($2::bigint = 0 OR seq < $2::bigint)
		 ORDER BY seq DESC
		 LIMIT $3`

	rows, err := s.db.Query(ctx, query, channelID, before, limit)
	if err != nil {
		return nil, fmt.Errorf("лента сообщений: %w", err)
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("чтение сообщения: %w", err)
		}
		out = append(out, m)
	}

	return out, rows.Err()
}

func (s *Store) MessageByID(ctx context.Context, id string) (Message, error) {
	const query = `SELECT ` + messageColumns + ` FROM messages WHERE id = $1::uuid`

	m, err := scanMessage(s.db.QueryRow(ctx, query, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, ErrMessageNotFound
	}
	if err != nil {
		return Message{}, fmt.Errorf("сообщение по id: %w", err)
	}
	return m, nil
}

func (s *Store) UpdateMessageContent(ctx context.Context, id, content string) (Message, error) {
	const query = `
		UPDATE messages
		   SET content = $2, edited_at = now()
		 WHERE id = $1::uuid AND deleted_at IS NULL
		RETURNING ` + messageColumns

	m, err := scanMessage(s.db.QueryRow(ctx, query, id, content))
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, ErrMessageNotFound
	}
	if err != nil {
		return Message{}, fmt.Errorf("правка сообщения: %w", err)
	}
	return m, nil
}

func (s *Store) SoftDeleteMessage(ctx context.Context, id string) error {
	const query = `UPDATE messages SET deleted_at = now() WHERE id = $1::uuid AND deleted_at IS NULL`

	tag, err := s.db.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("удаление сообщения: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMessageNotFound
	}
	return nil
}
