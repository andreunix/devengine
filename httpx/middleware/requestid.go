package middleware

import (
	"net/http"
	"strings"

	"github.com/andreunix/devengine/httpx/requestid"
	"github.com/andreunix/devengine/id"
)

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if reqID == "" || len(reqID) > 128 {
			reqID = id.MustUUIDv7()
		}
		w.Header().Set("X-Request-ID", reqID)
		ctx := requestid.WithContext(r.Context(), reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
