package middleware

import (
	"log/slog"
	"net/http"

	"github.com/andreunix/devengine/httpx/problem"
	"github.com/andreunix/devengine/httpx/requestid"
)

func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recovered := recover(); recovered != nil {
					if logger != nil {
						logger.Error("http panic recovered", "panic", recovered, "request_id", requestid.FromContext(r.Context()))
					}
					problem.InternalServerError(w, r)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
