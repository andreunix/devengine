package health

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/andreunix/devengine/telemetry"
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
	generation  uint64
}

// Registry holds named health checks and exposes them as HTTP handlers.
type Registry struct {
	mu             sync.RWMutex
	checks         map[string]entry
	timeout        time.Duration
	verbose        bool
	running        map[string]bool
	lastResults    map[string]CheckResult
	logger         *slog.Logger
	duration       telemetry.Histogram
	timeouts       telemetry.Counter
	panics         telemetry.Counter
	inflight       telemetry.Counter
	nextGeneration uint64
}

// NewRegistry creates a Registry with a 2-second per-check timeout.
func NewRegistry() *Registry {
	return &Registry{
		checks:      make(map[string]entry),
		running:     make(map[string]bool),
		lastResults: make(map[string]CheckResult),
		timeout:     2 * time.Second,
	}
}

type upDownMeter interface {
	Int64UpDownCounter(name string) telemetry.Counter
}

// SetTelemetry enables low-cardinality health metrics. The supplied meter is
// application-owned; the registry never creates providers or exporters.
func (r *Registry) SetTelemetry(meter telemetry.Meter) {
	if meter == nil {
		meter = telemetry.NoopMeter
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.duration = meter.Float64Histogram("health_check_duration")
	r.timeouts = meter.Int64Counter("health_check_timeout_total")
	r.panics = meter.Int64Counter("health_check_panics_total")
	if signed, ok := meter.(upDownMeter); ok {
		r.inflight = signed.Int64UpDownCounter("health_check_inflight")
	}
}

// SetLogger enables transition-only health logging.
func (r *Registry) SetLogger(logger *slog.Logger) {
	r.mu.Lock()
	r.logger = logger
	r.mu.Unlock()
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
	r.nextGeneration++
	r.checks[name] = entry{check: check, criticality: c, generation: r.nextGeneration}
	delete(r.lastResults, name)
}

// Remove deregisters the named check.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.checks, name)
	delete(r.lastResults, name)
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

// Snapshot returns the most recent result of each check. The returned map is
// detached from the registry and is safe for the caller to modify.
func (r *Registry) Snapshot() map[string]CheckResult {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]CheckResult, len(r.lastResults))
	for name, result := range r.lastResults {
		out[name] = result
	}
	return out
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
				r.mu.Lock()
				if r.running[name] {
					r.mu.Unlock()
					cr.Status, cr.Error = "error", "check already running"
					ch <- result{name: name, res: cr}
					return
				}
				r.running[name] = true
				duration := r.duration
				timeouts := r.timeouts
				panics := r.panics
				inflight := r.inflight
				r.mu.Unlock()

				defer func() {
					if rec := recover(); rec != nil {
						cr.Status = "error"
						cr.Error = "internal check panic"
						ch <- result{name: name, res: cr}
					}
				}()

				resultCh := make(chan error, 1)
				started := time.Now()
				attrs := map[string]string{"check": name}
				if inflight != nil {
					inflight.Add(req.Context(), 1, attrs)
				}
				go func() {
					defer func() {
						r.mu.Lock()
						delete(r.running, name)
						r.mu.Unlock()
						if duration != nil {
							duration.Record(req.Context(), time.Since(started).Seconds(), attrs)
						}
						if inflight != nil {
							inflight.Add(req.Context(), -1, attrs)
						}
					}()
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
						if panics != nil {
							panics.Add(req.Context(), 1, attrs)
						}
					} else {
						cr.Error = err.Error()
						if errors.Is(err, context.DeadlineExceeded) && timeouts != nil {
							timeouts.Add(req.Context(), 1, attrs)
						}
					}
				}
				ch <- result{name: name, res: cr}
			}()
		}

		checks := make(map[string]CheckResult, len(names))
		ready := true
		for range names {
			item := <-ch
			r.recordResult(item.name, snapshot[item.name].generation, item.res)
			if !verbose && item.res.Error != "" {
				item.res.Error = "redacted"
			}
			checks[item.name] = item.res
			if item.res.Status == "error" && snapshot[item.name].criticality == Critical {
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

func (r *Registry) recordResult(name string, generation uint64, result CheckResult) {
	r.mu.Lock()
	current, registered := r.checks[name]
	if !registered || current.generation != generation {
		r.mu.Unlock()
		return
	}
	previous, seen := r.lastResults[name]
	r.lastResults[name] = result
	logger := r.logger
	r.mu.Unlock()
	if !seen || previous.Status == result.Status || logger == nil {
		return
	}
	if result.Status == "ok" {
		logger.Info("health check recovered", "check", name, "from", previous.Status, "to", result.Status)
		return
	}
	logger.Warn("health check became unhealthy", "check", name, "from", previous.Status, "to", result.Status)
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
