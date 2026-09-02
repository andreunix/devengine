package middleware

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/andreunix/devengine/httpx/requestid"
	"github.com/andreunix/devengine/telemetry"
	"go.opentelemetry.io/otel/propagation"
)

type telemetryConfig struct {
	propagator propagation.TextMapPropagator
}

// TelemetryOption configures HTTP telemetry integration.
type TelemetryOption func(*telemetryConfig)

// WithPropagator enables inbound extraction of distributed trace context. The
// propagator is application-owned; middleware never configures global OTel
// state, providers, or exporters.
func WithPropagator(propagator propagation.TextMapPropagator) TelemetryOption {
	return func(config *telemetryConfig) { config.propagator = propagator }
}

// Telemetry returns a middleware that traces HTTP requests and records metrics.
// Without WithPropagator, the middleware does not extract request headers.
func Telemetry(tracer telemetry.Tracer, meter telemetry.Meter, options ...TelemetryOption) Middleware {
	if tracer == nil {
		tracer = telemetry.NoopTracer
	}
	if meter == nil {
		meter = telemetry.NoopMeter
	}
	config := telemetryConfig{}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}

	reqCounter := meter.Int64Counter("http.server.requests")
	reqLatency := meter.Float64Histogram("http.server.latency")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			if config.propagator != nil {
				ctx = config.propagator.Extract(ctx, propagation.HeaderCarrier(r.Header))
			}
			ctx, span := tracer.Start(ctx, fmt.Sprintf("%s %s", r.Method, r.URL.Path))
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

// InjectTraceContext injects the trace context into headers for an outbound
// request or response. A nil propagator intentionally leaves headers unchanged.
func InjectTraceContext(ctx context.Context, header http.Header, propagator propagation.TextMapPropagator) {
	if propagator != nil {
		propagator.Inject(ctx, propagation.HeaderCarrier(header))
	}
}
