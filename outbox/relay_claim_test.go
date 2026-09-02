package outbox

import (
	"context"
	"github.com/andreunix/devengine/events"
	"github.com/andreunix/devengine/telemetry"
	testpostgres "github.com/andreunix/devengine/testutil/postgres"
	"log/slog"
	"sync"
	"testing"
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
