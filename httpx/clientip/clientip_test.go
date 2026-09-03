package clientip_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/andreunix/devengine/httpx/clientip"
)

func TestResolverHonorsConfiguredHeaderPriority(t *testing.T) {
	resolver, err := clientip.New(
		[]string{"10.0.0.0/8"},
		[]string{clientip.HeaderCFConnectingIP, clientip.HeaderXForwardedFor},
	)
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:9000"
	r.Header.Set(clientip.HeaderCFConnectingIP, "203.0.113.8")
	r.Header.Set(clientip.HeaderXForwardedFor, "198.51.100.4")

	if got := resolver.Resolve(r); got != "203.0.113.8" {
		t.Fatalf("expected configured priority to select CF header, got %q", got)
	}
}

func TestResolverParsesForwardedChainRightToLeft(t *testing.T) {
	resolver, err := clientip.New(
		[]string{"10.0.0.0/8", "fd00::/8"},
		[]string{clientip.HeaderForwarded},
	)
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:9000"
	r.Header.Set(clientip.HeaderForwarded, `for=203.0.113.9;proto=https, for="[fd00::2]:443"`)

	if got := resolver.Resolve(r); got != "203.0.113.9" {
		t.Fatalf("expected first untrusted hop, got %q", got)
	}
}

func TestResolverFailsClosedOnMalformedPreferredHeader(t *testing.T) {
	resolver, err := clientip.New(
		[]string{"10.0.0.0/8"},
		[]string{clientip.HeaderForwarded, clientip.HeaderXForwardedFor},
	)
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:9000"
	r.Header.Set(clientip.HeaderForwarded, "for=unknown")
	r.Header.Set(clientip.HeaderXForwardedFor, "198.51.100.7")

	if got := resolver.Resolve(r); got != "10.0.0.1" {
		t.Fatalf("expected malformed preferred header to fall back to peer, got %q", got)
	}
}

func TestResolverIgnoresHeadersFromUntrustedPeer(t *testing.T) {
	resolver, err := clientip.New(
		[]string{"10.0.0.0/8"},
		[]string{clientip.HeaderCFConnectingIP},
	)
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "198.51.100.20:9000"
	r.Header.Set(clientip.HeaderCFConnectingIP, "1.1.1.1")

	if got := resolver.Resolve(r); got != "198.51.100.20" {
		t.Fatalf("untrusted peer influenced client IP: got %q", got)
	}
}

func TestResolverIgnoresSpoofedXFFFromUntrustedPeer(t *testing.T) {
	resolver, err := clientip.New(
		[]string{"10.0.0.0/8"},
		[]string{clientip.HeaderXForwardedFor},
	)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "198.51.100.20:9000"
	r.Header.Set(clientip.HeaderXForwardedFor, "1.1.1.1")
	if got := resolver.Resolve(r); got != "198.51.100.20" {
		t.Fatalf("spoofed XFF influenced client IP: got %q", got)
	}
}

