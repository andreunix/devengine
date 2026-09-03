package events

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type testHandler struct{ eventType string }

func (h *testHandler) EventType() string                   { return h.eventType }
func (h *testHandler) Handle(context.Context, Event) error { return nil }

func TestRegistryRejectsInvalidDuplicateAndFrozenRegistration(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nil); !errors.Is(err, ErrInvalidHandler) {
		t.Fatalf("nil handler error = %v", err)
	}
	if err := r.Register(&testHandler{}); !errors.Is(err, ErrInvalidHandler) {
		t.Fatalf("empty event type error = %v", err)
	}
	if err := r.Register(HandlerFunc{Type: "event"}); !errors.Is(err, ErrInvalidHandler) {
		t.Fatalf("nil function error = %v", err)
	}
	h := &testHandler{eventType: "orders.created"}
	if err := r.Register(h); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(h); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate error = %v", err)
	}
	r.Freeze()
	if !r.Frozen() {
		t.Fatal("registry was not frozen")
	}
	if err := r.Register(&testHandler{eventType: "orders.cancelled"}); !errors.Is(err, ErrRegistryFrozen) {
		t.Fatalf("frozen error = %v", err)
	}
}

func TestRegistryWildcardAndMultipleHandlers(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&testHandler{eventType: "orders.created"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&testHandler{eventType: "orders.created"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&testHandler{eventType: "*"}); err != nil {
		t.Fatal(err)
	}
	if got := len(r.HandlersFor("orders.created")); got != 3 {
		t.Fatalf("handlers = %d, want 3", got)
	}
	if got := len(r.HandlersFor("*")); got != 1 {
		t.Fatalf("wildcard handlers = %d, want 1", got)
	}
}

func TestRegistryRejectsDuplicateHandlerFunc(t *testing.T) {
	r := NewRegistry()
	fn := func(context.Context, Event) error { return nil }
	h := HandlerFunc{Type: "event", HandleF: fn}
	if err := r.Register(h); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(h); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate HandlerFunc error = %v", err)
	}
}

func TestRegistryConcurrentRegistrationAndRead(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = r.Register(&testHandler{eventType: "event"})
		}()
		go func() {
			defer wg.Done()
			_ = r.HandlersFor("event")
		}()
	}
	wg.Wait()
	r.Freeze()
}
