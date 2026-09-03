package jobs_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/andreunix/devengine/jobs"
)

func TestRegistryRejectsInvalidAndDuplicateHandlers(t *testing.T) {
	registry := jobs.NewRegistry()
	handler := jobs.HandlerFunc(func(context.Context, []byte) error { return nil })

	if err := registry.Register("", handler); !errors.Is(err, jobs.ErrInvalidRegistration) {
		t.Fatalf("empty name error = %v", err)
	}
	if err := registry.Register("nil", nil); !errors.Is(err, jobs.ErrInvalidRegistration) {
		t.Fatalf("nil handler error = %v", err)
	}
	var typedNil jobs.HandlerFunc
	if err := registry.Register("typed-nil", typedNil); !errors.Is(err, jobs.ErrInvalidRegistration) {
		t.Fatalf("typed nil handler error = %v", err)
	}
	if err := registry.Register("task", handler); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("task", handler); !errors.Is(err, jobs.ErrHandlerRegistered) {
		t.Fatalf("duplicate handler error = %v", err)
	}
}

func TestRegistryConcurrentRegistrationAndRead(t *testing.T) {
	registry := jobs.NewRegistry()
	handler := jobs.HandlerFunc(func(context.Context, []byte) error { return nil })
	const registrations = 32

	var wg sync.WaitGroup
	errs := make(chan error, registrations)
	for i := range registrations {
		name := fmt.Sprintf("task-%d", i)
		wg.Add(2)
		go func() {
			defer wg.Done()
			errs <- registry.Register(name, handler)
		}()
		go func() {
			defer wg.Done()
			_ = registry.HandlerFor(name)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := range registrations {
		if registry.HandlerFor(fmt.Sprintf("task-%d", i)) == nil {
			t.Fatalf("handler task-%d was not registered", i)
		}
	}
}

func TestRegistryReplaceIsExplicit(t *testing.T) {
	registry := jobs.NewRegistry()
	first := jobs.HandlerFunc(func(context.Context, []byte) error { return errors.New("first") })
	second := jobs.HandlerFunc(func(context.Context, []byte) error { return errors.New("second") })

	if err := registry.Replace("task", second); !errors.Is(err, jobs.ErrHandlerNotFound) {
		t.Fatalf("replace missing handler error = %v", err)
	}
	if err := registry.Register("task", first); err != nil {
		t.Fatal(err)
	}
	if err := registry.Replace("task", second); err != nil {
		t.Fatal(err)
	}
	if got := registry.HandlerFor("task"); got == nil || got.Handle(context.Background(), nil).Error() != "second" {
		t.Fatalf("replacement handler = %v", got)
	}
}
