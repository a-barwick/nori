package netguard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://relay.noribot.chat/oauth/token", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "1.1.1.1, 203.0.113.9")
	if got := ClientIP(req); got != "203.0.113.9" {
		t.Fatalf("ClientIP() = %q, want 203.0.113.9", got)
	}
}

func TestRateLimitIPKey(t *testing.T) {
	if got := RateLimitIPKey("203.0.113.9"); got != "203.0.113.9" {
		t.Fatalf("IPv4 key = %q, want 203.0.113.9", got)
	}
	a := RateLimitIPKey("2001:db8:abcd:1234::1")
	b := RateLimitIPKey("2001:db8:abcd:1234:ffff::99")
	if a != "2001:db8:abcd:1234::/64" || a != b {
		t.Fatalf("IPv6 /64 normalization failed: a=%q b=%q", a, b)
	}
}
