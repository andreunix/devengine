package engine

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/andreunix/devengine/telemetry"
)

type Option func(*Engine)

func WithName(name string) Option {
	return func(e *Engine) { e.name = name }
}

func WithAddress(address string) Option {
	return func(e *Engine) { e.address = address }
}

// WithProfile sets the engine profile (ProfileHTTPAndWorker, ProfileHTTP or ProfileWorker).
// The default is ProfileHTTPAndWorker.
func WithProfile(p Profile) Option {
	return func(e *Engine) { e.profile = p }
}

// WithVersion attaches a version string to every structured log record emitted
// by the engine. Consumers typically pass a build-time constant or git tag.
func WithVersion(version string) Option {
	return func(e *Engine) { e.version = version }
}

// WithEnvironment attaches an environment label (e.g. "production", "staging")
// to every structured log record emitted by the engine.
func WithEnvironment(env string) Option {
	return func(e *Engine) { e.environment = env }
}

// WithLogger replaces the engine's logger. If attrs are set via WithVersion or
// WithEnvironment, they are added as base attributes on top of the provided logger.
func WithLogger(logger *slog.Logger) Option {
	return func(e *Engine) {
		if logger != nil {
			e.logger = logger
		}
	}
}

func WithShutdownTimeout(timeout time.Duration) Option {
	return func(e *Engine) {
		if timeout > 0 {
			e.shutdownTimeout = timeout
		}
	}
}

func WithServerTimeouts(readHeader, read, write, idle time.Duration) Option {
	return func(e *Engine) {
		e.serverTimeouts = serverTimeouts{
			readHeader: readHeader,
			read:       read,
			write:      write,
			idle:       idle,
		}
	}
}

func WithMiddleware(middleware ...func(http.Handler) http.Handler) Option {
	return func(e *Engine) {
		e.middleware = append(e.middleware, middleware...)
	}
}

// WithTelemetry configures the Engine to use the provided Tracer and Meter.
func WithTelemetry(tracer telemetry.Tracer, meter telemetry.Meter) Option {
	return func(e *Engine) {
		if tracer != nil {
			e.tracer = tracer
		}
		if meter != nil {
			e.meter = meter
		}
	}
}
