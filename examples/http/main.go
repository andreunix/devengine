// Command http demonstrates an HTTP-only engine.
package main

import (
	"net/http"

	"github.com/andreunix/devengine/engine"
)

func main() {
	app := engine.New(engine.WithProfile(engine.ProfileHTTP))
	app.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	// Production applications call app.Run(context.Background()).
}
