package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nemanc123/discord_clone/internal/perms"
)

type fakeProvider struct {
	perm  perms.Permission
	err   error
	calls int
}

func (f *fakeProvider) Permissions(context.Context, string, string, string) (perms.Permission, error) {
	f.calls++
	return f.perm, f.err
}

func TestFilterAllowsWhenPermissionPresent(t *testing.T) {
	provider := &fakeProvider{perm: perms.ViewChannel | perms.SendMessages}
	filter := NewCachedFilter(provider, time.Minute)

	if !filter.CanView(context.Background(), "g", "u", "c") {
		t.Fatal("право ViewChannel есть — должно быть разрешено")
	}
}

func TestFilterDeniesWithoutViewChannel(t *testing.T) {
	provider := &fakeProvider{perm: perms.SendMessages}
	filter := NewCachedFilter(provider, time.Minute)

	if filter.CanView(context.Background(), "g", "u", "c") {
		t.Fatal("без ViewChannel событие отдавать нельзя")
	}
}

func TestFilterDeniesOnProviderError(t *testing.T) {
	provider := &fakeProvider{err: errors.New("база недоступна")}
	filter := NewCachedFilter(provider, time.Minute)

	if filter.CanView(context.Background(), "g", "u", "c") {
		t.Fatal("при ошибке вычисления прав доступа быть не должно")
	}
}

func TestFilterCachesResult(t *testing.T) {
	provider := &fakeProvider{perm: perms.ViewChannel}
	filter := NewCachedFilter(provider, time.Minute)

	for i := 0; i < 5; i++ {
		if !filter.CanView(context.Background(), "g", "u", "c") {
			t.Fatal("должно быть разрешено")
		}
	}

	if provider.calls != 1 {
		t.Fatalf("провайдер вызван %d раз, ожидали 1 (кэш не работает)", provider.calls)
	}
}

func TestFilterRefreshesAfterTTL(t *testing.T) {
	now := time.Now()
	provider := &fakeProvider{perm: perms.ViewChannel}
	filter := NewCachedFilter(provider, 10*time.Second)
	filter.now = func() time.Time { return now }

	filter.CanView(context.Background(), "g", "u", "c")
	filter.CanView(context.Background(), "g", "u", "c")

	if provider.calls != 1 {
		t.Fatalf("в пределах TTL ожидали 1 вызов, получили %d", provider.calls)
	}

	now = now.Add(11 * time.Second)
	provider.perm = 0

	if filter.CanView(context.Background(), "g", "u", "c") {
		t.Fatal("после истечения TTL права должны пересчитаться")
	}
	if provider.calls != 2 {
		t.Fatalf("после TTL ожидали 2 вызова, получили %d", provider.calls)
	}
}

func TestFilterCachesPerChannel(t *testing.T) {
	provider := &fakeProvider{perm: perms.ViewChannel}
	filter := NewCachedFilter(provider, time.Minute)

	filter.CanView(context.Background(), "g", "u", "c1")
	filter.CanView(context.Background(), "g", "u", "c2")

	if provider.calls != 2 {
		t.Fatalf("разные каналы должны считаться отдельно, вызовов %d", provider.calls)
	}
}
