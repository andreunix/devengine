package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/andreunix/devengine/httpx/requestid"
)

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

// Unwrap returns the underlying http.ResponseWriter.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func Logging(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			writer := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(writer, r)
			if writer.status == 0 {
				writer.status = http.StatusOK
			}
			if logger != nil {
				logger.Info("http request",
					"method", r.Method,
					"path", r.URL.Path,
					"status", writer.status,
					"duration_ms", time.Since(started).Milliseconds(),
					"request_id", requestid.FromContext(r.Context()),
				)
			}
		})
	}
}
