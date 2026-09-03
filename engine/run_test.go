package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestModuleCanRegisterWorker(t *testing.T) {
	app := New(WithAddress("127.0.0.1:0"))
	module := ModuleFunc{
		ModuleName: "worker-module",
		RegisterFunc: func(app *Engine) error {
			return app.AddWorker(WorkerFunc{WorkerName: "module-worker"})
		},
	}
	if err := app.Register(module); err != nil {
		t.Fatal(err)
	}
}

func TestRunReturnsWorkerFailure(t *testing.T) {
	sentinel := errors.New("worker failed")
	app := New(WithAddress("127.0.0.1:0"))
	if err := app.AddWorker(WorkerFunc{
		WorkerName: "failing",
		RunFunc:    func(context.Context) error { return sentinel },
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("expected worker error, got %v", err)
	}
}

func TestRunRejectsUnexpectedCleanWorkerExit(t *testing.T) {
	app := New(WithProfile(ProfileWorker))
	if err := app.AddWorker(WorkerFunc{
		WorkerName: "short-lived",
		RunFunc:    func(context.Context) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}

	err := app.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), `worker "short-lived": exited unexpectedly before shutdown`) {
		t.Fatalf("expected unexpected worker exit error, got %v", err)
	}
}

func TestRunBoundsNonCooperativeWorkerShutdown(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	app := New(
		WithProfile(ProfileWorker),
		WithWorkerShutdownTimeout(20*time.Millisecond),
	)
	if err := app.AddWorker(WorkerFunc{
		WorkerName: "stuck-worker",
		RunFunc: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	<-started
	cancel()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "stuck-worker") {
			t.Fatalf("expected timeout error identifying stuck-worker, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("engine shutdown remained blocked by non-cooperative worker")
	}
	close(release)
}

func TestRunGracefulShutdownByProfile(t *testing.T) {
	tests := []struct {
		name          string
		profile       Profile
		expectsWorker bool
	}{
		{name: "http-only", profile: ProfileHTTP},
		{name: "worker-only", profile: ProfileWorker, expectsWorker: true},
		{name: "combined", profile: ProfileHTTPAndWorker, expectsWorker: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			started := make(chan struct{})
			app := New(WithProfile(tt.profile), WithAddress("127.0.0.1:0"))
			if err := app.AddWorker(WorkerFunc{
				WorkerName: "cooperative",
				RunFunc: func(ctx context.Context) error {
					close(started)
					<-ctx.Done()
					return nil
				},
			}); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- app.Run(ctx) }()
			if tt.expectsWorker {
				<-started
			}
			cancel()

			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("graceful shutdown returned error: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("graceful shutdown timed out")
			}
			if !tt.expectsWorker {
				select {
				case <-started:
					t.Fatal("HTTP-only profile started a worker")
				default:
				}
			}
		})
	}
}
