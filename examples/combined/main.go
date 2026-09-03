// Command combined demonstrates HTTP and worker registration in one process.
package main

import (
	"context"
	"net/http"

	"github.com/andreunix/devengine/engine"
)

func main() {
	app := engine.New()
	app.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	_ = app.AddWorker(engine.WorkerFunc{
		WorkerName: "consumer",
		RunFunc: func(ctx context.Context) error {
			<-ctx.Done()
			return nil
		},
	})
}
