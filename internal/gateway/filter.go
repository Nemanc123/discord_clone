package gateway

import (
	"context"
	"sync"
	"time"

	"github.com/Nemanc123/discord_clone/internal/perms"
)

// PermissionsProvider — источник прав. guild.Service удовлетворяет интерфейсу.
type PermissionsProvider interface {
	Permissions(ctx context.Context, guildID, userID, channelID string) (perms.Permission, error)
}

type cacheEntry struct {
	allowed bool
	expires time.Time
}

// CachedFilter проверяет VIEW_CHANNEL с кэшем.
//
// Без кэша каждое сообщение стоило бы по запросу в БД на каждого подписчика:
// 50 участников гильдии × N сообщений в секунду — это сразу тысячи запросов.
// TTL 10 секунд означает, что смена прав применяется к рассылке с задержкой
// до 10 секунд. Для текстовых событий это приемлемо; для голоса права
// проверяются в момент выдачи токена LiveKit.
type CachedFilter struct {
	provider PermissionsProvider
	ttl      time.Duration
	now      func() time.Time

	mu    sync.RWMutex
	cache map[string]cacheEntry
}

func NewCachedFilter(provider PermissionsProvider, ttl time.Duration) *CachedFilter {
	return &CachedFilter{
		provider: provider,
		ttl:      ttl,
		now:      time.Now,
		cache:    make(map[string]cacheEntry),
	}
}

func (f *CachedFilter) CanView(ctx context.Context, guildID, userID, channelID string) bool {
	key := guildID + "|" + userID + "|" + channelID

	f.mu.RLock()
	entry, ok := f.cache[key]
	f.mu.RUnlock()
	if ok && f.now().Before(entry.expires) {
		return entry.allowed
	}

	p, err := f.provider.Permissions(ctx, guildID, userID, channelID)
	if err != nil {
		// Не смогли посчитать права — не отдаём событие. Ошибка в сторону
		// «не показать» безопаснее, чем утечка приватного канала.
		return false
	}

	allowed := p.Has(perms.ViewChannel)

	f.mu.Lock()
	if len(f.cache) > 10_000 {
		f.cleanupLocked()
	}
	f.cache[key] = cacheEntry{allowed: allowed, expires: f.now().Add(f.ttl)}
	f.mu.Unlock()

	return allowed
}

func (f *CachedFilter) cleanupLocked() {
	now := f.now()
	for key, entry := range f.cache {
		if now.After(entry.expires) {
			delete(f.cache, key)
		}
	}
}
