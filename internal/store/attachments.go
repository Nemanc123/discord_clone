package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var ErrAttachmentNotFound = errors.New("вложение не найдено")

type Attachment struct {
	ID          string
	MessageID   string
	Filename    string
	ContentType string
	SizeBytes   int64
	StorageKey  string
}

// AttachmentWithChannel — вложение вместе с каналом его сообщения.
// Канал нужен, чтобы проверить VIEW_CHANNEL перед выдачей ссылки.
type AttachmentWithChannel struct {
	Attachment
	ChannelID string
}

type CreateAttachmentParams struct {
	MessageID   string
	Filename    string
	ContentType string
	SizeBytes   int64
	StorageKey  string
}

func (s *Store) CreateAttachment(ctx context.Context, p CreateAttachmentParams) (Attachment, error) {
	const query = `
		INSERT INTO attachments (message_id, filename, content_type, size_bytes, storage_key)
		VALUES ($1::uuid, $2, $3, $4, $5)
		RETURNING id::text, message_id::text, filename, content_type, size_bytes, storage_key`

	var a Attachment
	err := s.db.QueryRow(ctx, query,
		p.MessageID, p.Filename, p.ContentType, p.SizeBytes, p.StorageKey,
	).Scan(&a.ID, &a.MessageID, &a.Filename, &a.ContentType, &a.SizeBytes, &a.StorageKey)
	if err != nil {
		return Attachment{}, fmt.Errorf("создание вложения: %w", err)
	}
	return a, nil
}

// AttachmentsForMessages загружает вложения пачкой — без N+1 запросов
// на каждое сообщение в ленте.
func (s *Store) AttachmentsForMessages(ctx context.Context, messageIDs []string) (map[string][]Attachment, error) {
	if len(messageIDs) == 0 {
		return map[string][]Attachment{}, nil
	}

	const query = `
		SELECT id::text, message_id::text, filename, content_type, size_bytes, storage_key
		  FROM attachments
		 WHERE message_id = ANY($1::uuid[])
		 ORDER BY created_at`

	rows, err := s.db.Query(ctx, query, messageIDs)
	if err != nil {
		return nil, fmt.Errorf("вложения: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]Attachment, len(messageIDs))
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.ID, &a.MessageID, &a.Filename, &a.ContentType, &a.SizeBytes, &a.StorageKey); err != nil {
			return nil, fmt.Errorf("чтение вложения: %w", err)
		}
		out[a.MessageID] = append(out[a.MessageID], a)
	}

	return out, rows.Err()
}

// AttachmentByID возвращает вложение и канал его сообщения.
// Удалённые сообщения не отдают вложения: файл остаётся, но ссылка — нет.
func (s *Store) AttachmentByID(ctx context.Context, id string) (AttachmentWithChannel, error) {
	const query = `
		SELECT a.id::text, a.message_id::text, a.filename, a.content_type,
		       a.size_bytes, a.storage_key, m.channel_id::text
		  FROM attachments a
		  JOIN messages m ON m.id = a.message_id
		 WHERE a.id = $1::uuid
		   AND m.deleted_at IS NULL`

	var a AttachmentWithChannel
	err := s.db.QueryRow(ctx, query, id).Scan(
		&a.ID, &a.MessageID, &a.Filename, &a.ContentType,
		&a.SizeBytes, &a.StorageKey, &a.ChannelID,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AttachmentWithChannel{}, ErrAttachmentNotFound
		}
		return AttachmentWithChannel{}, fmt.Errorf("вложение по id: %w", err)
	}
	return a, nil
}
