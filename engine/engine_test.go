package engine

import (
	"context"
	"testing"
	"time"
)

func TestRejectsDuplicateModules(t *testing.T) {
	app := New()
	module := ModuleFunc{ModuleName: "catalog"}
	if err := app.Register(module); err != nil {
		t.Fatal(err)
	}
	if err := app.Register(module); err == nil {
		t.Fatal("expected duplicate module error")
	}
}

func TestRejectsDuplicateWorkers(t *testing.T) {
	app := New()
	worker := WorkerFunc{WorkerName: "mail"}
	if err := app.AddWorker(worker); err != nil {
		t.Fatal(err)
	}
	if err := app.AddWorker(worker); err == nil {
		t.Fatal("expected duplicate worker error")
	}
}

func TestProfileString(t *testing.T) {
	cases := []struct {
		p    Profile
		want string
	}{
		{ProfileHTTPAndWorker, "http+worker"},
		{ProfileHTTP, "http-only"},
		{ProfileWorker, "worker-only"},
	}
	for _, tc := range cases {
		if got := tc.p.String(); got != tc.want {
			t.Errorf("Profile(%d).String() = %q, want %q", tc.p, got, tc.want)
		}
	}
}

func TestWithProfile(t *testing.T) {
	app := New(WithProfile(ProfileHTTP))
	if app.profile != ProfileHTTP {
		t.Fatalf("expected ProfileHTTP, got %v", app.profile)
	}
}

// TestProfileWorkerDoesNotBlockWithNoHTTP verifies that a worker-only engine
// shuts down cleanly when ctx is cancelled (no HTTP server to bind).
func TestProfileWorkerDoesNotBlockWithNoHTTP(t *testing.T) {
	workerRan := make(chan struct{})
	app := New(
		WithProfile(ProfileWorker),
	)
	if err := app.AddWorker(WorkerFunc{
		WorkerName: "signal-worker",
		RunFunc: func(ctx context.Context) error {
			close(workerRan)
			<-ctx.Done()
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	select {
	case <-workerRan:
	case <-time.After(time.Second):
		t.Fatal("worker did not start within timeout")
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("engine did not shut down within timeout")
	}
}
