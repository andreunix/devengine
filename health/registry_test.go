package health

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andreunix/devengine/telemetry"
)

func TestHandlerEnforcesTimeoutForCheckIgnoringContext(t *testing.T) {
	r := NewRegistry()
	r.SetTimeout(20 * time.Millisecond)
	r.Add("stuck", func(context.Context) error { select {} })
	start := time.Now()
	w := httptest.NewRecorder()
	r.Handler("svc").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", w.Code)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("handler blocked for %s", elapsed)
	}
}

func TestHandlerTimeoutForInformationalCheckDoesNotBlockReadiness(t *testing.T) {
	r := NewRegistry()
	r.SetTimeout(20 * time.Millisecond)
	r.AddInformational("stuck", func(context.Context) error { select {} })
	w := httptest.NewRecorder()
	r.Handler("svc").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandlerDoesNotMultiplyStuckChecks(t *testing.T) {
	r := NewRegistry()
	r.SetTimeout(10 * time.Millisecond)
	started := make(chan struct{}, 1)
	r.Add("stuck", func(context.Context) error { started <- struct{}{}; select {} })
	for range 20 {
		w := httptest.NewRecorder()
		r.Handler("svc").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatal(w.Code)
		}
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("check did not start")
	}
	select {
	case <-started:
		t.Fatal("stuck check started more than once")
	case <-time.After(30 * time.Millisecond):
	}
}

func TestCooperativeCheckRunsAgain(t *testing.T) {
	r := NewRegistry()
	var calls atomic.Int32
	r.Add("cooperative", func(context.Context) error { calls.Add(1); return nil })
	for range 2 {
		w := httptest.NewRecorder()
		r.Handler("svc").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if w.Code != http.StatusOK {
			t.Fatal(w.Code)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestHealthTelemetryRecordsDurationTimeoutPanicAndInflight(t *testing.T) {
	meter := newHealthTestMeter()
	r := NewRegistry()
	r.SetTelemetry(meter)
	r.SetTimeout(20 * time.Millisecond)
	r.Add("timeout", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	r.Add("panic", func(context.Context) error { panic("boom") })

	r.Handler("svc").ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if got := meter.counterValue("health_check_timeout_total"); got != 1 {
		t.Fatalf("timeouts = %d, want 1", got)
	}
	if got := meter.counterValue("health_check_panics_total"); got != 1 {
		t.Fatalf("panics = %d, want 1", got)
	}
	if got := meter.counterValue("health_check_inflight"); got != 0 {
		t.Fatalf("inflight = %d, want 0", got)
	}
	if got := meter.histogramCount("health_check_duration"); got != 2 {
		t.Fatalf("duration recordings = %d, want 2", got)
	}
}

func TestSnapshotAndTransitionOnlyLogs(t *testing.T) {
	var unhealthy atomic.Bool
	var logs bytes.Buffer
	r := NewRegistry()
	r.SetVerbose(true)
	r.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	r.Add("database", func(context.Context) error {
		if unhealthy.Load() {
			return errors.New("database unavailable")
		}
		return nil
	})
	handler := r.Handler("svc")
	probe := func() { handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/readyz", nil)) }

	probe()
	unhealthy.Store(true)
	probe()
	probe()
	unhealthy.Store(false)
	probe()

	snapshot := r.Snapshot()
	if got := snapshot["database"].Status; got != "ok" {
		t.Fatalf("snapshot status = %q", got)
	}
	output := logs.String()
	if got := strings.Count(output, "became unhealthy"); got != 1 {
		t.Fatalf("unhealthy logs = %d, want 1: %s", got, output)
	}
	if got := strings.Count(output, "recovered"); got != 1 {
		t.Fatalf("recovery logs = %d, want 1: %s", got, output)
	}
	if changed := r.Snapshot(); len(changed) != 1 {
		t.Fatalf("snapshot size = %d", len(changed))
	}
	delete(snapshot, "database")
	if len(r.Snapshot()) != 1 {
		t.Fatal("caller mutated registry snapshot")
	}
}

func TestRemovedCheckCannotReappearInSnapshot(t *testing.T) {
	r := NewRegistry()
	started := make(chan struct{})
	release := make(chan struct{})
	r.Add("database", func(context.Context) error {
		close(started)
		<-release
		return nil
	})
	done := make(chan struct{})
	go func() {
		r.Handler("svc").ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/readyz", nil))
		close(done)
	}()
	<-started
	r.Remove("database")
	close(release)
	<-done
	if _, exists := r.Snapshot()["database"]; exists {
		t.Fatal("removed check reappeared in snapshot")
	}
}

type healthTestMeter struct {
	mu         sync.Mutex
	counters   map[string]int64
	histograms map[string]int
}

func newHealthTestMeter() *healthTestMeter {
	return &healthTestMeter{counters: make(map[string]int64), histograms: make(map[string]int)}
}

func (m *healthTestMeter) Int64Counter(name string) telemetry.Counter {
	return healthTestCounter{meter: m, name: name}
}

func (m *healthTestMeter) Int64UpDownCounter(name string) telemetry.Counter {
	return healthTestCounter{meter: m, name: name}
}

func (m *healthTestMeter) Float64Histogram(name string) telemetry.Histogram {
	return healthTestHistogram{meter: m, name: name}
}

func (m *healthTestMeter) counterValue(name string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counters[name]
}

func (m *healthTestMeter) histogramCount(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.histograms[name]
}

type healthTestCounter struct {
	meter *healthTestMeter
	name  string
}

func (c healthTestCounter) Add(_ context.Context, int64Value int64, _ map[string]string) {
	c.meter.mu.Lock()
	c.meter.counters[c.name] += int64Value
	c.meter.mu.Unlock()
}

type healthTestHistogram struct {
	meter *healthTestMeter
	name  string
}

func (h healthTestHistogram) Record(context.Context, float64, map[string]string) {
	h.meter.mu.Lock()
	h.meter.histograms[h.name]++
	h.meter.mu.Unlock()
}
