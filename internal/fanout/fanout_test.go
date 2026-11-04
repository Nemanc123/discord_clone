package fanout_test

// Проверка межузловой доставки требует настоящего Redis:
// без него тесты пропускаются.

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Nemanc123/discord_clone/internal/chat"
	"github.com/Nemanc123/discord_clone/internal/fanout"
)

type recorder struct {
	mu     sync.Mutex
	events []chat.Event
}

func (r *recorder) Publish(_ context.Context, _ string, event chat.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func newClient(t *testing.T) *redis.Client {
	t.Helper()

	raw := os.Getenv("REDIS_URL")
	if raw == "" {
		raw = "redis://localhost:6379/0"
	}
	opts, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatalf("разбор REDIS_URL: %v", err)
	}

	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		t.Skip("Redis недоступен — тесты fan-out пропущены")
	}

	t.Cleanup(func() { _ = client.Close() })
	return client
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func TestEventReachesOtherNode(t *testing.T) {
	client := newClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	localA := &recorder{}
	localB := &recorder{}

	publisherA := fanout.NewPublisher(localA, client, "node-a")
	subscriberB := fanout.NewSubscriber(localB, client, "node-b")
	go subscriberB.Run(ctx)

	// Даём подписке встать.
	time.Sleep(300 * time.Millisecond)

	publisherA.Publish(ctx, "guild-test-1", chat.Event{Type: "MESSAGE_CREATE", ChannelID: "chan-1"})

	if localA.count() != 1 {
		t.Fatalf("локальный узел получил %d событий, ожидали 1", localA.count())
	}
	if !waitFor(t, 3*time.Second, func() bool { return localB.count() == 1 }) {
		t.Fatalf("другой узел получил %d событий, ожидали 1", localB.count())
	}
}

func TestNodeIgnoresOwnEvents(t *testing.T) {
	client := newClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	local := &recorder{}
	nodeID := "node-self"

	publisher := fanout.NewPublisher(local, client, nodeID)
	subscriber := fanout.NewSubscriber(local, client, nodeID)
	go subscriber.Run(ctx)

	time.Sleep(300 * time.Millisecond)

	publisher.Publish(ctx, "guild-test-2", chat.Event{Type: "MESSAGE_CREATE", ChannelID: "chan-2"})

	// Даём время на возможный дубль.
	time.Sleep(500 * time.Millisecond)

	if local.count() != 1 {
		t.Fatalf("узел получил %d событий, ожидали ровно 1 (без дубля из Redis)", local.count())
	}
}

func TestDifferentGuildsAreIsolated(t *testing.T) {
	client := newClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	localA := &recorder{}
	localB := &recorder{}

	publisher := fanout.NewPublisher(localA, client, "node-a")
	go fanout.NewSubscriber(localB, client, "node-b").Run(ctx)

	time.Sleep(300 * time.Millisecond)

	publisher.Publish(ctx, "guild-x", chat.Event{Type: "MESSAGE_CREATE", ChannelID: "c1"})
	publisher.Publish(ctx, "guild-y", chat.Event{Type: "MESSAGE_CREATE", ChannelID: "c2"})

	if !waitFor(t, 3*time.Second, func() bool { return localB.count() == 2 }) {
		t.Fatalf("узел B получил %d событий, ожидали 2", localB.count())
	}
}
