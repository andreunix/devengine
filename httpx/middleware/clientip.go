package middleware

import (
	"net/http"

	"github.com/andreunix/devengine/httpx/clientip"
)

// ClientIP returns a middleware that resolves the real client IP using trusted
// and injects it into the request context. Retrieve it with clientip.FromContext.
func ClientIP(trusted clientip.TrustedProxies) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientip.FromRequest(r, trusted)
			ctx := clientip.WithContext(r.Context(), ip)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
