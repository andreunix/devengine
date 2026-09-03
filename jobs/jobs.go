package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/andreunix/devengine/id"
	"github.com/jackc/pgx/v5"
)

// Job defines a task to be executed by a worker.
type Job struct {
	ID          string
	Name        string
	Payload     any
	RunAt       time.Time
	MaxAttempts int
}

// Enqueue inserts a job into the devengine_jobs table. It must be called
// within a transaction.
func Enqueue(ctx context.Context, tx pgx.Tx, job Job) error {
	if job.ID == "" {
		job.ID = id.MustUUIDv7()
	}
	if job.Name == "" {
		return fmt.Errorf("jobs: job name is required")
	}
	if job.RunAt.IsZero() {
		job.RunAt = time.Now()
	}
	if job.MaxAttempts <= 0 {
		job.MaxAttempts = 5
	}

	payload, err := json.Marshal(job.Payload)
	if err != nil {
		return fmt.Errorf("jobs: marshal payload: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO devengine_jobs (id, name, payload, run_at, max_attempts)
		VALUES ($1, $2, $3, $4, $5)
	`, job.ID, job.Name, payload, job.RunAt, job.MaxAttempts)

	if err != nil {
		return fmt.Errorf("jobs: enqueue: %w", err)
	}
	return nil
}

// Handler is an interface for processing jobs.
type Handler interface {
	Handle(ctx context.Context, payload []byte) error
}

// HandlerFunc allows using a function as a Handler.
type HandlerFunc func(ctx context.Context, payload []byte) error

func (f HandlerFunc) Handle(ctx context.Context, payload []byte) error {
	return f(ctx, payload)
}

// Registry stores the handlers for different job types.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

var (
	ErrInvalidRegistration = errors.New("jobs: invalid handler registration")
	ErrHandlerRegistered   = errors.New("jobs: handler already registered")
	ErrHandlerNotFound     = errors.New("jobs: handler not found")
)

// NewRegistry creates a new Registry.
func NewRegistry() *Registry {
	return &Registry{
		handlers: make(map[string]Handler),
	}
}

// Register associates a name with a handler. It rejects invalid and duplicate
// registrations so startup configuration cannot be overwritten silently.
func (r *Registry) Register(name string, h Handler) error {
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidRegistration)
	}
	if isNilHandler(h) {
		return fmt.Errorf("%w: handler for %q is nil", ErrInvalidRegistration, name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[name]; exists {
		return fmt.Errorf("%w: %q", ErrHandlerRegistered, name)
	}
	r.handlers[name] = h
	return nil
}

// Replace explicitly replaces an existing handler.
func (r *Registry) Replace(name string, h Handler) error {
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidRegistration)
	}
	if isNilHandler(h) {
		return fmt.Errorf("%w: handler for %q is nil", ErrInvalidRegistration, name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[name]; !exists {
		return fmt.Errorf("%w: %q", ErrHandlerNotFound, name)
	}
	r.handlers[name] = h
	return nil
}

// HandlerFor returns the handler registered for the given name.
func (r *Registry) HandlerFor(name string) Handler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.handlers[name]
}

func isNilHandler(h Handler) bool {
	if h == nil {
		return true
	}
	v := reflect.ValueOf(h)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
