package outbox

import (
	"context"
	"github.com/andreunix/devengine/events"
	"github.com/andreunix/devengine/telemetry"
	testpostgres "github.com/andreunix/devengine/testutil/postgres"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type lostMeter struct {
	mu       sync.Mutex
	statuses []string
}

func (m *lostMeter) Int64Counter(string) telemetry.Counter { return lostCounter{m} }
func (m *lostMeter) Float64Histogram(string) telemetry.Histogram {
	return telemetry.NoopMeter.Float64Histogram("")
}

type lostCounter struct{ m *lostMeter }

func (c lostCounter) Add(_ context.Context, _ int64, a map[string]string) {
	c.m.mu.Lock()
	defer c.m.mu.Unlock()
	c.m.statuses = append(c.m.statuses, a["status"])
}
func TestProcessBatchRecordsClaimLost(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()
	if _, e := db.Pool().Exec(ctx, Schema); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Pool().Exec(ctx, `INSERT INTO outbox_messages(id,event_type,aggregate_id,aggregate_type,occurred_at)VALUES('lost','event','','',NOW())`); e != nil {
		t.Fatal(e)
	}
	r := events.NewRegistry()
	r.Register(events.HandlerFunc{Type: "event", HandleF: func(context.Context, events.Event) error {
		_, e := db.Pool().Exec(ctx, `UPDATE outbox_messages SET claim_token='new-owner' WHERE id='lost'`)
		return e
	}})
	m := &lostMeter{}
	relay := &Relay{Pool: db.Pool(), Registry: r, Meter: m, Tracer: telemetry.NoopTracer}
	if e := relay.processBatch(ctx, slog.Default()); e != nil {
		t.Fatal(e)
	}
	if len(m.statuses) != 1 || m.statuses[0] != "claim_lost" {
		t.Fatalf("statuses=%v", m.statuses)
	}
}

func TestProcessBatchRenewsLeaseWhileHandlerRuns(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()
	if _, err := db.Pool().Exec(ctx, Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx, `INSERT INTO outbox_messages(id,event_type,aggregate_id,aggregate_type,occurred_at) VALUES('renew','event','','',NOW())`); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	registry := events.NewRegistry()
	registry.Register(events.HandlerFunc{Type: "event", HandleF: func(context.Context, events.Event) error {
		close(started)
		<-release
		return nil
	}})
	config := RelayConfig{LeaseDuration: 80 * time.Millisecond, LeaseRenewalInterval: 10 * time.Millisecond}
	relay := &Relay{Pool: db.Pool(), Registry: registry, Config: config, Tracer: telemetry.NoopTracer, Meter: telemetry.NoopMeter}
	done := make(chan error, 1)
	go func() { done <- relay.processBatch(ctx, slog.Default()) }()
	<-started

	time.Sleep(150 * time.Millisecond)
	var secondCalls atomic.Int32
	secondRegistry := events.NewRegistry()
	secondRegistry.Register(events.HandlerFunc{Type: "event", HandleF: func(context.Context, events.Event) error {
		secondCalls.Add(1)
		return nil
	}})
	second := &Relay{Pool: db.Pool(), Registry: secondRegistry, Config: config, Tracer: telemetry.NoopTracer, Meter: telemetry.NoopMeter}
	if err := second.processBatch(ctx, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if got := secondCalls.Load(); got != 0 {
		t.Fatalf("second relay processed %d messages, want 0", got)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestProcessBatchCancelsHandlerAfterLostLease(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()
	if _, err := db.Pool().Exec(ctx, Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx, `INSERT INTO outbox_messages(id,event_type,aggregate_id,aggregate_type,occurred_at) VALUES('lost-renewal','event','','',NOW())`); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	cancelled := make(chan struct{})
	registry := events.NewRegistry()
	registry.Register(events.HandlerFunc{Type: "event", HandleF: func(handlerCtx context.Context, _ events.Event) error {
		close(started)
		<-handlerCtx.Done()
		close(cancelled)
		return handlerCtx.Err()
	}})
	relay := &Relay{Pool: db.Pool(), Registry: registry, Config: RelayConfig{LeaseDuration: time.Second, LeaseRenewalInterval: 10 * time.Millisecond}, Tracer: telemetry.NoopTracer, Meter: telemetry.NoopMeter}
	done := make(chan error, 1)
	go func() { done <- relay.processBatch(ctx, slog.Default()) }()
	<-started
	if _, err := db.Pool().Exec(ctx, `UPDATE outbox_messages SET claim_token = 'other-owner' WHERE id = 'lost-renewal'`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("handler was not cancelled after lease ownership was lost")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestLeaseRenewalDoesNotReviveExpiredClaim(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()
	if _, err := db.Pool().Exec(ctx, Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx, `
		INSERT INTO outbox_messages(id, event_type, aggregate_id, aggregate_type, occurred_at, locked_until, claim_token)
		VALUES ('expired-renewal', 'event', '', '', NOW(), NOW() - interval '1 second', 'owner')
	`); err != nil {
		t.Fatal(err)
	}

	relay := &Relay{Pool: db.Pool(), Config: RelayConfig{LeaseRenewalInterval: 5 * time.Millisecond}}
	handlerCtx, stop := relay.startLeaseRenewal(ctx, slog.Default(), "outbox_messages", outboxRow{id: "expired-renewal", eventType: "event", claimToken: "owner"}, time.Second)
	defer stop()
	select {
	case <-handlerCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("expired claim was renewed instead of losing ownership")
	}
}

func TestLeaseRenewalStopsAfterStop(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()
	if _, err := db.Pool().Exec(ctx, Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx, `
		INSERT INTO outbox_messages(id, event_type, aggregate_id, aggregate_type, occurred_at, locked_until, claim_token)
		VALUES ('stop-renewal', 'event', '', '', NOW(), NOW() + interval '30 milliseconds', 'owner')
	`); err != nil {
		t.Fatal(err)
	}

	relay := &Relay{Pool: db.Pool(), Config: RelayConfig{LeaseRenewalInterval: 5 * time.Millisecond}}
	_, stop := relay.startLeaseRenewal(ctx, slog.Default(), "outbox_messages", outboxRow{id: "stop-renewal", eventType: "event", claimToken: "owner"}, 200*time.Millisecond)
	var renewedUntil time.Time
	deadline := time.After(time.Second)
	for {
		if err := db.Pool().QueryRow(ctx, `SELECT locked_until FROM outbox_messages WHERE id = 'stop-renewal'`).Scan(&renewedUntil); err != nil {
			t.Fatal(err)
		}
		if renewedUntil.After(time.Now().Add(100 * time.Millisecond)) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("lease was not renewed")
		case <-time.After(5 * time.Millisecond):
		}
	}
	stop()

	var afterStop time.Time
	if err := db.Pool().QueryRow(ctx, `SELECT locked_until FROM outbox_messages WHERE id = 'stop-renewal'`).Scan(&afterStop); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	var later time.Time
	if err := db.Pool().QueryRow(ctx, `SELECT locked_until FROM outbox_messages WHERE id = 'stop-renewal'`).Scan(&later); err != nil {
		t.Fatal(err)
	}
	if !later.Equal(afterStop) {
		t.Fatalf("lease changed after renewal stopped: got %s, want %s", later, afterStop)
	}
}
