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
	"time"
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
	// Handle processes the event. Returning a non-nil error causes the relay
	// to retry according to its RetryConfig.
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
	handlers map[string][]Handler
}

// NewRegistry creates an empty handler registry.
func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string][]Handler)}
}

// Register adds h to the registry. Handlers registered for "*" receive all events.
func (r *Registry) Register(h Handler) {
	if h == nil {
		return
	}
	t := h.EventType()
	r.handlers[t] = append(r.handlers[t], h)
}

// HandlersFor returns all handlers that should process an event of the given type.
func (r *Registry) HandlersFor(eventType string) []Handler {
	var out []Handler
	out = append(out, r.handlers[eventType]...)
	if eventType != "*" {
		out = append(out, r.handlers["*"]...)
	}
	return out
}
