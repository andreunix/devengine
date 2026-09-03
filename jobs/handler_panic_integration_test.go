package jobs

import (
	"context"
	"log/slog"
	"testing"

	"github.com/andreunix/devengine/telemetry"
	testpostgres "github.com/andreunix/devengine/testutil/postgres"
)

func TestProcessBatchRecordsHandlerPanicAsFailure(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := db.Pool().Exec(ctx, Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx, `INSERT INTO devengine_jobs (id, name) VALUES ('panic-job', 'panic')`); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := registry.Register("panic", HandlerFunc(func(context.Context, []byte) error {
		panic("handler panic")
	})); err != nil {
		t.Fatal(err)
	}
	worker := &Worker{Pool: db.Pool(), Registry: registry, Tracer: telemetry.NoopTracer, Meter: telemetry.NoopMeter}

	if err := worker.processBatch(ctx, slog.Default()); err != nil {
		t.Fatal(err)
	}
	var attempts int
	var lastError string
	if err := db.Pool().QueryRow(ctx, `SELECT attempt, last_error FROM devengine_jobs WHERE id = 'panic-job'`).Scan(&attempts, &lastError); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || lastError != "jobs: handler panic: handler panic" {
		t.Fatalf("attempts=%d last_error=%q", attempts, lastError)
	}
}
