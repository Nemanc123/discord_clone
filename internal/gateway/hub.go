// Package gateway — долгоживущие WebSocket-соединения.
//
// Хаб хранит подписки «гильдия → клиенты» и рассылает события. Один экземпляр
// хаба работает в одном процессе; для нескольких экземпляров поверх него
// ставится Redis pub/sub (internal/fanout), а хаб остаётся точкой доставки
// в локальные соединения.
package gateway

import (
	"context"
	"sync"

	"github.com/Nemanc123/discord_clone/internal/chat"
)

const (
	OpDispatch     = 0
	OpHeartbeat    = 1
	OpIdentify     = 2
	OpHello        = 10
	OpHeartbeatAck = 11
)

// PermissionFilter проверяет, может ли пользователь видеть канал.
// Интерфейс, а не конкретный сервис: хаб не должен знать про БД.
type PermissionFilter interface {
	CanView(ctx context.Context, guildID, userID, channelID string) bool
}

type Hub struct {
	mu      sync.RWMutex
	byGuild map[string]map[*Client]struct{}
	filter  PermissionFilter
}

func NewHub() *Hub {
	return &Hub{byGuild: make(map[string]map[*Client]struct{})}
}

// SetFilter включает проверку прав при рассылке. Без фильтра события
// получают все подписчики гильдии.
func (h *Hub) SetFilter(f PermissionFilter) {
	h.filter = f
}

func (h *Hub) Register(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, guildID := range c.guildIDs() {
		if h.byGuild[guildID] == nil {
			h.byGuild[guildID] = make(map[*Client]struct{})
		}
		h.byGuild[guildID][c] = struct{}{}
	}
}

func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, guildID := range c.guildIDs() {
		if subscribers, ok := h.byGuild[guildID]; ok {
			delete(subscribers, c)
			if len(subscribers) == 0 {
				delete(h.byGuild, guildID)
			}
		}
	}
}

// Publish реализует chat.Publisher.
func (h *Hub) Publish(ctx context.Context, guildID string, event chat.Event) {
	h.mu.RLock()
	subscribers := make([]*Client, 0, len(h.byGuild[guildID]))
	for c := range h.byGuild[guildID] {
		subscribers = append(subscribers, c)
	}
	h.mu.RUnlock()

	for _, c := range subscribers {
		// Событие канала уходит только тем, кто этот канал видит.
		// Иначе приватный канал утекал бы через сокет.
		if event.ChannelID != "" && h.filter != nil {
			if !h.filter.CanView(ctx, guildID, c.userID, event.ChannelID) {
				continue
			}
		}
		c.dispatch(event.Type, event.Data)
	}
}

// Online — сколько соединений сейчас в гильдии. Пригодится для presence.
func (h *Hub) Online(guildID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.byGuild[guildID])
}
