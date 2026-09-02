package engine

import (
	"log/slog"
	"net/http"
	"time"
)

type Option func(*Engine)

func WithName(name string) Option {
	return func(e *Engine) { e.name = name }
}

func WithAddress(address string) Option {
	return func(e *Engine) { e.address = address }
}

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
