package otel

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestAdapterRecordsSpansAndMetricsInMemory(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := trace.NewTracerProvider(trace.WithSpanProcessor(recorder))
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	adapter := New(tp, mp)

	ctx, span := adapter.Tracer().Start(context.Background(), "request")
	span.SetAttribute("http.method", "GET")
	span.RecordError(errors.New("handler failed"))
	span.End()
	adapter.Meter().Int64Counter("requests").Add(ctx, 1, map[string]string{"status": "500"})

	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "request" {
		t.Fatalf("spans = %+v", spans)
	}
	if len(spans[0].Events()) == 0 {
		t.Fatal("expected recorded error event")
	}
	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.ScopeMetrics) == 0 {
		t.Fatal("expected collected metric")
	}
}

func TestAdapterPreservesAttributeTypes(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := trace.NewTracerProvider(trace.WithSpanProcessor(recorder))
	adapter := New(tp, metric.NewMeterProvider())

	_, span := adapter.Tracer().Start(context.Background(), "attributes")
	span.SetAttribute("string", "value")
	span.SetAttribute("bool", true)
	span.SetAttribute("int", 201)
	span.SetAttribute("float", 1.5)
	span.SetAttribute("strings", []string{"a", "b"})
	span.SetAttribute("fallback", struct{ Name string }{"ok"})
	span.End()

	got := recorder.Ended()[0].Attributes()
	assertAttributeType(t, got, "string", attribute.STRING)
	assertAttributeType(t, got, "bool", attribute.BOOL)
	assertAttributeType(t, got, "int", attribute.INT64)
	assertAttributeType(t, got, "float", attribute.FLOAT64)
	assertAttributeType(t, got, "strings", attribute.STRINGSLICE)
	assertAttributeType(t, got, "fallback", attribute.STRING)
}

func assertAttributeType(t *testing.T, attributes []attribute.KeyValue, key string, want attribute.Type) {
	t.Helper()
	for _, got := range attributes {
		if string(got.Key) == key {
			if got.Value.Type() != want {
				t.Fatalf("attribute %q type = %v, want %v", key, got.Value.Type(), want)
			}
			return
		}
	}
	t.Fatalf("attribute %q not found", key)
}
