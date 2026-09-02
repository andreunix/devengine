package jobs

import (
	"context"
	"github.com/andreunix/devengine/telemetry"
	testpostgres "github.com/andreunix/devengine/testutil/postgres"
	"log/slog"
	"sync"
	"testing"
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
	r.Register("task", HandlerFunc(func(context.Context, []byte) error {
		_, e := db.Pool().Exec(ctx, `UPDATE devengine_jobs SET claim_token='new-owner' WHERE id='lost'`)
		return e
	}))
	m := &claimMeter{}
	w := &Worker{Pool: db.Pool(), Registry: r, Meter: m, Tracer: telemetry.NoopTracer}
	if e := w.processBatch(ctx, slog.Default()); e != nil {
		t.Fatal(e)
	}
	if len(m.statuses) != 1 || m.statuses[0] != "claim_lost" {
		t.Fatalf("statuses=%v", m.statuses)
	}
}
