// Package chat — сообщения: отправка, лента, правка, удаление.
package chat

import (
	"context"
	"errors"
	"fmt"
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

type Service struct {
	store *store.Store
	guild *guild.Service
	pub   Publisher
}

func NewService(st *store.Store, gs *guild.Service, pub Publisher) *Service {
	return &Service{store: st, guild: gs, pub: pub}
}

// MessageView — сообщение в том виде, в каком его видят клиенты.
type MessageView struct {
	ID        string     `json:"id"`
	Seq       int64      `json:"seq"`
	ChannelID string     `json:"channel_id"`
	AuthorID  *string    `json:"author_id,omitempty"`
	Content   string     `json:"content"`
	Type      string     `json:"type"`
	ReplyToID *string    `json:"reply_to_id,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	EditedAt  *time.Time `json:"edited_at,omitempty"`
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

func (s *Service) Post(ctx context.Context, actorID, channelID, content string, replyTo *string) (store.Message, error) {
	channel, err := s.authorize(ctx, actorID, channelID, perms.SendMessages)
	if err != nil {
		return store.Message{}, err
	}

	content = strings.TrimSpace(content)
	if content == "" {
		return store.Message{}, fmt.Errorf("%w: сообщение не может быть пустым", ErrValidation)
	}
	if len([]rune(content)) > MaxContentLen {
		return store.Message{}, fmt.Errorf("%w: сообщение длиннее %d символов", ErrValidation, MaxContentLen)
	}

	msgType := "default"
	if replyTo != nil && *replyTo != "" {
		msgType = "reply"
		parent, err := s.store.MessageByID(ctx, *replyTo)
		if err != nil {
			return store.Message{}, fmt.Errorf("%w: сообщение для ответа не найдено", ErrValidation)
		}
		// Отвечать можно только внутри того же канала: иначе ответ
		// утащит читателя в другую ветку разговора.
		if parent.ChannelID != channelID {
			return store.Message{}, fmt.Errorf("%w: ответ должен быть в том же канале", ErrValidation)
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
		return store.Message{}, err
	}

	s.publish(ctx, channel, Event{Type: "MESSAGE_CREATE", ChannelID: channel.ID, Data: ToView(msg)})

	return msg, nil
}

// History отдаёт ленту назад от курсора before (0 — с конца).
// Результат в обратном порядке: от старых к новым, как удобно рисовать.
func (s *Service) History(ctx context.Context, actorID, channelID string, before int64, limit int32) ([]store.Message, error) {
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

	// Разворачиваем: клиенту удобнее рисовать сверху вниз.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}

	return msgs, nil
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
