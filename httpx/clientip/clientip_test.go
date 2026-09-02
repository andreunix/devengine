package clientip_test

import (
	"net/http/httptest"
	"testing"

	"github.com/andreunix/devengine/httpx/clientip"
)

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
