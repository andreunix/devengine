package engine

import (
	"context"
	"errors"
	"testing"
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
