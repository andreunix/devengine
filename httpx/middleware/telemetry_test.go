package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	devotel "github.com/andreunix/devengine/telemetry/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTelemetryExtractsW3CTraceContext(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	adapter := devotel.New(trace.NewTracerProvider(trace.WithSpanProcessor(recorder)), metric.NewMeterProvider())
	handler := Telemetry(adapter.Tracer(), adapter.Meter(), WithPropagator(propagation.TraceContext{}))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	request := httptest.NewRequest(http.MethodGet, "/orders", nil)
	request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	if got, want := spans[0].SpanContext().TraceID().String(), "4bf92f3577b34da6a3ce929d0e0e4736"; got != want {
		t.Fatalf("trace ID = %s, want %s", got, want)
	}
	if !spans[0].Parent().IsValid() {
		t.Fatal("expected extracted parent span context")
	}
}

func TestInjectTraceContext(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	adapter := devotel.New(trace.NewTracerProvider(trace.WithSpanProcessor(recorder)), metric.NewMeterProvider())
	ctx, span := adapter.Tracer().Start(context.Background(), "outbound")
	defer span.End()
	header := make(http.Header)
	InjectTraceContext(ctx, header, propagation.TraceContext{})
	if header.Get("traceparent") == "" {
		t.Fatal("expected traceparent header")
	}
}

func TestTelemetryUsesCustomPropagator(t *testing.T) {
	propagator := &recordingPropagator{}
	handler := Telemetry(nil, nil, WithPropagator(propagator))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !propagator.extracted {
		t.Fatal("expected custom propagator extraction")
	}

	InjectTraceContext(context.Background(), make(http.Header), propagator)
	if !propagator.injected {
		t.Fatal("expected custom propagator injection")
	}
}

func TestTelemetryUsesRoutePatternAndSemanticAttributes(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	adapter := devotel.New(trace.NewTracerProvider(trace.WithSpanProcessor(recorder)), metric.NewMeterProvider())
	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	handler := Telemetry(adapter.Tracer(), adapter.Meter())(mux)

	request := httptest.NewRequest(http.MethodGet, "https://api.example.com/users/123?token=secret", nil)
	handler.ServeHTTP(httptest.NewRecorder(), request)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	if got := spans[0].Name(); got != "GET /users/{id}" {
		t.Fatalf("span name = %q, want route pattern", got)
	}
	attributes := attributeMap(spans[0].Attributes())
	assertStringAttribute(t, attributes, "http.request.method", http.MethodGet)
	assertStringAttribute(t, attributes, "http.route", "/users/{id}")
	assertStringAttribute(t, attributes, "url.path", "/users/123")
	assertStringAttribute(t, attributes, "server.address", "api.example.com")
	if _, ok := attributes["http.url"]; ok {
		t.Fatal("full URL must not be recorded")
	}
	if got := attributes["http.response.status_code"].AsInt64(); got != http.StatusOK {
		t.Fatalf("status code = %d, want %d", got, http.StatusOK)
	}
}

func TestTelemetryWithoutPropagatorDoesNotExtractHeaders(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	adapter := devotel.New(trace.NewTracerProvider(trace.WithSpanProcessor(recorder)), metric.NewMeterProvider())
	handler := Telemetry(adapter.Tracer(), adapter.Meter())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	span := recorder.Ended()[0]
	if span.Parent().IsValid() {
		t.Fatal("traceparent was extracted without an explicit propagator")
	}
	if span.SpanContext().TraceID().String() == "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatal("span unexpectedly joined the inbound trace")
	}
}

func attributeMap(attributes []attribute.KeyValue) map[string]attribute.Value {
	result := make(map[string]attribute.Value, len(attributes))
	for _, item := range attributes {
		result[string(item.Key)] = item.Value
	}
	return result
}

func assertStringAttribute(t *testing.T, attributes map[string]attribute.Value, key, want string) {
	t.Helper()
	value, ok := attributes[key]
	if !ok {
		t.Fatalf("attribute %q not found", key)
	}
	if got := value.AsString(); got != want {
		t.Fatalf("attribute %q = %q, want %q", key, got, want)
	}
}

type recordingPropagator struct {
	extracted bool
	injected  bool
}

func (p *recordingPropagator) Extract(ctx context.Context, _ propagation.TextMapCarrier) context.Context {
	p.extracted = true
	return ctx
}

func (p *recordingPropagator) Inject(context.Context, propagation.TextMapCarrier) {
	p.injected = true
}

func (p *recordingPropagator) Fields() []string { return nil }
