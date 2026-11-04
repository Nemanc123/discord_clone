package voice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Nemanc123/discord_clone/internal/guild"
	"github.com/Nemanc123/discord_clone/internal/perms"
	"github.com/Nemanc123/discord_clone/internal/store"
)

var (
	ErrValidation = errors.New("неверные данные")
	ErrForbidden  = errors.New("недостаточно прав")
)

// Event — событие для шлюза. ChannelID позволяет отфильтровать его по
// VIEW_CHANNEL, как и события сообщений.
type Event struct {
	Type      string
	ChannelID string
	Data      any
}

// Publisher отвязывает голосовой сервис от конкретного способа доставки.
type Publisher interface {
	PublishVoice(ctx context.Context, guildID string, event Event)
}

// JoinResult — всё, что нужно клиенту, чтобы подключиться к комнате.
type JoinResult struct {
	Token      string `json:"token"`
	URL        string `json:"url"`
	Room       string `json:"room"`
	ExpiresIn  int    `json:"expires_in"`
	CanPublish bool   `json:"can_publish"`
}

type Service struct {
	store  *store.Store
	guild  *guild.Service
	issuer TokenIssuer
	url    string
	pub    Publisher
}

func NewService(st *store.Store, gs *guild.Service, issuer TokenIssuer, url string, pub Publisher) *Service {
	return &Service{store: st, guild: gs, issuer: issuer, url: url, pub: pub}
}

// Join проверяет права и выдаёт токен на вход в голосовой канал.
//
// Токен не создаёт строку в voice_sessions: факт входа фиксирует вебхук
// LiveKit. Иначе в базе висели бы «участники», которые так и не зашли.
func (s *Service) Join(ctx context.Context, userID, channelID string) (JoinResult, error) {
	channel, p, err := s.authorize(ctx, userID, channelID, perms.ViewChannel|perms.Connect)
	if err != nil {
		return JoinResult{}, err
	}

	if channel.Type != "voice" {
		return JoinResult{}, fmt.Errorf("%w: это не голосовой канал", ErrValidation)
	}

	user, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return JoinResult{}, err
	}

	name := user.Username
	if user.DisplayName != nil && strings.TrimSpace(*user.DisplayName) != "" {
		name = *user.DisplayName
	}

	token, err := s.issuer.Issue(user.ID, name, channel.ID, p.Has(perms.Speak))
	if err != nil {
		return JoinResult{}, err
	}

	return JoinResult{
		Token:      token,
		URL:        s.url,
		Room:       channel.ID,
		ExpiresIn:  int(s.issuer.TTL.Seconds()),
		CanPublish: p.Has(perms.Speak),
	}, nil
}

// Participants — кто сейчас в канале. Нужен клиенту, который только
// открыл канал и ещё не подключился к комнате.
func (s *Service) Participants(ctx context.Context, userID, channelID string) ([]store.VoiceParticipant, error) {
	channel, _, err := s.authorize(ctx, userID, channelID, perms.ViewChannel)
	if err != nil {
		return nil, err
	}
	if channel.Type != "voice" {
		return nil, fmt.Errorf("%w: это не голосовой канал", ErrValidation)
	}
	return s.store.VoiceParticipants(ctx, channelID)
}

// HandleWebhook проверяет уведомление LiveKit и применяет его к базе.
func (s *Service) HandleWebhook(ctx context.Context, rawToken string, body []byte) error {
	event, err := VerifyWebhook(s.issuer.APIKey, s.issuer.APISecret, rawToken, body)
	if err != nil {
		return err
	}

	switch event.Event {
	case "participant_joined":
		slog.Info("livekit webhook", "event", event.Event, "room", event.Room.Name, "user", event.Participant.Identity)
		return s.participantJoined(ctx, event.Room.Name, event.Participant.Identity)
	case "participant_left":
		slog.Info("livekit webhook", "event", event.Event, "room", event.Room.Name, "user", event.Participant.Identity)
		return s.participantLeft(ctx, event.Room.Name, event.Participant.Identity)
	case "room_finished":
		slog.Info("livekit webhook", "event", event.Event, "room", event.Room.Name)
		return s.roomFinished(ctx, event.Room.Name)
	default:
		// track_published, room_started и прочее нам пока не нужны.
		return nil
	}
}

