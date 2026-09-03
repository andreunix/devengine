package jobs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/andreunix/devengine/jobs"
	"github.com/andreunix/devengine/telemetry"
	testpostgres "github.com/andreunix/devengine/testutil/postgres"
)

func TestConcurrentWorkersUseSkipLockedWithoutDuplicateDelivery(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()
	if _, err := db.Pool().Exec(ctx, jobs.Schema); err != nil {
		t.Fatal(err)
	}
	const jobCount = 16
	for i := range jobCount {
		id := fmt.Sprintf("concurrent-%02d", i)
		if _, err := db.Pool().Exec(ctx, `
			INSERT INTO devengine_jobs (id, name, payload) VALUES ($1, 'concurrent', $2)
		`, id, fmt.Sprintf(`{"id":%q}`, id)); err != nil {
			t.Fatal(err)
		}
	}

	entered := make(chan struct{}, jobCount)
	release := make(chan struct{})
	var releaseOnce sync.Once
	seen := make(map[string]int)
	var seenMu sync.Mutex
	registry := jobs.NewRegistry()
	if err := registry.Register("concurrent", jobs.HandlerFunc(func(_ context.Context, payload []byte) error {
		var body struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(payload, &body); err != nil {
			return err
		}
		seenMu.Lock()
		seen[body.ID]++
		seenMu.Unlock()
		entered <- struct{}{}
		<-release
		return nil
	})); err != nil {
		t.Fatal(err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 4)
	for range 4 {
		worker := &jobs.Worker{
			Pool: db.Pool(), Registry: registry,
			Config: jobs.WorkerConfig{BatchSize: 1, PollInterval: time.Millisecond},
			Tracer: telemetry.NoopTracer, Meter: telemetry.NoopMeter,
		}
		go func() { done <- worker.Run(runCtx) }()
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		cancel()
		for range 4 {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("concurrent jobs worker did not stop")
			}
		}
	})

	for range 4 {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("workers did not process jobs concurrently")
		}
	}
	releaseOnce.Do(func() { close(release) })

	deadline := time.After(3 * time.Second)
	for {
		var remaining int
		if err := db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM devengine_jobs`).Scan(&remaining); err != nil {
			t.Fatal(err)
		}
		if remaining == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("%d jobs remained after concurrent processing", remaining)
		case <-time.After(5 * time.Millisecond):
		}
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	if len(seen) != jobCount {
		t.Fatalf("delivered %d unique jobs, want %d", len(seen), jobCount)
	}
	for id, calls := range seen {
		if calls != 1 {
			t.Fatalf("job %s delivered %d times", id, calls)
		}
	}
}
