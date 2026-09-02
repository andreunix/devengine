package otel

import (
	"context"
	"errors"
	"testing"

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
