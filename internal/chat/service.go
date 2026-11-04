// Package chat — сообщения: отправка, лента, правка, удаление.
package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nemanc123/discord_clone/internal/guild"
	"github.com/Nemanc123/discord_clone/internal/perms"
	"github.com/Nemanc123/discord_clone/internal/store"
)

var (
	ErrValidation = errors.New("неверные данные")
	ErrForbidden  = errors.New("недостаточно прав")
)

const (
	MaxContentLen = 4000
	DefaultLimit  = 50
	MaxLimit      = 100

	// MaxAttachmentBytes — предел на один файл.
	MaxAttachmentBytes = 25 << 20
	MaxFilenameLen     = 255
)

// Event — то, что уходит подписчикам через шлюз.
type Event struct {
	Type string
	// ChannelID нужен шлюзу, чтобы проверить VIEW_CHANNEL у получателя.
	// Пустая строка — событие не привязано к каналу.
	ChannelID string
	Data      any
}

// Publisher отвязывает сервис от конкретного способа доставки.
// Реализация — либо внутрипроцессный хаб, либо хаб + Redis pub/sub
// (см. internal/fanout).
type Publisher interface {
	Publish(ctx context.Context, guildID string, event Event)
}

// AttachmentStorage — минимум, который нужен сервису от хранилища файлов.
type AttachmentStorage interface {
	PresignedPut(ctx context.Context, key string) (string, error)
	Stat(ctx context.Context, key string) (size int64, exists bool, err error)
}

type Service struct {
	store   *store.Store
	guild   *guild.Service
	pub     Publisher
	storage AttachmentStorage
}

func NewService(st *store.Store, gs *guild.Service, pub Publisher) *Service {
	return &Service{store: st, guild: gs, pub: pub}
}

// WithStorage включает поддержку вложений. Без хранилища сервис работает
// как раньше — текстовые сообщения без файлов.
func (s *Service) WithStorage(st AttachmentStorage) *Service {
	s.storage = st
	return s
}

