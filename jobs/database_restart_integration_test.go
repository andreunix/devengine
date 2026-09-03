package jobs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andreunix/devengine/jobs"
	"github.com/andreunix/devengine/telemetry"
	testpostgres "github.com/andreunix/devengine/testutil/postgres"
)

// TestWorkerRecoversAfterDatabaseRestart coordinates with the VPS resilience
// workflow through marker files. It is skipped during normal test runs.
func TestWorkerRecoversAfterDatabaseRestart(t *testing.T) {
	controlDir := os.Getenv("DEVENGINE_CHAOS_CONTROL_DIR")
	if controlDir == "" {
		t.Skip("DEVENGINE_CHAOS_CONTROL_DIR is not set")
	}
	if err := os.MkdirAll(controlDir, 0o700); err != nil {
		t.Fatal(err)
	}
	startedFile := filepath.Join(controlDir, "handler-started")
	releaseFile := filepath.Join(controlDir, "release-handler")

	db := testpostgres.NewIsolatedDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := db.Pool().Exec(ctx, jobs.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx, `INSERT INTO devengine_jobs (id, name, payload) VALUES ('restart-job', 'restart', '{}')`); err != nil {
		t.Fatal(err)
	}

	registry := jobs.NewRegistry()
	if err := registry.Register("restart", jobs.HandlerFunc(func(handlerCtx context.Context, _ []byte) error {
		if err := os.WriteFile(startedFile, []byte("started\n"), 0o600); err != nil {
			return err
		}
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := os.Stat(releaseFile); err == nil {
				return nil
			}
			select {
			case <-handlerCtx.Done():
				return handlerCtx.Err()
			case <-ticker.C:
			}
		}
	})); err != nil {
		t.Fatal(err)
	}

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	worker := &jobs.Worker{
		Pool: db.Pool(), Registry: registry,
		Config: jobs.WorkerConfig{
			BatchSize:            1,
			PollInterval:         50 * time.Millisecond,
			LeaseDuration:        30 * time.Second,
			LeaseRenewalInterval: 5 * time.Second,
		},
		Tracer: telemetry.NoopTracer, Meter: telemetry.NoopMeter,
	}
	go func() { done <- worker.Run(runCtx) }()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var remaining int
		err := db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM devengine_jobs`).Scan(&remaining)
		if err == nil && remaining == 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("job was not durably completed after database restart: %v", ctx.Err())
		case <-ticker.C:
		}
	}
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not stop")
	}
}
