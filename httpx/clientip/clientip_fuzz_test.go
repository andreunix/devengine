package clientip

import (
	"net"
	"net/http/httptest"
	"testing"
)

func FuzzFromRequestForwardedChain(f *testing.F) {
	f.Add("203.0.113.10, 10.0.0.2", "198.51.100.5")
	f.Add("not-an-ip, 192.168.1.1", "")
	f.Add("2001:db8::1, fd00::2", "2001:db8::2")
	trusted := MustParseTrustedProxies([]string{"10.0.0.0/8", "192.168.0.0/16", "fd00::/8"})

	f.Fuzz(func(t *testing.T, forwardedFor, realIP string) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "10.0.0.1:4321"
		req.Header.Set("X-Forwarded-For", forwardedFor)
		req.Header.Set("X-Real-IP", realIP)
		got := FromRequest(req, trusted)
		if net.ParseIP(got) == nil {
			t.Fatalf("FromRequest returned a non-IP value %q", got)
		}
	})
}

func FuzzFromRequestNeverTrustsUnconfiguredPeer(f *testing.F) {
	f.Add("127.0.0.1", "10.0.0.1")
	f.Add("203.0.113.1, 10.0.0.1", "192.168.1.1")
	trusted := MustParseTrustedProxies([]string{"10.0.0.0/8"})

	f.Fuzz(func(t *testing.T, forwardedFor, realIP string) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "198.51.100.20:4321"
		req.Header.Set("X-Forwarded-For", forwardedFor)
		req.Header.Set("X-Real-IP", realIP)
		if got := FromRequest(req, trusted); got != "198.51.100.20" {
			t.Fatalf("untrusted peer influenced client IP: got %q", got)
		}
	})
}

func FuzzResolverForwardedNeverReturnsMalformedAddress(f *testing.F) {
	f.Add(`for=203.0.113.10, for="[fd00::2]:443"`)
	f.Add("for=unknown")
	f.Add(`for="unterminated`)
	resolver, err := New(
		[]string{"10.0.0.0/8", "fd00::/8"},
		[]string{HeaderForwarded},
	)
	if err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, forwarded string) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "10.0.0.1:4321"
		req.Header.Set(HeaderForwarded, forwarded)
		got := resolver.Resolve(req)
		if net.ParseIP(got) == nil {
			t.Fatalf("Resolver.Resolve returned a non-IP value %q", got)
		}
	})
}
