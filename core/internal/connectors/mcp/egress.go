package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"nori/internal/state"
)

var (
	cgnatPrefix     = netip.MustParsePrefix("100.64.0.0/10")
	benchmarkPrefix = netip.MustParsePrefix("198.18.0.0/15")
	reservedPrefix  = netip.MustParsePrefix("240.0.0.0/4")
	doc2Prefix      = netip.MustParsePrefix("192.0.2.0/24")
	doc3Prefix      = netip.MustParsePrefix("198.51.100.0/24")
	doc4Prefix      = netip.MustParsePrefix("203.0.113.0/24")
	ietfPrefix      = netip.MustParsePrefix("192.0.0.0/24")
	docv6Prefix     = netip.MustParsePrefix("2001:db8::/32")
	nat64Prefix     = netip.MustParsePrefix("64:ff9b::/96")
	nat64LocalPref  = netip.MustParsePrefix("64:ff9b:1::/48")
)

// allowedAddr reports whether addr is a routable public unicast IP (or loopback when allowLoopback is true).
// It unmaps IPv4-in-IPv6 addresses so ::ffff:169.254.169.254 or ::ffff:127.0.0.1 are classified identically to IPv4.
func allowedAddr(addr netip.Addr, allowLoopback bool) bool {
	addr = addr.Unmap()
	if !addr.IsValid() {
		return false
	}
	if addr.IsLoopback() {
		return allowLoopback
	}
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() || addr.IsUnspecified() || addr.IsInterfaceLocalMulticast() {
		return false
	}
	if cgnatPrefix.Contains(addr) || benchmarkPrefix.Contains(addr) || reservedPrefix.Contains(addr) ||
		doc2Prefix.Contains(addr) || doc3Prefix.Contains(addr) || doc4Prefix.Contains(addr) ||
		ietfPrefix.Contains(addr) || docv6Prefix.Contains(addr) ||
		nat64Prefix.Contains(addr) || nat64LocalPref.Contains(addr) {
		return false
	}
	return true
}

func isInternalHostname(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return h == "metadata" || h == "metadata.google.internal" ||
		strings.HasSuffix(h, ".internal") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".localhost")
}

// validateRemoteURL checks that raw is a valid HTTPS URL (or loopback HTTP/HTTPS when allowLoopback is set)
// without embedded credentials or private/link-local/metadata hostnames or IP literals.
func validateRemoteURL(raw string, allowLoopback bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("%w: the endpoint must be an absolute URL without credentials or fragments", state.ErrInvalidResource)
	}
	host := strings.TrimSuffix(u.Hostname(), ".")
	if host == "" {
		return fmt.Errorf("%w: the endpoint must include a host", state.ErrInvalidResource)
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Unmap().IsLoopback() && allowLoopback && (u.Scheme == "http" || u.Scheme == "https") {
			return nil
		}
		if u.Scheme != "https" || !allowedAddr(addr, false) {
			return fmt.Errorf("%w: the endpoint must be a public HTTPS URL", state.ErrInvalidResource)
		}
		return nil
	}
	if strings.EqualFold(host, "localhost") && allowLoopback && (u.Scheme == "http" || u.Scheme == "https") {
		return nil
	}
	if u.Scheme != "https" || strings.EqualFold(host, "localhost") || isInternalHostname(host) {
		return fmt.Errorf("%w: the endpoint must be a public HTTPS URL", state.ErrInvalidResource)
	}
	return nil
}

// safeTransport wraps an underlying RoundTripper (or creates a fresh dial-guarded http.Transport) so every
// outbound request validates its URL and resolves DNS once at dial time, connecting only to permitted IPs.
type safeTransport struct {
	base          http.RoundTripper
	allowLoopback bool
}

func (t safeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil {
		return nil, fmt.Errorf("missing request URL")
	}
	if err := validateRemoteURL(req.URL.String(), t.allowLoopback); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}

func newDialGuardedTransport(allowLoopback bool) *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	// Keep http.ProxyFromEnvironment intact (used by operator egress controls and NORI_STACK_NO_MODEL=1),
	// while resolving DNS once at dial time and refusing connections to non-public addresses.
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	base.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		cleanHost := strings.TrimSuffix(host, ".")
		if parsed, err := netip.ParseAddr(cleanHost); err == nil {
			if !allowedAddr(parsed, allowLoopback) {
				return nil, fmt.Errorf("refusing connection to non-public address %s", parsed)
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(parsed.Unmap().String(), port))
		}
		if isInternalHostname(cleanHost) || (!allowLoopback && strings.EqualFold(cleanHost, "localhost")) {
			return nil, fmt.Errorf("refusing connection to internal host %s", cleanHost)
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", cleanHost)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("host %s resolved to no addresses", cleanHost)
		}
		// Reject the entire host if any resolved record points at a non-public IP (prevents round-robin DNS rebinding).
		for _, ip := range ips {
			if !allowedAddr(ip, allowLoopback) {
				return nil, fmt.Errorf("refusing connection to %s: resolves to non-public address %s", cleanHost, ip)
			}
		}
		var lastErr error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	return base
}

func (s *Service) outboundTransport() http.RoundTripper {
	if s.Client != nil && s.Client.Transport != nil {
		return safeTransport{base: s.Client.Transport, allowLoopback: s.LocalProcesses}
	}
	s.transportOnce.Do(func() {
		s.transport = safeTransport{base: newDialGuardedTransport(s.LocalProcesses), allowLoopback: s.LocalProcesses}
	})
	return s.transport
}

func (s *Service) newHTTPClient(timeout time.Duration) *http.Client {
	if s.Client != nil && s.Client.Timeout > 0 && timeout == 0 {
		timeout = s.Client.Timeout
	}
	return &http.Client{
		Timeout:       timeout,
		Transport:     s.outboundTransport(),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