func (s *Service) participantJoined(ctx context.Context, channelID, userID string) error {
	channel, err := s.voiceChannel(ctx, channelID)
	if err != nil {
		return err
	}
	if channel == nil {
		return nil // комната не наша или канал уже удалён
	}

	// Один человек — одна голосовая комната: вход в новую закрывает старую.
	if err := s.store.CloseOtherVoiceSessions(ctx, userID, channelID); err != nil {
		return err
	}
	if err := s.store.JoinVoiceSession(ctx, channelID, userID); err != nil {
		return err
	}

	s.publish(ctx, channel.GuildID, Event{
		Type:      "VOICE_STATE_UPDATE",
		ChannelID: channelID,
		Data: map[string]any{
			"channel_id": channelID,
			"user_id":    userID,
			"joined":     true,
		},
	})
	return nil
}

func (s *Service) participantLeft(ctx context.Context, channelID, userID string) error {
	channel, err := s.voiceChannel(ctx, channelID)
	if err != nil {
		return err
	}
	if channel == nil {
		return nil
	}

	if err := s.store.LeaveVoiceSession(ctx, channelID, userID); err != nil {
		return err
	}

	s.publish(ctx, channel.GuildID, Event{
		Type:      "VOICE_STATE_UPDATE",
		ChannelID: channelID,
		Data: map[string]any{
			"channel_id": channelID,
			"user_id":    userID,
			"joined":     false,
		},
	})
	return nil
}

func (s *Service) roomFinished(ctx context.Context, channelID string) error {
	channel, err := s.voiceChannel(ctx, channelID)
	if err != nil {
		return err
	}
	if channel == nil {
		return nil
	}

	// Сначала запоминаем состав: после UPDATE список уже пуст.
	participants, err := s.store.VoiceParticipants(ctx, channelID)
	if err != nil {
		return err
	}
	if err := s.store.CloseVoiceChannel(ctx, channelID); err != nil {
		return err
	}

	for _, p := range participants {
		s.publish(ctx, channel.GuildID, Event{
			Type:      "VOICE_STATE_UPDATE",
			ChannelID: channelID,
			Data: map[string]any{
				"channel_id": channelID,
				"user_id":    p.UserID,
				"joined":     false,
			},
		})
	}
	return nil
}

// voiceChannel возвращает канал, если он существует и голосовой.
// nil без ошибки — «не наш случай»: такой вебхук можно игнорировать.
func (s *Service) voiceChannel(ctx context.Context, channelID string) (*store.Channel, error) {
	channel, err := s.store.ChannelByID(ctx, channelID)
	if err != nil {
		if errors.Is(err, store.ErrChannelNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if channel.Type != "voice" {
		return nil, nil
	}
	return &channel, nil
}

func (s *Service) authorize(ctx context.Context, userID, channelID string, want perms.Permission) (store.Channel, perms.Permission, error) {
	channel, err := s.store.ChannelByID(ctx, channelID)
	if err != nil {
		return store.Channel{}, 0, err
	}
	if channel.GuildID == nil {
		return store.Channel{}, 0, fmt.Errorf("%w: личные каналы пока не поддерживаются", ErrValidation)
	}

	member, err := s.store.MemberExists(ctx, *channel.GuildID, userID)
	if err != nil {
		return store.Channel{}, 0, err
	}
	if !member {
		return store.Channel{}, 0, fmt.Errorf("%w: вы не участник этого сервера", ErrForbidden)
	}

	p, err := s.guild.Permissions(ctx, *channel.GuildID, userID, channelID)
	if err != nil {
		return store.Channel{}, 0, err
	}
	if !p.Has(want) {
		return store.Channel{}, 0, fmt.Errorf("%w: нужно право %b", ErrForbidden, int64(want))
	}

	return channel, p, nil
}

func (s *Service) publish(ctx context.Context, guildID *string, event Event) {
	if s.pub == nil || guildID == nil {
		return
	}
	s.pub.PublishVoice(ctx, *guildID, event)
}
