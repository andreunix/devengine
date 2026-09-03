// Package clientip resolves client IPs without trusting forwarding headers
// received directly from untrusted peers.
package clientip

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

const (
	HeaderForwarded      = "Forwarded"
	HeaderXForwardedFor  = "X-Forwarded-For"
	HeaderXRealIP        = "X-Real-IP"
	HeaderCFConnectingIP = "CF-Connecting-IP"
	maxHeaderBytes       = 8 << 10
	maxHops              = 32
)

var supportedHeaders = map[string]string{
	"forwarded": HeaderForwarded, "x-forwarded-for": HeaderXForwardedFor,
	"x-real-ip": HeaderXRealIP, "cf-connecting-ip": HeaderCFConnectingIP,
}

// Resolver is an immutable, concurrency-safe client IP resolver.
type Resolver struct {
	trusted  []netip.Prefix
	priority []string
}

// New constructs a resolver. Header order defines precedence.
func New(trustedCIDRs, priority []string) (*Resolver, error) {
	if len(priority) == 0 {
		return nil, errors.New("clientip: header priority must not be empty")
	}
	trusted := make([]netip.Prefix, 0, len(trustedCIDRs))
	seenPrefixes := make(map[netip.Prefix]struct{}, len(trustedCIDRs))
	for _, raw := range trustedCIDRs {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("clientip: invalid trusted CIDR %q: %w", raw, err)
		}
		prefix = prefix.Masked()
		if _, exists := seenPrefixes[prefix]; exists {
			return nil, fmt.Errorf("clientip: duplicate trusted CIDR %q", raw)
		}
		seenPrefixes[prefix] = struct{}{}
		trusted = append(trusted, prefix)
	}
	headers := make([]string, 0, len(priority))
	seenHeaders := make(map[string]struct{}, len(priority))
	for _, raw := range priority {
		key := strings.ToLower(strings.TrimSpace(raw))
		header, ok := supportedHeaders[key]
		if !ok {
			return nil, fmt.Errorf("clientip: unsupported header %q", raw)
		}
		if _, exists := seenHeaders[key]; exists {
			return nil, fmt.Errorf("clientip: duplicate header %q", raw)
		}
		seenHeaders[key] = struct{}{}
		headers = append(headers, header)
	}
	return &Resolver{trusted: trusted, priority: headers}, nil
}

// Resolve returns the direct peer unless it is trusted and a configured
// forwarding header contains a fully valid chain. Malformed headers fail
// closed instead of allowing a lower-priority header to influence the result.
func (r *Resolver) Resolve(req *http.Request) string {
	if req == nil {
		return ""
	}
	peer, peerText, ok := parseRemoteAddr(req.RemoteAddr)
	if !ok || !r.contains(peer) {
		return peerText
	}
	for _, header := range r.priority {
		values := req.Header.Values(header)
		if len(values) == 0 {
			continue
		}
		chain, valid := parseHeader(header, values)
		if !valid {
			return peerText
		}
		for i := len(chain) - 1; i >= 0; i-- {
			if !r.contains(chain[i]) {
				return chain[i].String()
			}
		}
		return peerText
	}
	return peerText
}

