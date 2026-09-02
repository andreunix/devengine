package middleware

import (
	"context"
	"net/http"

	"github.com/andreunix/devengine/httpx/clientip"
)

type clientIPKey struct{}

// ClientIP returns a middleware that resolves the real client IP using trusted
// and injects it into the request context. Retrieve it with ClientIPFromContext.
func ClientIP(trusted clientip.TrustedProxies) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientip.FromRequest(r, trusted)
			ctx := context.WithValue(r.Context(), clientIPKey{}, ip)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ClientIPFromContext returns the client IP injected by the ClientIP middleware.
// Returns empty string if the middleware has not run.
func ClientIPFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(clientIPKey{}).(string); ok {
		return v
	}
	return ""
}
