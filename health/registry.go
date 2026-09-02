package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

type Check func(context.Context) error

type Registry struct {
	mu      sync.RWMutex
	checks  map[string]Check
	timeout time.Duration
}

func NewRegistry() *Registry {
	return &Registry{checks: make(map[string]Check), timeout: 2 * time.Second}
}

func (r *Registry) Add(name string, check Check) {
	if name == "" || check == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks[name] = check
}

func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.checks, name)
}

func LiveHandler(service string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": service})
	}
}

func (r *Registry) Handler(service string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		r.mu.RLock()
		checks := make(map[string]Check, len(r.checks))
		for name, check := range r.checks {
			checks[name] = check
		}
		r.mu.RUnlock()

		names := make([]string, 0, len(checks))
		for name := range checks {
			names = append(names, name)
		}
		sort.Strings(names)

		results := make(map[string]string, len(checks))
		ready := true
		for _, name := range names {
			ctx, cancel := context.WithTimeout(request.Context(), r.timeout)
			err := checks[name](ctx)
			cancel()
			if err != nil {
				ready = false
				results[name] = "error"
				continue
			}
			results[name] = "ok"
		}
		status := http.StatusOK
		state := "ok"
		if !ready {
			status = http.StatusServiceUnavailable
			state = "not_ready"
		}
		writeJSON(w, status, map[string]any{"status": state, "service": service, "checks": results})
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