func (r *Resolver) contains(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, prefix := range r.trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func parseRemoteAddr(raw string) (netip.Addr, string, bool) {
	if addrPort, err := netip.ParseAddrPort(raw); err == nil {
		addr := addrPort.Addr()
		if addr.Zone() != "" {
			return netip.Addr{}, "", false
		}
		addr = addr.Unmap()
		return addr, addr.String(), true
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil || addr.Zone() != "" {
		return netip.Addr{}, "", false
	}
	addr = addr.Unmap()
	return addr, addr.String(), true
}

func parseHeader(header string, values []string) ([]netip.Addr, bool) {
	if header == HeaderCFConnectingIP || header == HeaderXRealIP {
		if len(values) != 1 || strings.Contains(values[0], ",") {
			return nil, false
		}
		addr, ok := parseBareAddr(strings.TrimSpace(values[0]))
		return []netip.Addr{addr}, ok
	}
	if headerBytes(values) > maxHeaderBytes {
		return nil, false
	}
	joined := strings.Join(values, ",")
	if header == HeaderForwarded {
		return parseForwarded(joined)
	}
	parts := strings.Split(joined, ",")
	if len(parts) == 0 || len(parts) > maxHops {
		return nil, false
	}
	chain := make([]netip.Addr, 0, len(parts))
	for _, part := range parts {
		addr, ok := parseBareAddr(strings.TrimSpace(part))
		if !ok {
			return nil, false
		}
		chain = append(chain, addr)
	}
	return chain, true
}

func headerBytes(values []string) int {
	total := 0
	for _, value := range values {
		total += len(value)
	}
	return total
}

func parseBareAddr(raw string) (netip.Addr, bool) {
	if raw == "" || strings.ContainsAny(raw, "[]") {
		return netip.Addr{}, false
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil || addr.Zone() != "" || addr.IsUnspecified() {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func parseForwarded(raw string) ([]netip.Addr, bool) {
	elements, ok := splitOutsideQuotes(raw, ',')
	if !ok || len(elements) == 0 || len(elements) > maxHops {
		return nil, false
	}
	chain := make([]netip.Addr, 0, len(elements))
	for _, element := range elements {
		params, valid := splitOutsideQuotes(element, ';')
		if !valid || len(params) == 0 {
			return nil, false
		}
		var addr netip.Addr
		found := false
		for _, param := range params {
			name, value, present := strings.Cut(strings.TrimSpace(param), "=")
			if !present || strings.TrimSpace(name) == "" || strings.TrimSpace(value) == "" {
				return nil, false
			}
			if strings.EqualFold(strings.TrimSpace(name), "for") {
				if found {
					return nil, false
				}
				addr, valid = parseForwardedNode(strings.TrimSpace(value))
				if !valid {
					return nil, false
				}
				found = true
			}
		}
		if !found {
			return nil, false
		}
		chain = append(chain, addr)
	}
	return chain, true
}

func splitOutsideQuotes(raw string, separator byte) ([]string, bool) {
	var parts []string
	start, quoted, escaped := 0, false, false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == '\r' || c == '\n' || (c < 0x20 && c != '\t') || c == 0x7f {
			return nil, false
		}
		if escaped {
			escaped = false
			continue
		}
		if quoted && c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
			continue
		}
		if c == separator && !quoted {
			part := strings.TrimSpace(raw[start:i])
			if part == "" {
				return nil, false
			}
			parts = append(parts, part)
			start = i + 1
		}
	}
	if quoted || escaped {
		return nil, false
	}
	last := strings.TrimSpace(raw[start:])
	if last == "" {
		return nil, false
	}
	return append(parts, last), true
}

func parseForwardedNode(raw string) (netip.Addr, bool) {
	quoted := len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"'
	if strings.HasPrefix(raw, "\"") != strings.HasSuffix(raw, "\"") {
		return netip.Addr{}, false
	}
	if quoted {
		var b strings.Builder
		for i := 1; i < len(raw)-1; i++ {
			if raw[i] == '\\' {
				i++
				if i >= len(raw)-1 {
					return netip.Addr{}, false
				}
			}
			b.WriteByte(raw[i])
		}
		raw = b.String()
	}
	if strings.EqualFold(raw, "unknown") || strings.HasPrefix(raw, "_") || raw == "" {
		return netip.Addr{}, false
	}
	if strings.HasPrefix(raw, "[") {
		if addrPort, err := netip.ParseAddrPort(raw); err == nil {
			addr := addrPort.Addr()
			return addr.Unmap(), addr.Is6() && addr.Zone() == "" && !addr.IsUnspecified()
		}
		if !strings.HasSuffix(raw, "]") {
			return netip.Addr{}, false
		}
		addr, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]"))
		return addr.Unmap(), err == nil && addr.Is6() && addr.Zone() == "" && !addr.IsUnspecified()
	}
	if quoted {
		if addrPort, err := netip.ParseAddrPort(raw); err == nil {
			addr := addrPort.Addr()
			return addr.Unmap(), addr.Is4() && !addr.IsUnspecified()
		}
	}
	addr, ok := parseBareAddr(raw)
	return addr, ok && addr.Is4()
}

// TrustedProxies is the legacy trusted proxy representation.
// Deprecated: use Resolver and New.
type TrustedProxies struct{ nets []*net.IPNet }

// ParseTrustedProxies parses legacy trusted CIDRs.
// Deprecated: use New.
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
// Deprecated: use New.
func MustParseTrustedProxies(cidrs []string) TrustedProxies {
	tp, err := ParseTrustedProxies(cidrs)
	if err != nil {
		panic("clientip: invalid CIDR: " + err.Error())
	}
	return tp
}

// Contains reports whether ip is trusted.
// Deprecated: use Resolver.Resolve.
func (tp TrustedProxies) Contains(ip net.IP) bool {
	for _, network := range tp.nets {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// FromRequest resolves using the legacy XFF then X-Real-IP behavior.
// Deprecated: construct a Resolver with New and call Resolve.
func FromRequest(req *http.Request, trusted TrustedProxies) string {
	directIP, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		directIP = req.RemoteAddr
	}
	direct := net.ParseIP(directIP)
	if direct == nil || !trusted.Contains(direct) {
		return directIP
	}
	if xff := req.Header.Get(HeaderXForwardedFor); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			candidate := strings.TrimSpace(parts[i])
			if ip := net.ParseIP(candidate); ip != nil && !trusted.Contains(ip) {
				return candidate
			}
		}
	}
	if xri := strings.TrimSpace(req.Header.Get(HeaderXRealIP)); xri != "" {
		if ip := net.ParseIP(xri); ip != nil && !trusted.Contains(ip) {
			return xri
		}
	}
	return directIP
}

type contextKey struct{}

// FromContext retrieves a client IP previously stored in ctx.
func FromContext(ctx context.Context) string {
	if value, ok := ctx.Value(contextKey{}).(string); ok {
		return value
	}
	return ""
}

// WithContext returns a context carrying ip.
func WithContext(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, contextKey{}, ip)
}
