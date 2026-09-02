package requestid

import "context"

type contextKey struct{}

// FromContext retrieves the request ID previously set in the context.
// Returns an empty string if not set.
func FromContext(ctx context.Context) string {
	if v, ok := ctx.Value(contextKey{}).(string); ok {
		return v
	}
	return ""
}

// WithContext returns a new context carrying the given request ID.
func WithContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}
