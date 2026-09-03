// Command worker demonstrates a worker-only engine.
package main

import (
	"context"

	"github.com/andreunix/devengine/engine"
)

func main() {
	app := engine.New(engine.WithProfile(engine.ProfileWorker))
	_ = app.AddWorker(engine.WorkerFunc{
		WorkerName: "example-worker",
		RunFunc: func(ctx context.Context) error {
			<-ctx.Done()
			return nil
		},
	})
}
