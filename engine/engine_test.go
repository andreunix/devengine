package engine

import "testing"

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
