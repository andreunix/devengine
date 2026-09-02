package middleware

import (
	"fmt"
	"net/http"
	"time"

	"github.com/andreunix/devengine/httpx/requestid"
	"github.com/andreunix/devengine/telemetry"
)

// Telemetry returns a middleware that traces HTTP requests and records metrics.
func Telemetry(tracer telemetry.Tracer, meter telemetry.Meter) Middleware {
	if tracer == nil {
		tracer = telemetry.NoopTracer
	}
	if meter == nil {
		meter = telemetry.NoopMeter
	}

	reqCounter := meter.Int64Counter("http.server.requests")
	reqLatency := meter.Float64Histogram("http.server.latency")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, span := tracer.Start(r.Context(), fmt.Sprintf("%s %s", r.Method, r.URL.Path))
			defer span.End()

			span.SetAttribute("http.method", r.Method)
			span.SetAttribute("http.url", r.URL.String())

			if reqID := requestid.FromContext(ctx); reqID != "" {
				span.SetAttribute("http.request_id", reqID)
			}

			start := time.Now()
			writer := &statusWriter{ResponseWriter: w}

			next.ServeHTTP(writer, r.WithContext(ctx))

			if writer.status == 0 {
				writer.status = http.StatusOK
			}

			duration := time.Since(start).Seconds()

			span.SetAttribute("http.status_code", writer.status)

			attrs := map[string]string{
				"method": r.Method,
				"status": fmt.Sprintf("%d", writer.status),
			}
			reqCounter.Add(ctx, 1, attrs)
			reqLatency.Record(ctx, duration, attrs)
		})
	}
}
