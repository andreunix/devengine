package jobs

import (
	"context"
	"encoding/json"
	"fmt"
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

// NewRegistry creates a new Registry.
func NewRegistry() *Registry {
	return &Registry{
		handlers: make(map[string]Handler),
	}
}

// Register associates a name with a handler.
func (r *Registry) Register(name string, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[name] = h
}

// HandlerFor returns the handler registered for the given name.
func (r *Registry) HandlerFor(name string) Handler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.handlers[name]
}
