// Package otel adapts OpenTelemetry providers to devengine telemetry interfaces.
// Applications own provider, exporter, resource and propagator setup.
package otel

import (
	"context"
	"fmt"
	"github.com/andreunix/devengine/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type Adapter struct {
	tracer oteltrace.Tracer
	meter  metric.Meter
}

func New(tp oteltrace.TracerProvider, mp metric.MeterProvider) Adapter {
	return Adapter{tracer: tp.Tracer("github.com/andreunix/devengine"), meter: mp.Meter("github.com/andreunix/devengine")}
}
func (a Adapter) Tracer() telemetry.Tracer { return tracer{a.tracer} }
func (a Adapter) Meter() telemetry.Meter   { return meterAdapter{a.meter} }

type tracer struct{ inner oteltrace.Tracer }

func (t tracer) Start(ctx context.Context, name string) (context.Context, telemetry.Span) {
	ctx, s := t.inner.Start(ctx, name)
	return ctx, span{s}
}

type span struct{ inner oteltrace.Span }

func (s span) End() { s.inner.End() }
func (s span) RecordError(e error) {
	if e != nil {
		s.inner.RecordError(e)
	}
}
func (s span) SetAttribute(k string, v any) {
	s.inner.SetAttributes(attribute.String(k, fmt.Sprint(v)))
}

type meterAdapter struct{ inner metric.Meter }

func (m meterAdapter) Int64Counter(n string) telemetry.Counter {
	c, e := m.inner.Int64Counter(n)
	if e != nil {
		return telemetry.NoopMeter.Int64Counter(n)
	}
	return counter{c}
}
func (m meterAdapter) Float64Histogram(n string) telemetry.Histogram {
	h, e := m.inner.Float64Histogram(n)
	if e != nil {
		return telemetry.NoopMeter.Float64Histogram(n)
	}
	return histogram{h}
}

type counter struct{ inner metric.Int64Counter }

func (c counter) Add(ctx context.Context, v int64, a map[string]string) {
	c.inner.Add(ctx, v, metric.WithAttributes(attrs(a)...))
}

type histogram struct{ inner metric.Float64Histogram }

func (h histogram) Record(ctx context.Context, v float64, a map[string]string) {
	h.inner.Record(ctx, v, metric.WithAttributes(attrs(a)...))
}
func attrs(m map[string]string) []attribute.KeyValue {
	r := make([]attribute.KeyValue, 0, len(m))
	for k, v := range m {
		r = append(r, attribute.String(k, v))
	}
	return r
}
