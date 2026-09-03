// Package events defines the neutral event envelope and handler interface
// used by the devengine outbox and other async infrastructure.
//
// The engine provides the mechanism; consuming applications define the event
// types (e.g. UserCreated, OrderPlaced). Events should not import domain
// packages from specific products.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidHandler = errors.New("events: invalid handler")
	ErrRegistryFrozen = errors.New("events: registry is frozen")
	ErrDuplicate      = errors.New("events: handler already registered")
)

// Event is the canonical envelope for all asynchronous domain events.
type Event struct {
	// ID is a unique identifier for this event occurrence (UUIDv7 recommended).
	ID string `json:"id"`
	// Type identifies the event kind (e.g. "user.created", "order.placed").
	Type string `json:"type"`
	// AggregateID identifies the domain entity this event relates to.
	AggregateID string `json:"aggregate_id,omitempty"`
	// AggregateType identifies the kind of the domain entity.
	AggregateType string `json:"aggregate_type,omitempty"`
	// Payload is the event body, serialised as raw JSON.
	Payload json.RawMessage `json:"payload,omitempty"`
	// OccurredAt is the time the event occurred in the domain (not when it was stored).
	OccurredAt time.Time `json:"occurred_at"`
	// SchemaVersion allows consumers to evolve payload formats without breaking
	// existing handlers.
	SchemaVersion int `json:"schema_version,omitempty"`
}

// Handler processes a single event. Implementations must be idempotent because
// the outbox relay may deliver the same event more than once on restart.
type Handler interface {
	// EventType returns the event type this handler accepts.
	// Use "*" to handle all types.
	EventType() string
	// Handle processes the event. Returning a non-nil error causes the relay to
	// retry according to the message-owned max_attempts policy.
	Handle(ctx context.Context, event Event) error
}

// HandlerFunc adapts a plain function to the Handler interface.
type HandlerFunc struct {
	Type    string
	HandleF func(ctx context.Context, event Event) error
}

func (h HandlerFunc) EventType() string { return h.Type }
func (h HandlerFunc) Handle(ctx context.Context, e Event) error {
	if h.HandleF == nil {
		return nil
	}
	return h.HandleF(ctx, e)
}

// Registry maps event types to their handlers.
// A single event type may have multiple handlers.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string][]Handler
	frozen   bool
}

// NewRegistry creates an empty handler registry.
func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string][]Handler)}
}

// Register adds h to the registry. Multiple handlers for the same event type
// are supported; registering the same handler instance or HandlerFunc twice is rejected.
// Handlers registered for "*" receive all events. Register returns an error
// after Freeze has made the registry read-only.
func (r *Registry) Register(h Handler) error {
	if isNilHandler(h) {
		return ErrInvalidHandler
	}
	t := strings.TrimSpace(h.EventType())
	if t == "" {
		return ErrInvalidHandler
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return ErrRegistryFrozen
	}
	if r.handlers == nil {
		r.handlers = make(map[string][]Handler)
	}
	for _, existing := range r.handlers[t] {
		if sameHandler(existing, h) {
			return ErrDuplicate
		}
	}
	r.handlers[t] = append(r.handlers[t], h)
	return nil
}

// Freeze prevents further registration. Reads remain safe concurrently.
func (r *Registry) Freeze() {
	r.mu.Lock()
	r.frozen = true
	r.mu.Unlock()
}

// Frozen reports whether registration has been closed.
func (r *Registry) Frozen() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.frozen
}

// HandlersFor returns all handlers that should process an event of the given type.
func (r *Registry) HandlersFor(eventType string) []Handler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Handler
	out = append(out, r.handlers[eventType]...)
	if eventType != "*" {
		out = append(out, r.handlers["*"]...)
	}
	return out
}

func isNilHandler(h Handler) bool {
	if h == nil {
		return true
	}
	switch handler := h.(type) {
	case HandlerFunc:
		return handler.HandleF == nil
	case *HandlerFunc:
		return handler == nil || handler.HandleF == nil
	}
	v := reflect.ValueOf(h)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func sameHandler(a, b Handler) bool {
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	if ta != tb {
		return false
	}
	if ta.Comparable() {
		return a == b
	}
	var af, bf HandlerFunc
	switch value := a.(type) {
	case HandlerFunc:
		af = value
	case *HandlerFunc:
		af = *value
	default:
		return false
	}
	switch value := b.(type) {
	case HandlerFunc:
		bf = value
	case *HandlerFunc:
		bf = *value
	default:
		return false
	}
	return af.Type == bf.Type && reflect.ValueOf(af.HandleF).Pointer() == reflect.ValueOf(bf.HandleF).Pointer()
}
