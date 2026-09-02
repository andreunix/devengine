package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
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
