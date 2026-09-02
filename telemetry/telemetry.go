package telemetry

import "context"

// Span represents a single operation within a trace.
type Span interface {
	End()
	RecordError(err error)
	SetAttribute(key string, value any)
}

// Tracer creates spans.
type Tracer interface {
	Start(ctx context.Context, name string) (context.Context, Span)
}

// Counter records absolute values (e.g. number of requests, bytes sent).
type Counter interface {
	Add(ctx context.Context, value int64, attributes map[string]string)
}

// Histogram records a distribution of values (e.g. request latencies).
type Histogram interface {
	Record(ctx context.Context, value float64, attributes map[string]string)
}

// Meter creates telemetry instruments.
type Meter interface {
	Int64Counter(name string) Counter
	Float64Histogram(name string) Histogram
}

// noop implementations

type noopSpan struct{}

func (noopSpan) End()                               {}
func (noopSpan) RecordError(err error)              {}
func (noopSpan) SetAttribute(key string, value any) {}

type noopTracer struct{}

func (noopTracer) Start(ctx context.Context, name string) (context.Context, Span) {
	return ctx, noopSpan{}
}

type noopCounter struct{}

func (noopCounter) Add(ctx context.Context, value int64, attributes map[string]string) {}

type noopHistogram struct{}

func (noopHistogram) Record(ctx context.Context, value float64, attributes map[string]string) {}

type noopMeter struct{}

func (noopMeter) Int64Counter(name string) Counter       { return noopCounter{} }
func (noopMeter) Float64Histogram(name string) Histogram { return noopHistogram{} }

var (
	// NoopTracer is a tracer that performs no operations.
	NoopTracer Tracer = noopTracer{}
	// NoopMeter is a meter that performs no operations.
	NoopMeter Meter = noopMeter{}
)
