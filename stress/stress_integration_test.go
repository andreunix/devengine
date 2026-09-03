package stress_test

import (
	"context"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andreunix/devengine/events"
	"github.com/andreunix/devengine/jobs"
	"github.com/andreunix/devengine/outbox"
	devpostgres "github.com/andreunix/devengine/postgres"
	"github.com/andreunix/devengine/telemetry"
	testpostgres "github.com/andreunix/devengine/testutil/postgres"
)

func TestBacklog(t *testing.T) {
	countText := os.Getenv("DEVENGINE_STRESS_COUNT")
	if countText == "" {
		t.Skip("DEVENGINE_STRESS_COUNT is not set")
	}
	count, err := strconv.Atoi(countText)
	if err != nil || count < 1 || count > 1_000_000 {
		t.Fatalf("invalid DEVENGINE_STRESS_COUNT %q", countText)
	}
	t.Run("jobs", func(t *testing.T) { stressJobs(t, count) })
	t.Run("outbox", func(t *testing.T) { stressOutbox(t, count) })
}

func stressJobs(t *testing.T, count int) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if _, err := db.Pool().Exec(ctx, jobs.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx, `
		INSERT INTO devengine_jobs (id, name, payload)
		SELECT 'stress-job-' || value, 'stress', jsonb_build_object('id', 'stress-job-' || value)
		FROM generate_series(1, $1) AS value
	`, count); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int64
	var duplicates atomic.Int64
	var seen sync.Map
	registry := jobs.NewRegistry()
	if err := registry.Register("stress", jobs.HandlerFunc(func(_ context.Context, payload []byte) error {
		key := string(payload)
		if _, loaded := seen.LoadOrStore(key, struct{}{}); loaded {
			duplicates.Add(1)
		}
		calls.Add(1)
		return nil
	})); err != nil {
		t.Fatal(err)
	}

	runCtx, stop := context.WithCancel(ctx)
	done := startJobWorkers(runCtx, db, registry, 8)
	waitForCount(t, ctx, func() (int, error) {
		var remaining int
		err := db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM devengine_jobs`).Scan(&remaining)
		return remaining, err
	})
	stop()
	waitWorkers(t, done, 8)
	if got := calls.Load(); got != int64(count) {
		t.Fatalf("job deliveries = %d, want %d", got, count)
	}
	if got := duplicates.Load(); got != 0 {
		t.Fatalf("duplicate job deliveries = %d", got)
	}
}

func stressOutbox(t *testing.T, count int) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if _, err := db.Pool().Exec(ctx, outbox.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx, `
		INSERT INTO outbox_messages (id, event_type, aggregate_id, aggregate_type, payload, occurred_at)
		SELECT 'stress-event-' || value, 'stress', '', '', '{}', NOW()
		FROM generate_series(1, $1) AS value
	`, count); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int64
	var duplicates atomic.Int64
	var seen sync.Map
	registry := events.NewRegistry()
	if err := registry.Register(events.HandlerFunc{Type: "stress", HandleF: func(_ context.Context, event events.Event) error {
		if _, loaded := seen.LoadOrStore(event.ID, struct{}{}); loaded {
			duplicates.Add(1)
		}
		calls.Add(1)
		return nil
	}}); err != nil {
		t.Fatal(err)
	}

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 8)
	for range 8 {
		relay := &outbox.Relay{
			Pool: db.Pool(), Registry: registry,
			Config: outbox.RelayConfig{BatchSize: 100, PollInterval: time.Millisecond},
			Tracer: telemetry.NoopTracer, Meter: telemetry.NoopMeter,
		}
		go func() { done <- relay.Run(runCtx) }()
	}
	waitForCount(t, ctx, func() (int, error) {
		var remaining int
		err := db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM outbox_messages WHERE processed_at IS NULL`).Scan(&remaining)
		return remaining, err
	})
	stop()
	waitWorkers(t, done, 8)
	if got := calls.Load(); got != int64(count) {
		t.Fatalf("event deliveries = %d, want %d", got, count)
	}
	if got := duplicates.Load(); got != 0 {
		t.Fatalf("duplicate event deliveries = %d", got)
	}
}

func startJobWorkers(ctx context.Context, db *devpostgres.DB, registry *jobs.Registry, count int) <-chan error {
	done := make(chan error, count)
	for range count {
		worker := &jobs.Worker{
			Pool: db.Pool(), Registry: registry,
			Config: jobs.WorkerConfig{BatchSize: 100, PollInterval: time.Millisecond},
			Tracer: telemetry.NoopTracer, Meter: telemetry.NoopMeter,
		}
		go func() { done <- worker.Run(ctx) }()
	}
	return done
}

func waitForCount(t *testing.T, ctx context.Context, read func() (int, error)) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		remaining, err := read()
		if err != nil {
			t.Fatal(err)
		}
		if remaining == 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("backlog did not drain: %d remaining: %v", remaining, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitWorkers(t *testing.T, done <-chan error, count int) {
	t.Helper()
	for range count {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("worker stopped with error: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("worker did not stop")
		}
	}
}
