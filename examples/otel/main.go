// Command otel demonstrates application-owned OpenTelemetry providers.
package main

import (
	"context"

	"github.com/andreunix/devengine/engine"
	devotel "github.com/andreunix/devengine/telemetry/otel"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/trace"
)

func main() {
	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	mp := metric.NewMeterProvider()
	defer func() { _ = mp.Shutdown(context.Background()) }()
	adapter := devotel.New(tp, mp)
	_ = engine.New(engine.WithTelemetry(adapter.Tracer(), adapter.Meter()))
}
