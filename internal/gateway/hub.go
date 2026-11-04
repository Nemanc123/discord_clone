// Package gateway — долгоживущие WebSocket-соединения.
//
// Хаб хранит подписки «гильдия → клиенты» и рассылает события.
// Сейчас он внутрипроцессный: хватает для одного экземпляра сервера.
// При переходе на несколько узлов публикация переедет в Redis pub/sub —
// интерфейс chat.Publisher для этого и выделен.
package gateway

import (
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

type Hub struct {
	mu      sync.RWMutex
	byGuild map[string]map[*Client]struct{}
}

func NewHub() *Hub {
	return &Hub{byGuild: make(map[string]map[*Client]struct{})}
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
func (h *Hub) Publish(guildID string, event chat.Event) {
	h.mu.RLock()
	subscribers := make([]*Client, 0, len(h.byGuild[guildID]))
	for c := range h.byGuild[guildID] {
		subscribers = append(subscribers, c)
	}
	h.mu.RUnlock()

	for _, c := range subscribers {
		c.dispatch(event.Type, event.Data)
	}
}

// Online — сколько соединений сейчас в гильдии. Пригодится для presence.
func (h *Hub) Online(guildID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.byGuild[guildID])
}