// MessageView — сообщение в том виде, в каком его видят клиенты.
type MessageView struct {
	ID          string           `json:"id"`
	Seq         int64            `json:"seq"`
	ChannelID   string           `json:"channel_id"`
	AuthorID    *string          `json:"author_id,omitempty"`
	Content     string           `json:"content"`
	Type        string           `json:"type"`
	ReplyToID   *string          `json:"reply_to_id,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
	EditedAt    *time.Time       `json:"edited_at,omitempty"`
	Attachments []AttachmentView `json:"attachments,omitempty"`
}

type AttachmentView struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	StorageKey  string `json:"storage_key"`
}

func ToView(m store.Message) MessageView {
	return MessageView{
		ID:        m.ID,
		Seq:       m.Seq,
		ChannelID: m.ChannelID,
		AuthorID:  m.AuthorID,
		Content:   m.Content,
		Type:      m.Type,
		ReplyToID: m.ReplyToID,
		CreatedAt: m.CreatedAt,
		EditedAt:  m.EditedAt,
	}
}

func toViewWithAttachments(m store.Message, attachments []store.Attachment) MessageView {
	view := ToView(m)
	if len(attachments) == 0 {
		return view
	}

	view.Attachments = make([]AttachmentView, 0, len(attachments))
	for _, a := range attachments {
		view.Attachments = append(view.Attachments, AttachmentView{
			ID:          a.ID,
			Filename:    a.Filename,
			ContentType: a.ContentType,
			SizeBytes:   a.SizeBytes,
			StorageKey:  a.StorageKey,
		})
	}
	return view
}

func (s *Service) Post(ctx context.Context, actorID, channelID, content string, replyTo *string) (store.Message, error) {
	msg, _, err := s.post(ctx, actorID, channelID, content, replyTo, nil)
	return msg, err
}

// PostWithAttachments — отправка с файлами. Вложения уже должны быть
// загружены в хранилище по ссылке из PresignAttachment.
// Возвращает готовое представление: клиенту нужны вложения в ответе,
// иначе после отправки придётся перечитывать ленту.
func (s *Service) PostWithAttachments(ctx context.Context, actorID, channelID, content string, replyTo *string, attachments []store.CreateAttachmentParams) (MessageView, error) {
	msg, stored, err := s.post(ctx, actorID, channelID, content, replyTo, attachments)
	if err != nil {
		return MessageView{}, err
	}
	return toViewWithAttachments(msg, stored), nil
}

func (s *Service) post(ctx context.Context, actorID, channelID, content string, replyTo *string, attachments []store.CreateAttachmentParams) (store.Message, []store.Attachment, error) {
	channel, err := s.authorize(ctx, actorID, channelID, perms.SendMessages)
	if err != nil {
		return store.Message{}, nil, err
	}

	content = strings.TrimSpace(content)
	if content == "" && len(attachments) == 0 {
		return store.Message{}, nil, fmt.Errorf("%w: сообщение не может быть пустым", ErrValidation)
	}
	if len([]rune(content)) > MaxContentLen {
		return store.Message{}, nil, fmt.Errorf("%w: сообщение длиннее %d символов", ErrValidation, MaxContentLen)
	}

	// Вложения проверяем до создания сообщения: иначе при битом ключе
	// в базе останется пустое сообщение, а клиент получит ошибку.
	prepared, err := s.validateAttachments(ctx, attachments)
	if err != nil {
		return store.Message{}, nil, err
	}

	msgType := "default"
	if replyTo != nil && *replyTo != "" {
		msgType = "reply"
		parent, err := s.store.MessageByID(ctx, *replyTo)
		if err != nil {
			return store.Message{}, nil, fmt.Errorf("%w: сообщение для ответа не найдено", ErrValidation)
		}
		// Отвечать можно только внутри того же канала: иначе ответ
		// утащит читателя в другую ветку разговора.
		if parent.ChannelID != channelID {
			return store.Message{}, nil, fmt.Errorf("%w: ответ должен быть в том же канале", ErrValidation)
		}
	}

	msg, err := s.store.CreateMessage(ctx, store.CreateMessageParams{
		ChannelID: channelID,
		AuthorID:  actorID,
		Content:   content,
		Type:      msgType,
		ReplyToID: replyTo,
	})
	if err != nil {
		return store.Message{}, nil, err
	}

	stored := make([]store.Attachment, 0, len(prepared))
	for _, attachment := range prepared {
		attachment.MessageID = msg.ID
		created, err := s.store.CreateAttachment(ctx, attachment)
		if err != nil {
			return store.Message{}, nil, err
		}
		stored = append(stored, created)
	}

	s.publish(ctx, channel, Event{
		Type:      "MESSAGE_CREATE",
		ChannelID: channel.ID,
		Data:      toViewWithAttachments(msg, stored),
	})

	return msg, stored, nil
}

// validateAttachments проверяет файлы до вставки сообщения и подменяет
// размер клиента на реальный размер объекта в хранилище.
func (s *Service) validateAttachments(ctx context.Context, attachments []store.CreateAttachmentParams) ([]store.CreateAttachmentParams, error) {
	if len(attachments) == 0 {
		return nil, nil
	}
	if s.storage == nil {
		return nil, fmt.Errorf("%w: хранилище файлов не настроено", ErrValidation)
	}

	out := make([]store.CreateAttachmentParams, 0, len(attachments))
	for _, a := range attachments {
		a.Filename = strings.TrimSpace(a.Filename)
		if a.StorageKey == "" {
			return nil, fmt.Errorf("%w: у вложения нет ключа хранилища", ErrValidation)
		}
		if a.Filename == "" || len([]rune(a.Filename)) > MaxFilenameLen {
			return nil, fmt.Errorf("%w: имя файла — от 1 до %d символов", ErrValidation, MaxFilenameLen)
		}

		size, exists, err := s.storage.Stat(ctx, a.StorageKey)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("%w: файл не загружен в хранилище", ErrValidation)
		}
		if size <= 0 || size > MaxAttachmentBytes {
			return nil, fmt.Errorf("%w: размер файла — от 1 байта до 25 МиБ", ErrValidation)
		}

		// Размер берём из хранилища: клиентское значение можно подделать.
		a.SizeBytes = size
		out = append(out, a)
	}
	return out, nil
}

// History отдаёт ленту назад от курсора before (0 — с конца).
// Результат в обратном порядке: от старых к новым, как удобно рисовать.
func (s *Service) History(ctx context.Context, actorID, channelID string, before int64, limit int32) ([]MessageView, error) {
	if _, err := s.authorize(ctx, actorID, channelID, perms.ViewChannel); err != nil {
		return nil, err
	}

	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	if before < 0 {
		before = 0
	}

	msgs, err := s.store.MessagesBefore(ctx, channelID, before, limit)
	if err != nil {
		return nil, err
	}

	// Вложения грузим одной пачкой: по запросу на сообщение — это N+1.
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.ID)
	}

	attachments, err := s.store.AttachmentsForMessages(ctx, ids)
	if err != nil {
		return nil, err
	}

	views := make([]MessageView, 0, len(msgs))
	for _, m := range msgs {
		views = append(views, toViewWithAttachments(m, attachments[m.ID]))
	}

	// Разворачиваем: клиенту удобнее рисовать сверху вниз.
	for i, j := 0, len(views)-1; i < j; i, j = i+1, j-1 {
		views[i], views[j] = views[j], views[i]
	}

	return views, nil
}

func (s *Service) Edit(ctx context.Context, actorID, messageID, content string) (store.Message, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return store.Message{}, fmt.Errorf("%w: сообщение не может быть пустым", ErrValidation)
	}
	if len([]rune(content)) > MaxContentLen {
		return store.Message{}, fmt.Errorf("%w: сообщение длиннее %d символов", ErrValidation, MaxContentLen)
	}

	msg, err := s.store.MessageByID(ctx, messageID)
	if err != nil {
		return store.Message{}, err
	}

	channel, err := s.authorize(ctx, actorID, msg.ChannelID, perms.ViewChannel)
	if err != nil {
		return store.Message{}, err
	}

	// Чужое сообщение правит только тот, у кого есть ManageMessages.
	if msg.AuthorID == nil || *msg.AuthorID != actorID {
		if err := s.requireChannelPermission(ctx, actorID, msg.ChannelID, perms.ManageMessages); err != nil {
			return store.Message{}, err
		}
	}

	updated, err := s.store.UpdateMessageContent(ctx, messageID, content)
	if err != nil {
		return store.Message{}, err
	}

	s.publish(ctx, channel, Event{Type: "MESSAGE_UPDATE", ChannelID: channel.ID, Data: ToView(updated)})

	return updated, nil
}

func (s *Service) Delete(ctx context.Context, actorID, messageID string) error {
	msg, err := s.store.MessageByID(ctx, messageID)
	if err != nil {
		return err
	}

	channel, err := s.authorize(ctx, actorID, msg.ChannelID, perms.ViewChannel)
	if err != nil {
		return err
	}

	if msg.AuthorID == nil || *msg.AuthorID != actorID {
		if err := s.requireChannelPermission(ctx, actorID, msg.ChannelID, perms.ManageMessages); err != nil {
			return err
		}
	}

	if err := s.store.SoftDeleteMessage(ctx, messageID); err != nil {
		return err
	}

	s.publish(ctx, channel, Event{Type: "MESSAGE_DELETE", ChannelID: channel.ID, Data: map[string]string{
		"id":         msg.ID,
		"channel_id": msg.ChannelID,
	}})

	return nil
}

// MarkRead двигает отметку о прочтении до seq.
func (s *Service) MarkRead(ctx context.Context, userID, channelID string, seq int64) error {
	if seq < 0 {
		return fmt.Errorf("%w: seq не может быть отрицательным", ErrValidation)
	}

	if _, err := s.authorize(ctx, userID, channelID, perms.ViewChannel); err != nil {
		return err
	}

	return s.store.MarkRead(ctx, userID, channelID, seq)
}

// Unread отдаёт счётчики непрочитанных по каналам сервера.
func (s *Service) Unread(ctx context.Context, userID, guildID string) ([]store.Unread, error) {
	member, err := s.store.MemberExists(ctx, guildID, userID)
	if err != nil {
		return nil, err
	}
	if !member {
		return nil, fmt.Errorf("%w: вы не участник этого сервера", ErrForbidden)
	}

	return s.store.UnreadForGuild(ctx, userID, guildID)
}

// PresignAttachment выдаёт ссылку на загрузку и ключ, который потом
// прикрепляется к сообщению. Сам файл через сервер не идёт.
func (s *Service) PresignAttachment(ctx context.Context, userID, channelID, filename string, sizeBytes int64) (key, uploadURL string, err error) {
	if s.storage == nil {
		return "", "", fmt.Errorf("%w: хранилище файлов не настроено", ErrValidation)
	}

	if _, err := s.authorize(ctx, userID, channelID, perms.AttachFiles); err != nil {
		return "", "", err
	}

	filename = strings.TrimSpace(filename)
	if filename == "" || len([]rune(filename)) > MaxFilenameLen {
		return "", "", fmt.Errorf("%w: имя файла — от 1 до %d символов", ErrValidation, MaxFilenameLen)
	}
	if sizeBytes <= 0 || sizeBytes > MaxAttachmentBytes {
		return "", "", fmt.Errorf("%w: размер файла — от 1 байта до 25 МиБ", ErrValidation)
	}

	key = fmt.Sprintf("attachments/%s/%s%s", channelID, randomID(), strings.ToLower(filepath.Ext(filename)))

	uploadURL, err = s.storage.PresignedPut(ctx, key)
	if err != nil {
		return "", "", err
	}

	return key, uploadURL, nil
}

func randomID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func (s *Service) requireChannelPermission(ctx context.Context, userID, channelID string, want perms.Permission) error {
	// Через authorize — чтобы проверка членства была ровно в одном месте.
	_, err := s.authorize(ctx, userID, channelID, want)
	return err
}

// authorize проверяет, что канал существует, относится к серверу, пользователь
// в этом сервере состоит и у него есть нужное право именно в этом канале.
//
// Проверка членства обязательна: @everyone-права выдаются всем, включая тех,
// кто в сервере не состоит, — без неё писать мог бы любой зарегистрированный.
func (s *Service) authorize(ctx context.Context, userID, channelID string, want perms.Permission) (store.Channel, error) {
	channel, err := s.store.ChannelByID(ctx, channelID)
	if err != nil {
		return store.Channel{}, err
	}
	if channel.GuildID == nil {
		return store.Channel{}, fmt.Errorf("%w: личные каналы пока не поддерживаются", ErrValidation)
	}

	member, err := s.store.MemberExists(ctx, *channel.GuildID, userID)
	if err != nil {
		return store.Channel{}, err
	}
	if !member {
		return store.Channel{}, fmt.Errorf("%w: вы не участник этого сервера", ErrForbidden)
	}

	p, err := s.guild.Permissions(ctx, *channel.GuildID, userID, channelID)
	if err != nil {
		return store.Channel{}, err
	}
	if !p.Has(want) {
		return store.Channel{}, fmt.Errorf("%w: нужно право %b", ErrForbidden, int64(want))
	}

	return channel, nil
}

func (s *Service) publish(ctx context.Context, channel store.Channel, event Event) {
	if s.pub == nil || channel.GuildID == nil {
		return
	}
	s.pub.Publish(ctx, *channel.GuildID, event)
}
