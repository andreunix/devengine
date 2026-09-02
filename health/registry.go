package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Criticality determines whether a failing check blocks the readiness response.
type Criticality int

const (
	// Critical checks must pass for the service to be considered ready.
	// A single failing critical check causes /readyz to return 503.
	Critical Criticality = iota
	// Informational checks are included in the readiness response for
	// observability, but their failure does not block readiness.
	Informational
)

// Check is a function that verifies a dependency is available.
type Check func(context.Context) error

type entry struct {
	check       Check
	criticality Criticality
}

// Registry holds named health checks and exposes them as HTTP handlers.
type Registry struct {
	mu      sync.RWMutex
	checks  map[string]entry
	timeout time.Duration
	verbose bool
}

// NewRegistry creates a Registry with a 2-second per-check timeout.
func NewRegistry() *Registry {
	return &Registry{
		checks:  make(map[string]entry),
		timeout: 2 * time.Second,
	}
}

// Add registers a critical health check under name.
func (r *Registry) Add(name string, check Check) {
	r.AddWithCriticality(name, check, Critical)
}

// AddInformational registers a non-blocking informational health check.
// Its failure is reported in the response body but does not cause 503.
func (r *Registry) AddInformational(name string, check Check) {
	r.AddWithCriticality(name, check, Informational)
}

// AddWithCriticality registers a check with explicit criticality.
func (r *Registry) AddWithCriticality(name string, check Check, c Criticality) {
	if name == "" || check == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks[name] = entry{check: check, criticality: c}
}

// Remove deregisters the named check.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.checks, name)
}

// SetTimeout overrides the default 2-second per-check timeout.
// If d is less than or equal to 0, it defaults to 2 seconds.
func (r *Registry) SetTimeout(d time.Duration) {
	if d <= 0 {
		d = 2 * time.Second
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.timeout = d
}

// SetVerbose enables or disables exposing detailed error messages in the HTTP response.
// By default, it is false to avoid leaking sensitive information like IPs or DSNs.
func (r *Registry) SetVerbose(v bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.verbose = v
}

// CheckResult holds the outcome of a single check.
type CheckResult struct {
	Status      string `json:"status"`          // "ok" or "error"
	Error       string `json:"error,omitempty"` // error message when status is "error"
	Criticality string `json:"criticality"`     // "critical" or "informational"
}

// LiveHandler returns a simple liveness probe that always succeeds.
func LiveHandler(service string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": service})
	}
}

// Handler returns an HTTP handler for the readiness endpoint. It runs all
// registered checks concurrently and returns 503 if any critical check fails.
func (r *Registry) Handler(service string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.RLock()
		snapshot := make(map[string]entry, len(r.checks))
		for name, e := range r.checks {
			snapshot[name] = e
		}
		timeout := r.timeout
		verbose := r.verbose
		r.mu.RUnlock()

		names := make([]string, 0, len(snapshot))
		for name := range snapshot {
			names = append(names, name)
		}
		sort.Strings(names)

		type result struct {
			name string
			res  CheckResult
		}
		ch := make(chan result, len(names))
		for _, name := range names {
			name := name
			e := snapshot[name]
			go func() {
				ctx, cancel := context.WithTimeout(req.Context(), timeout)
				defer cancel()
				cr := CheckResult{
					Status:      "ok",
					Criticality: criticalityString(e.criticality),
				}

				defer func() {
					if rec := recover(); rec != nil {
						cr.Status = "error"
						cr.Error = "internal check panic"
						ch <- result{name: name, res: cr}
					}
				}()

				resultCh := make(chan error, 1)
				go func() {
					defer func() {
						if recover() != nil {
							resultCh <- errCheckPanic{}
						}
					}()
					resultCh <- e.check(ctx)
				}()
				var err error
				select {
				case err = <-resultCh:
				case <-ctx.Done():
					err = ctx.Err()
				}
				if err != nil {
					cr.Status = "error"
					if _, ok := err.(errCheckPanic); ok {
						cr.Error = "internal check panic"
					} else {
						cr.Error = err.Error()
					}
				}
				ch <- result{name: name, res: cr}
			}()
		}

		checks := make(map[string]CheckResult, len(names))
		ready := true
		for range names {
			r := <-ch
			if !verbose && r.res.Error != "" {
				r.res.Error = "redacted"
			}
			checks[r.name] = r.res
			if r.res.Status == "error" && snapshot[r.name].criticality == Critical {
				ready = false
			}
		}

		status := http.StatusOK
		state := "ok"
		if !ready {
			status = http.StatusServiceUnavailable
			state = "not_ready"
		}
		writeJSON(w, status, map[string]any{
			"status":  state,
			"service": service,
			"checks":  checks,
		})
	})
}

type errCheckPanic struct{}

func (errCheckPanic) Error() string { return "internal check panic" }

func criticalityString(c Criticality) string {
	if c == Informational {
		return "informational"
	}
	return "critical"
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
