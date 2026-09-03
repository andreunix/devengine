package middleware

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
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

	reqCounter := meter.Int64Counter("http.server.request.count")
	reqLatency := meter.Float64Histogram("http.server.request.duration")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			if config.propagator != nil {
				ctx = config.propagator.Extract(ctx, propagation.HeaderCarrier(r.Header))
			}
			ctx, span := tracer.Start(ctx, r.Method)
			defer span.End()

			span.SetAttribute("http.request.method", r.Method)
			span.SetAttribute("url.path", r.URL.Path)
			if address := serverAddress(r.Host); address != "" {
				span.SetAttribute("server.address", address)
			}

			if reqID := requestid.FromContext(ctx); reqID != "" {
				span.SetAttribute("http.request_id", reqID)
			}

			start := time.Now()
			writer := &statusWriter{ResponseWriter: w}

			r = r.WithContext(ctx)
			next.ServeHTTP(writer, r)
			route := routePattern(r)
			if route != "" {
				span.SetAttribute("http.route", route)
				if named, ok := span.(interface{ SetName(string) }); ok {
					named.SetName(httpSpanName(r.Method, route))
				}
			}

			if writer.status == 0 {
				writer.status = http.StatusOK
			}

			duration := time.Since(start).Seconds()

			span.SetAttribute("http.response.status_code", writer.status)

			attrs := map[string]string{
				"http.request.method":       r.Method,
				"http.response.status_code": fmt.Sprintf("%d", writer.status),
			}
			if route != "" {
				attrs["http.route"] = route
			}
			reqCounter.Add(ctx, 1, attrs)
			reqLatency.Record(ctx, duration, attrs)
		})
	}
}

func routePattern(r *http.Request) string {
	pattern := strings.TrimSpace(r.Pattern)
	if pattern == "" {
		return ""
	}
	if strings.HasPrefix(pattern, r.Method+" ") {
		pattern = strings.TrimSpace(strings.TrimPrefix(pattern, r.Method+" "))
	}
	if slash := strings.IndexByte(pattern, '/'); slash >= 0 {
		return pattern[slash:]
	}
	return pattern
}

func httpSpanName(method, route string) string {
	if route == "" {
		return method
	}
	return method + " " + route
}

func serverAddress(hostport string) string {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return host
	}
	return strings.Trim(hostport, "[]")
}

// InjectTraceContext injects the trace context into headers for an outbound
// request or response. A nil propagator intentionally leaves headers unchanged.
func InjectTraceContext(ctx context.Context, header http.Header, propagator propagation.TextMapPropagator) {
	if propagator != nil {
		propagator.Inject(ctx, propagation.HeaderCarrier(header))
	}
}
