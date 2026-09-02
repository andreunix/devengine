package health

import (
	"context"
	"net/http"
	"net/http/httptest"
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
