package outbox

import (
	"context"
	"log/slog"
	"testing"

	"github.com/andreunix/devengine/events"
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
	if _, err := db.Pool().Exec(ctx, `
		INSERT INTO outbox_messages (id, event_type, aggregate_id, aggregate_type, occurred_at)
		VALUES ('panic-event', 'panic', '', '', NOW())
	`); err != nil {
		t.Fatal(err)
	}
	registry := events.NewRegistry()
	if err := registry.Register(events.HandlerFunc{
		Type: "panic",
		HandleF: func(context.Context, events.Event) error {
			panic("handler panic")
		},
	}); err != nil {
		t.Fatal(err)
	}
	registry.Freeze()
	relay := &Relay{Pool: db.Pool(), Registry: registry, Tracer: telemetry.NoopTracer, Meter: telemetry.NoopMeter}

	if err := relay.processBatch(ctx, slog.Default()); err != nil {
		t.Fatal(err)
	}
	var attempts int
	var lastError string
	if err := db.Pool().QueryRow(ctx, `SELECT attempt, last_error FROM outbox_messages WHERE id = 'panic-event'`).Scan(&attempts, &lastError); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || lastError != "handler events.HandlerFunc: outbox: handler panic: handler panic" {
		t.Fatalf("attempts=%d last_error=%q", attempts, lastError)
	}
}