func TestResolverRejectsDuplicateSingleValueHeader(t *testing.T) {
	resolver, err := clientip.New(
		[]string{"10.0.0.0/8"},
		[]string{clientip.HeaderCFConnectingIP},
	)
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:9000"
	r.Header.Add(clientip.HeaderCFConnectingIP, "203.0.113.1")
	r.Header.Add(clientip.HeaderCFConnectingIP, "203.0.113.2")

	if got := resolver.Resolve(r); got != "10.0.0.1" {
		t.Fatalf("expected safe fallback for duplicate header, got %q", got)
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		cidrs   []string
		headers []string
	}{
		{name: "invalid CIDR", cidrs: []string{"invalid"}, headers: []string{clientip.HeaderXForwardedFor}},
		{name: "unknown header", cidrs: []string{"10.0.0.0/8"}, headers: []string{"Client-IP"}},
		{name: "duplicate header", cidrs: []string{"10.0.0.0/8"}, headers: []string{clientip.HeaderForwarded, "forwarded"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := clientip.New(tt.cidrs, tt.headers); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func TestResolverHandlesRequiredAddressAndChainCases(t *testing.T) {
	tests := []struct {
		name    string
		trusted []string
		header  string
		values  []string
		remote  string
		want    string
	}{
		{name: "simple XFF", trusted: []string{"10.0.0.0/8"}, header: clientip.HeaderXForwardedFor, values: []string{"203.0.113.2"}, remote: "10.0.0.1:80", want: "203.0.113.2"},
		{name: "untrusted intermediary", trusted: []string{"10.0.0.0/8"}, header: clientip.HeaderXForwardedFor, values: []string{"203.0.113.2, 198.51.100.9, 10.0.0.2"}, remote: "10.0.0.1:80", want: "198.51.100.9"},
		{name: "Forwarded unquoted", trusted: []string{"10.0.0.0/8"}, header: clientip.HeaderForwarded, values: []string{"for=203.0.113.3"}, remote: "10.0.0.1:80", want: "203.0.113.3"},
		{name: "Forwarded quoted IPv4 with port", trusted: []string{"10.0.0.0/8"}, header: clientip.HeaderForwarded, values: []string{`for="203.0.113.4:8443"`}, remote: "10.0.0.1:80", want: "203.0.113.4"},
		{name: "Forwarded quoted IPv6", trusted: []string{"fd00::/8"}, header: clientip.HeaderForwarded, values: []string{`for="[2001:db8::5]:443";proto=https`}, remote: "[fd00::1]:80", want: "2001:db8::5"},
		{name: "IPv6 RemoteAddr without port", trusted: nil, header: clientip.HeaderXForwardedFor, values: []string{"203.0.113.3"}, remote: "2001:db8::8", want: "2001:db8::8"},
		{name: "all hops trusted", trusted: []string{"10.0.0.0/8"}, header: clientip.HeaderXForwardedFor, values: []string{"10.0.0.3,10.0.0.2"}, remote: "10.0.0.1:80", want: "10.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver, err := clientip.New(tt.trusted, []string{tt.header})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remote
			for _, value := range tt.values {
				r.Header.Add(tt.header, value)
			}
			if got := resolver.Resolve(r); got != tt.want {
				t.Fatalf("Resolve()=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolverReturnsEmptyForInvalidRemoteAddr(t *testing.T) {
	resolver, err := clientip.New([]string{"10.0.0.0/8"}, []string{clientip.HeaderXForwardedFor})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "not-an-ip"
	r.Header.Set(clientip.HeaderXForwardedFor, "203.0.113.1")
	if got := resolver.Resolve(r); got != "" {
		t.Fatalf("Resolve()=%q, want empty result", got)
	}
}

func TestResolverFailsClosedForInvalidChains(t *testing.T) {
	tests := []struct {
		name   string
		header string
		values []string
	}{
		{name: "invalid XFF hop", header: clientip.HeaderXForwardedFor, values: []string{"203.0.113.1, invalid"}},
		{name: "empty XFF hop", header: clientip.HeaderXForwardedFor, values: []string{"203.0.113.1,,10.0.0.2"}},
		{name: "Forwarded unknown", header: clientip.HeaderForwarded, values: []string{"for=unknown"}},
		{name: "Forwarded obfuscated", header: clientip.HeaderForwarded, values: []string{"for=_hidden"}},
		{name: "Forwarded missing for", header: clientip.HeaderForwarded, values: []string{"proto=https"}},
		{name: "Forwarded duplicate for", header: clientip.HeaderForwarded, values: []string{"for=203.0.113.1;for=203.0.113.2"}},
		{name: "Forwarded broken quote", header: clientip.HeaderForwarded, values: []string{`for="[2001:db8::1]`}},
		{name: "multiple X-Real-IP", header: clientip.HeaderXRealIP, values: []string{"203.0.113.1", "203.0.113.2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver, err := clientip.New([]string{"10.0.0.0/8"}, []string{tt.header})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = "10.0.0.1:80"
			for _, value := range tt.values {
				r.Header.Add(tt.header, value)
			}
			if got := resolver.Resolve(r); got != "10.0.0.1" {
				t.Fatalf("malformed header influenced result: %q", got)
			}
		})
	}
}

func TestDirectIPNoProxy(t *testing.T) {
	tp, _ := clientip.ParseTrustedProxies(nil)
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.5:12345"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")

	got := clientip.FromRequest(r, tp)
	// No trusted proxies → must use direct IP, XFF ignored.
	if got != "203.0.113.5" {
		t.Fatalf("expected direct IP, got %q", got)
	}
}

func TestTrustedProxyReadsXFF(t *testing.T) {
	tp := clientip.MustParseTrustedProxies([]string{"10.0.0.0/8"})
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:9000" // trusted proxy
	r.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.1")

	got := clientip.FromRequest(r, tp)
	// XFF: rightmost non-trusted is 203.0.113.5
	if got != "203.0.113.5" {
		t.Fatalf("expected real client IP, got %q", got)
	}
}

func TestSpoofingXFFFromUntrustedSource(t *testing.T) {
	tp := clientip.MustParseTrustedProxies([]string{"10.0.0.0/8"})
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.99:1234" // NOT trusted
	r.Header.Set("X-Forwarded-For", "1.1.1.1")

	got := clientip.FromRequest(r, tp)
	// Direct connection is not trusted → ignore XFF entirely.
	if got != "203.0.113.99" {
		t.Fatalf("expected direct IP (spoofing blocked), got %q", got)
	}
}

func TestInvalidCIDRReturnsError(t *testing.T) {
	_, err := clientip.ParseTrustedProxies([]string{"not-a-cidr"})
	if err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
}

func TestXRealIPFallback(t *testing.T) {
	tp := clientip.MustParseTrustedProxies([]string{"10.0.0.0/8"})
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:9000"
	// No XFF, but X-Real-IP present
	r.Header.Set("X-Real-IP", "203.0.113.7")

	got := clientip.FromRequest(r, tp)
	if got != "203.0.113.7" {
		t.Fatalf("expected X-Real-IP fallback, got %q", got)
	}
}
