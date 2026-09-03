package jobs

import (
	"context"
	"github.com/andreunix/devengine/telemetry"
	testpostgres "github.com/andreunix/devengine/testutil/postgres"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type claimMeter struct {
	mu       sync.Mutex
	statuses []string
}

func (m *claimMeter) Int64Counter(string) telemetry.Counter { return claimCounter{m} }
func (m *claimMeter) Float64Histogram(string) telemetry.Histogram {
	return telemetry.NoopMeter.Float64Histogram("")
}

type claimCounter struct{ m *claimMeter }

func (c claimCounter) Add(_ context.Context, _ int64, a map[string]string) {
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
	if _, e := db.Pool().Exec(ctx, `INSERT INTO devengine_jobs(id,name) VALUES('lost','task')`); e != nil {
		t.Fatal(e)
	}
	r := NewRegistry()
	if err := r.Register("task", HandlerFunc(func(context.Context, []byte) error {
		_, e := db.Pool().Exec(ctx, `UPDATE devengine_jobs SET claim_token='new-owner' WHERE id='lost'`)
		return e
	})); err != nil {
		t.Fatal(err)
	}
	m := &claimMeter{}
	w := &Worker{Pool: db.Pool(), Registry: r, Meter: m, Tracer: telemetry.NoopTracer}
	if e := w.processBatch(ctx, slog.Default()); e != nil {
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
	if _, err := db.Pool().Exec(ctx, `INSERT INTO devengine_jobs(id,name) VALUES('renew','task')`); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	registry := NewRegistry()
	if err := registry.Register("task", HandlerFunc(func(context.Context, []byte) error {
		close(started)
		<-release
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	config := WorkerConfig{LeaseDuration: 80 * time.Millisecond, LeaseRenewalInterval: 10 * time.Millisecond}
	worker := &Worker{Pool: db.Pool(), Registry: registry, Config: config, Tracer: telemetry.NoopTracer, Meter: telemetry.NoopMeter}
	done := make(chan error, 1)
	go func() { done <- worker.processBatch(ctx, slog.Default()) }()
	<-started

	// This is past the original lease. A second worker must not reclaim it.
	time.Sleep(150 * time.Millisecond)
	var secondCalls atomic.Int32
	secondRegistry := NewRegistry()
	if err := secondRegistry.Register("task", HandlerFunc(func(context.Context, []byte) error {
		secondCalls.Add(1)
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	second := &Worker{Pool: db.Pool(), Registry: secondRegistry, Config: config, Tracer: telemetry.NoopTracer, Meter: telemetry.NoopMeter}
	if err := second.processBatch(ctx, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if got := secondCalls.Load(); got != 0 {
		t.Fatalf("second worker processed %d jobs, want 0", got)
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
	if _, err := db.Pool().Exec(ctx, `INSERT INTO devengine_jobs(id,name) VALUES('lost-renewal','task')`); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	cancelled := make(chan struct{})
	registry := NewRegistry()
	if err := registry.Register("task", HandlerFunc(func(handlerCtx context.Context, _ []byte) error {
		close(started)
		<-handlerCtx.Done()
		close(cancelled)
		return handlerCtx.Err()
	})); err != nil {
		t.Fatal(err)
	}
	worker := &Worker{Pool: db.Pool(), Registry: registry, Config: WorkerConfig{LeaseDuration: time.Second, LeaseRenewalInterval: 10 * time.Millisecond}, Tracer: telemetry.NoopTracer, Meter: telemetry.NoopMeter}
	done := make(chan error, 1)
	go func() { done <- worker.processBatch(ctx, slog.Default()) }()
	<-started
	if _, err := db.Pool().Exec(ctx, `UPDATE devengine_jobs SET claim_token = 'other-owner' WHERE id = 'lost-renewal'`); err != nil {
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
		INSERT INTO devengine_jobs(id, name, locked_until, claim_token)
		VALUES ('expired-renewal', 'task', NOW() - interval '1 second', 'owner')
	`); err != nil {
		t.Fatal(err)
	}

	worker := &Worker{Pool: db.Pool(), Config: WorkerConfig{LeaseRenewalInterval: 5 * time.Millisecond}}
	handlerCtx, stop := worker.startLeaseRenewal(ctx, slog.Default(), jobRow{id: "expired-renewal", name: "task", claimToken: "owner"}, time.Second)
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
		INSERT INTO devengine_jobs(id, name, locked_until, claim_token)
		VALUES ('stop-renewal', 'task', NOW() + interval '1 second', 'owner')
	`); err != nil {
		t.Fatal(err)
	}

	worker := &Worker{Pool: db.Pool(), Config: WorkerConfig{LeaseRenewalInterval: 20 * time.Millisecond}}
	_, stop := worker.startLeaseRenewal(ctx, slog.Default(), jobRow{id: "stop-renewal", name: "task", claimToken: "owner"}, 2*time.Second)
	var renewedUntil time.Time
	deadline := time.After(2 * time.Second)
	for {
		if err := db.Pool().QueryRow(ctx, `SELECT locked_until FROM devengine_jobs WHERE id = 'stop-renewal'`).Scan(&renewedUntil); err != nil {
			t.Fatal(err)
		}
		if renewedUntil.After(time.Now().Add(time.Second)) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("lease was not renewed")
		case <-time.After(10 * time.Millisecond):
		}
	}
	stop()
	// pgx may return from a cancelled query before PostgreSQL finishes handling
	// the cancellation. Let that in-flight request settle before the assertion.
	time.Sleep(100 * time.Millisecond)

	var afterStop time.Time
	if err := db.Pool().QueryRow(ctx, `SELECT locked_until FROM devengine_jobs WHERE id = 'stop-renewal'`).Scan(&afterStop); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	var later time.Time
	if err := db.Pool().QueryRow(ctx, `SELECT locked_until FROM devengine_jobs WHERE id = 'stop-renewal'`).Scan(&later); err != nil {
		t.Fatal(err)
	}
	if !later.Equal(afterStop) {
		t.Fatalf("lease changed after renewal stopped: got %s, want %s", later, afterStop)
	}
}
