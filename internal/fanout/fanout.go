// Package fanout — доставка событий между экземплярами сервера.
//
// Один сервер обслуживает соединения своим хабом. Когда серверов несколько,
// событие, созданное на узле A, должно дойти до клиентов узла B. Для этого
// публикуем в Redis, а каждый узел подписан на каналы гильдий и раздаёт
// полученное своим соединениям.
//
// Узел-отправитель не дублирует доставку: в сообщении есть node_id, и
// подписчик игнорирует сообщения от самого себя (свои клиенты уже получили
// событие напрямую через хаб).
package fanout

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Nemanc123/discord_clone/internal/chat"
)

const ChannelPrefix = "discord_clone:guild:"

const publishTimeout = 2 * time.Second

// Local — то, что умеет доставлять события локальным соединениям. Это хаб.
type Local interface {
	Publish(ctx context.Context, guildID string, event chat.Event)
}

type wireEvent struct {
	NodeID string     `json:"node_id"`
	Event  chat.Event `json:"event"`
}

type Publisher struct {
	local  Local
	rdb    *redis.Client
	nodeID string
}

func NewPublisher(local Local, rdb *redis.Client, nodeID string) *Publisher {
	return &Publisher{local: local, rdb: rdb, nodeID: nodeID}
}

// Publish сначала отдаёт событие своим клиентам, потом публикует в Redis.
// Порядок важен: локальная доставка не должна зависеть от доступности Redis.
func (p *Publisher) Publish(ctx context.Context, guildID string, event chat.Event) {
	p.local.Publish(ctx, guildID, event)

	payload, err := json.Marshal(wireEvent{NodeID: p.nodeID, Event: event})
	if err != nil {
		slog.Error("fanout: кодирование события", "err", err, "type", event.Type)
		return
	}

	pubCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()

	if err := p.rdb.Publish(pubCtx, ChannelPrefix+guildID, payload).Err(); err != nil {
		// Redis упал — локальные клиенты событие уже получили.
		// Теряем только другие узлы; сервер продолжает работать.
		slog.Warn("fanout: публикация в redis не удалась", "err", err, "guild_id", guildID)
	}
}

type Subscriber struct {
	local  Local
	rdb    *redis.Client
	nodeID string
}

func NewSubscriber(local Local, rdb *redis.Client, nodeID string) *Subscriber {
	return &Subscriber{local: local, rdb: rdb, nodeID: nodeID}
}

// Run блокируется до отмены контекста.
func (s *Subscriber) Run(ctx context.Context) {
	sub := s.rdb.PSubscribe(ctx, ChannelPrefix+"*")
	defer func() { _ = sub.Close() }()

	slog.Info("fanout: подписка на события гильдий", "node_id", s.nodeID)

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-sub.Channel():
			if !ok {
				return
			}

			var w wireEvent
			if err := json.Unmarshal([]byte(msg.Payload), &w); err != nil {
				slog.Warn("fanout: неразборчивое сообщение", "err", err)
				continue
			}
			if w.NodeID == s.nodeID {
				continue // свои события уже доставлены
			}

			guildID := strings.TrimPrefix(msg.Channel, ChannelPrefix)
			s.local.Publish(ctx, guildID, w.Event)
		}
	}
}
