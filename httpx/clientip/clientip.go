// Package clientip resolves the real client IP address from an HTTP request,
// respecting a configurable list of trusted proxy CIDRs.
//
// Security: X-Forwarded-For and X-Real-IP headers are never trusted
// unconditionally. Only requests arriving from known trusted proxies have
// their forwarded-for header examined.
package clientip

import (
	"net"
	"net/http"
	"strings"
)

// TrustedProxies is a set of CIDR ranges whose X-Forwarded-For header is
// trusted. Use ParseTrustedProxies to construct one from strings.
type TrustedProxies struct {
	nets []*net.IPNet
}

// ParseTrustedProxies parses a list of CIDR strings (e.g. "10.0.0.0/8",
// "172.16.0.0/12"). It returns an error if any CIDR is invalid.
func ParseTrustedProxies(cidrs []string) (TrustedProxies, error) {
	tp := TrustedProxies{}
	for _, cidr := range cidrs {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return TrustedProxies{}, err
		}
		tp.nets = append(tp.nets, network)
	}
	return tp, nil
}

// MustParseTrustedProxies is like ParseTrustedProxies but panics on error.
// Suitable for package-level initialization with known-good CIDRs.
func MustParseTrustedProxies(cidrs []string) TrustedProxies {
	tp, err := ParseTrustedProxies(cidrs)
	if err != nil {
		panic("clientip: invalid CIDR: " + err.Error())
	}
	return tp
}

// Contains reports whether ip is within any of the trusted ranges.
func (tp TrustedProxies) Contains(ip net.IP) bool {
	for _, network := range tp.nets {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// FromRequest returns the best-effort real client IP for r.
//
// Algorithm:
//  1. Parse the direct connection IP (RemoteAddr).
//  2. If the direct IP is in trusted, walk X-Forwarded-For right-to-left,
//     stopping at the first non-trusted IP — that is the client.
//  3. If nothing trusted, fall back to the raw RemoteAddr IP.
//
// X-Real-IP is considered only if X-Forwarded-For is absent and the direct
// connection is trusted.
func FromRequest(r *http.Request, trusted TrustedProxies) string {
	directIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		directIP = r.RemoteAddr
	}
	direct := net.ParseIP(directIP)

	if direct == nil || !trusted.Contains(direct) {
		return directIP
	}

	// Direct connection is a trusted proxy — examine X-Forwarded-For.
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		parts := strings.Split(xff, ",")
		// Walk right-to-left; the rightmost non-trusted IP is the real client.
		for i := len(parts) - 1; i >= 0; i-- {
			candidate := strings.TrimSpace(parts[i])
			ip := net.ParseIP(candidate)
			if ip == nil {
				continue
			}
			if !trusted.Contains(ip) {
				return candidate
			}
		}
	}

	// Fall back to X-Real-IP if present and direct is trusted.
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		if ip := net.ParseIP(xri); ip != nil && !trusted.Contains(ip) {
			return xri
		}
	}

	return directIP
}

// contextKey is the key for the client IP stored in the request context.
type contextKey struct{}

// FromContext retrieves the client IP previously set by the middleware.
// Returns empty string if not set.
func FromContext(r *http.Request) string {
	if v, ok := r.Context().Value(contextKey{}).(string); ok {
		return v
	}
	return ""
}
