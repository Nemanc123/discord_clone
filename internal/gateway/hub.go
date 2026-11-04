// Package gateway — долгоживущие WebSocket-соединения.
//
// Хаб хранит подписки «гильдия → сессии» и рассылает события. Важно, что
// подписка привязана к СЕССИИ, а не к соединению: пока клиент переподключается,
// события продолжают копиться в буфере сессии, и RESUME их догоняет.
package gateway

import (
	"context"
	"sync"

	"github.com/Nemanc123/discord_clone/internal/chat"
)

const (
	OpDispatch       = 0
	OpHeartbeat      = 1
	OpIdentify       = 2
	OpResume         = 6
	OpInvalidSession = 9
	OpHello          = 10
	OpHeartbeatAck   = 11
)

// PermissionFilter проверяет, может ли пользователь видеть канал.
// Интерфейс, а не конкретный сервис: хаб не должен знать про БД.
type PermissionFilter interface {
	CanView(ctx context.Context, guildID, userID, channelID string) bool
}

type Hub struct {
	mu      sync.RWMutex
	byGuild map[string]map[*Session]struct{}
	clients map[*Session]*Client // отсутствие = офлайн
	online  map[string]int       // userID → число активных соединений
	filter  PermissionFilter
}

func NewHub() *Hub {
	return &Hub{
		byGuild: make(map[string]map[*Session]struct{}),
		clients: make(map[*Session]*Client),
		online:  make(map[string]int),
	}
}

// SetFilter включает проверку прав при рассылке. Без фильтра события
// получают все подписчики гильдии.
func (h *Hub) SetFilter(f PermissionFilter) {
	h.filter = f
}

// Attach связывает сессию с активным соединением.
// Возвращает true, если пользователь только что появился в сети.
func (h *Hub) Attach(session *Session, client *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, guildID := range session.Guilds {
		if h.byGuild[guildID] == nil {
			h.byGuild[guildID] = make(map[*Session]struct{})
		}
		h.byGuild[guildID][session] = struct{}{}
	}
	h.clients[session] = client

	becameOnline := h.online[session.UserID] == 0
	h.online[session.UserID]++
	return becameOnline
}

// Detach отвязывает соединение, но оставляет сессию подписанной.
// Возвращает true, если это было последнее соединение пользователя.
func (h *Hub) Detach(session *Session) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	_, wasOnline := h.clients[session]
	delete(h.clients, session)
	if !wasOnline {
		return false
	}

	h.online[session.UserID]--
	if h.online[session.UserID] > 0 {
		return false
	}

	delete(h.online, session.UserID)
	return true
}

// Drop полностью убирает сессию (истекла или клиент вышел).
func (h *Hub) Drop(session *Session) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, wasOnline := h.clients[session]; wasOnline {
		h.online[session.UserID]--
		if h.online[session.UserID] <= 0 {
			delete(h.online, session.UserID)
		}
	}

	delete(h.clients, session)
	for _, guildID := range session.Guilds {
		if subscribers, ok := h.byGuild[guildID]; ok {
			delete(subscribers, session)
			if len(subscribers) == 0 {
				delete(h.byGuild, guildID)
			}
		}
	}
}

// Broadcast рассылает событие уровня гильдии — например, смену статуса.
// Привязки к каналу нет, поэтому фильтр по правам не применяется.
func (h *Hub) Broadcast(ctx context.Context, guildID, eventType string, data any) {
	h.Publish(ctx, guildID, chat.Event{Type: eventType, Data: data})
}

// OnlineUsers — кто из участников гильдии сейчас в сети.
func (h *Hub) OnlineUsers(guildID string) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	seen := make(map[string]struct{})
	for session := range h.byGuild[guildID] {
		if _, online := h.clients[session]; online {
			seen[session.UserID] = struct{}{}
		}
	}

	out := make([]string, 0, len(seen))
	for userID := range seen {
		out = append(out, userID)
	}
	return out
}

// Publish реализует chat.Publisher.
//
// Событие записывается в буфер КАЖДОЙ подписанной сессии — даже если она
// сейчас без соединения. Именно это делает RESUME осмысленным.
func (h *Hub) Publish(ctx context.Context, guildID string, event chat.Event) {
	h.mu.RLock()
	sessions := make([]*Session, 0, len(h.byGuild[guildID]))
	for session := range h.byGuild[guildID] {
		sessions = append(sessions, session)
	}
	h.mu.RUnlock()

	for _, session := range sessions {
		if event.ChannelID != "" && h.filter != nil {
			if !h.filter.CanView(ctx, guildID, session.UserID, event.ChannelID) {
				continue
			}
		}

		payload, err := session.appendDispatch(event.Type, event.Data)
		if err != nil {
			continue
		}

		h.mu.RLock()
		client, online := h.clients[session]
		h.mu.RUnlock()

		if online {
			client.push(payload)
		}
	}
}

// Online — сколько сессий гильдии сейчас с активным соединением.
func (h *Hub) Online(guildID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	count := 0
	for session := range h.byGuild[guildID] {
		if _, ok := h.clients[session]; ok {
			count++
		}
	}
	return count
}
