package netguard

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIP extracts the connecting client IP from an HTTP request.
// Behind Cloud Run's Google Front End, the connecting IP is appended as the
// rightmost X-Forwarded-For hop.
func ClientIP(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			if ip := strings.TrimSpace(parts[i]); ip != "" {
				return ip
			}
		}
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		return xri
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	if addr := strings.TrimSpace(r.RemoteAddr); addr != "" {
		return addr
	}
	return "unknown"
}

// RateLimitIPKey normalizes an IP for per-client rate limiting, collapsing IPv6
// addresses to their /64 prefix so a single host cannot rotate interface IDs.
func RateLimitIPKey(raw string) string {
	clean := strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(raw), "]"), "[")
	addr, err := netip.ParseAddr(clean)
	if err != nil {
		if clean == "" {
			return "unknown"
		}
		return clean
	}
	addr = addr.Unmap()
	if addr.Is6() {
		return netip.PrefixFrom(addr, 64).Masked().String()
	}
	return addr.String()
}
