package outbox_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/andreunix/devengine/events"
	"github.com/andreunix/devengine/outbox"
	"github.com/andreunix/devengine/telemetry"
	testpostgres "github.com/andreunix/devengine/testutil/postgres"
)

func TestConcurrentRelaysUseSkipLockedWithoutDuplicateDelivery(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()
	if _, err := db.Pool().Exec(ctx, outbox.Schema); err != nil {
		t.Fatal(err)
	}
	const eventCount = 16
	for i := range eventCount {
		id := fmt.Sprintf("concurrent-%02d", i)
		if _, err := db.Pool().Exec(ctx, `
			INSERT INTO outbox_messages
				(id, event_type, aggregate_id, aggregate_type, payload, occurred_at)
			VALUES ($1, 'concurrent', '', '', '{}', NOW())
		`, id); err != nil {
			t.Fatal(err)
		}
	}

	entered := make(chan struct{}, eventCount)
	release := make(chan struct{})
	var releaseOnce sync.Once
	seen := make(map[string]int)
	var seenMu sync.Mutex
	registry := events.NewRegistry()
	if err := registry.Register(events.HandlerFunc{
		Type: "concurrent",
		HandleF: func(_ context.Context, event events.Event) error {
			seenMu.Lock()
			seen[event.ID]++
			seenMu.Unlock()
			entered <- struct{}{}
			<-release
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	registry.Freeze()

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 4)
	for range 4 {
		relay := &outbox.Relay{
			Pool: db.Pool(), Registry: registry,
			Config: outbox.RelayConfig{BatchSize: 1, PollInterval: time.Millisecond},
			Tracer: telemetry.NoopTracer, Meter: telemetry.NoopMeter,
		}
		go func() { done <- relay.Run(runCtx) }()
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		cancel()
		for range 4 {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("concurrent outbox relay did not stop")
			}
		}
	})

	for range 4 {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("relays did not process messages concurrently")
		}
	}
	releaseOnce.Do(func() { close(release) })

	deadline := time.After(3 * time.Second)
	for {
		var remaining int
		if err := db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM outbox_messages WHERE processed_at IS NULL`).Scan(&remaining); err != nil {
			t.Fatal(err)
		}
		if remaining == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("%d messages remained after concurrent processing", remaining)
		case <-time.After(5 * time.Millisecond):
		}
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	if len(seen) != eventCount {
		t.Fatalf("delivered %d unique messages, want %d", len(seen), eventCount)
	}
	for id, calls := range seen {
		if calls != 1 {
			t.Fatalf("message %s delivered %d times", id, calls)
		}
	}
}
