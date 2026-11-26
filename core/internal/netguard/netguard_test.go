package netguard

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync/atomic"
	"testing"
)

func TestAllowedAddr(t *testing.T) {
	cases := []struct {
		name              string
		addr              string
		wantNoLoopback    bool
		wantAllowLoopback bool
	}{
		{name: "ipv4_loopback_127_0_0_1", addr: "127.0.0.1", wantNoLoopback: false, wantAllowLoopback: true},
		{name: "ipv4_loopback_127_255_255_254", addr: "127.255.255.254", wantNoLoopback: false, wantAllowLoopback: true},

		{name: "rfc1918_10", addr: "10.0.0.1", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "rfc1918_172_16", addr: "172.16.0.1", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "rfc1918_172_31", addr: "172.31.255.254", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "rfc1918_192_168", addr: "192.168.1.1", wantNoLoopback: false, wantAllowLoopback: false},

		{name: "ipv4_link_local_metadata", addr: "169.254.169.254", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "ipv4_link_local_start", addr: "169.254.0.1", wantNoLoopback: false, wantAllowLoopback: false},

		{name: "ipv4_unspecified", addr: "0.0.0.0", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "ipv4_this_net", addr: "0.10.20.30", wantNoLoopback: false, wantAllowLoopback: false},

		{name: "cgnat_start", addr: "100.64.0.1", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "cgnat_end", addr: "100.127.255.254", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "public_before_cgnat", addr: "100.63.255.255", wantNoLoopback: true, wantAllowLoopback: true},
		{name: "public_after_cgnat", addr: "100.128.0.1", wantNoLoopback: true, wantAllowLoopback: true},

		{name: "six_to_four_anycast", addr: "192.88.99.1", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "benchmark_198_18", addr: "198.18.0.1", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "reserved_240", addr: "240.0.0.1", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "doc_test_net_1", addr: "192.0.2.1", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "doc_test_net_2", addr: "198.51.100.1", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "doc_test_net_3", addr: "203.0.113.1", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "ietf_protocol_assignments", addr: "192.0.0.1", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "ipv4_multicast", addr: "224.0.0.1", wantNoLoopback: false, wantAllowLoopback: false},

		{name: "ipv6_loopback", addr: "::1", wantNoLoopback: false, wantAllowLoopback: true},
		{name: "ipv6_ula_fc00", addr: "fc00::1", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "ipv6_ula_fd00", addr: "fd12:3456:789a::1", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "ipv6_link_local", addr: "fe80::1", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "ipv6_link_local_zoned", addr: "fe80::1%eth0", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "ipv6_unspecified", addr: "::", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "ipv6_multicast", addr: "ff02::1", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "ipv6_non_global_discard", addr: "100::1", wantNoLoopback: false, wantAllowLoopback: false},

		{name: "ipv4_mapped_loopback", addr: "::ffff:127.0.0.1", wantNoLoopback: false, wantAllowLoopback: true},
		{name: "ipv4_mapped_rfc1918", addr: "::ffff:10.0.0.1", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "ipv4_mapped_metadata", addr: "::ffff:169.254.169.254", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "ipv4_mapped_cgnat", addr: "::ffff:100.64.0.1", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "ipv4_mapped_6to4_anycast", addr: "::ffff:192.88.99.1", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "ipv4_mapped_public", addr: "::ffff:8.8.8.8", wantNoLoopback: true, wantAllowLoopback: true},

		{name: "nat64_well_known", addr: "64:ff9b::8.8.8.8", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "nat64_local", addr: "64:ff9b:1::1", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "teredo", addr: "2001:0000:4136:e378:8000:63bf:3fff:fdd2", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "six_to_four_ipv6", addr: "2002:0a00:0001::1", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "ipv6_documentation", addr: "2001:db8::1", wantNoLoopback: false, wantAllowLoopback: false},
		{name: "ipv6_public_with_zone", addr: "2606:4700:4700::1111%en0", wantNoLoopback: false, wantAllowLoopback: false},

		{name: "public_ipv4_cloudflare", addr: "1.1.1.1", wantNoLoopback: true, wantAllowLoopback: true},
		{name: "public_ipv4_google", addr: "8.8.8.8", wantNoLoopback: true, wantAllowLoopback: true},
		{name: "public_ipv4_example", addr: "93.184.216.34", wantNoLoopback: true, wantAllowLoopback: true},
		{name: "public_ipv6_cloudflare", addr: "2606:4700:4700::1111", wantNoLoopback: true, wantAllowLoopback: true},
		{name: "public_ipv6_google", addr: "2001:4860:4860::8888", wantNoLoopback: true, wantAllowLoopback: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ip := netip.MustParseAddr(tc.addr)
			if got := AllowedAddr(ip, false); got != tc.wantNoLoopback {
				t.Errorf("AllowedAddr(%s, false) = %v, want %v", tc.addr, got, tc.wantNoLoopback)
			}
			if got := AllowedAddr(ip, true); got != tc.wantAllowLoopback {
				t.Errorf("AllowedAddr(%s, true) = %v, want %v", tc.addr, got, tc.wantAllowLoopback)
			}
		})
	}

	t.Run("invalid_zero_addr", func(t *testing.T) {
		if AllowedAddr(netip.Addr{}, false) || AllowedAddr(netip.Addr{}, true) {
			t.Fatalf("zero netip.Addr must never be allowed")
		}
	})
}

func TestIsInternalHostnameAndValidateRemoteURL(t *testing.T) {
	hostCases := []struct {
		host string
		want bool
	}{
		{"metadata", true},
		{"metadata.", true},
		{"metadata..", true},
		{"metadata.google.internal", true},
		{"metadata.google.internal.", true},
		{"metadata.google.internal..", true},
		{"METADATA.GOOGLE.INTERNAL..", true},
		{"service.internal..", true},
		{"printer.local.", true},
		{"app.localhost..", true},
		{"api.example.com", false},
		{"api.example.com.", false},
	}
	for _, tc := range hostCases {
		if got := IsInternalHostname(tc.host); got != tc.want {
			t.Errorf("IsInternalHostname(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}

	urlCases := []struct {
		name          string
		raw           string
		allowLoopback bool
		wantOK        bool
	}{
		{name: "public_https", raw: "https://api.example.com/v1", allowLoopback: false, wantOK: true},
		{name: "public_https_ip", raw: "https://1.1.1.1/dns-query", allowLoopback: false, wantOK: true},
		{name: "public_http_rejected", raw: "http://api.example.com/v1", allowLoopback: false, wantOK: false},
		{name: "public_http_rejected_even_with_loopback", raw: "http://api.example.com/v1", allowLoopback: true, wantOK: false},
		{name: "localhost_trailing_dots_blocked", raw: "https://localhost../secret", allowLoopback: false, wantOK: false},
		{name: "localhost_trailing_dots_http_blocked", raw: "http://localhost..:8080/secret", allowLoopback: false, wantOK: false},
		{name: "localhost_trailing_dots_allowed_when_loopback", raw: "http://localhost..:8080/ok", allowLoopback: true, wantOK: true},
		{name: "metadata_trailing_dots_blocked", raw: "https://metadata.google.internal../computeMetadata/v1/", allowLoopback: false, wantOK: false},
		{name: "metadata_trailing_dots_blocked_even_with_loopback", raw: "https://metadata.google.internal../computeMetadata/v1/", allowLoopback: true, wantOK: false},
		{name: "loopback_ipv4_blocked", raw: "http://127.0.0.1:8080/", allowLoopback: false, wantOK: false},
		{name: "loopback_ipv4_allowed", raw: "http://127.0.0.1:8080/", allowLoopback: true, wantOK: true},
		{name: "loopback_ipv6_allowed", raw: "http://[::1]:8080/", allowLoopback: true, wantOK: true},
		{name: "ipv4_mapped_metadata_blocked", raw: "https://[::ffff:169.254.169.254]/latest/meta-data/", allowLoopback: true, wantOK: false},
		{name: "cgnat_blocked", raw: "https://100.64.0.1/", allowLoopback: true, wantOK: false},
		{name: "credentials_rejected", raw: "https://user:pass@api.example.com/", allowLoopback: false, wantOK: false},
		{name: "fragment_rejected", raw: "https://api.example.com/#frag", allowLoopback: false, wantOK: false},
		{name: "relative_rejected", raw: "/v1/items", allowLoopback: false, wantOK: false},
		{name: "dots_only_host_rejected", raw: "https://.../path", allowLoopback: false, wantOK: false},
	}

	for _, tc := range urlCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRemoteURL(tc.raw, tc.allowLoopback)
			if tc.wantOK && err != nil {
				t.Errorf("ValidateRemoteURL(%q, %v) = %v, want nil", tc.raw, tc.allowLoopback, err)
			}
			if !tc.wantOK && err == nil {
				t.Errorf("ValidateRemoteURL(%q, %v) = nil, want error", tc.raw, tc.allowLoopback)
			}
		})
	}
}

func TestNewTransportDisablesProxyAndGuardsDial(t *testing.T) {
	roots := x509.NewCertPool()
	rt := NewTransport(true, roots)
	guarded, ok := rt.(guardedRoundTripper)
	if !ok {
		t.Fatalf("NewTransport returned %T, want guardedRoundTripper", rt)
	}
	ht, ok := guarded.base.(*http.Transport)
	if !ok {
		t.Fatalf("guarded.base is %T, want *http.Transport", guarded.base)
	}
	if ht.Proxy != nil {
		t.Fatalf("NewTransport must set Proxy == nil to ignore HTTP_PROXY/HTTPS_PROXY")
	}
	if ht.TLSClientConfig == nil || ht.TLSClientConfig.RootCAs != roots || ht.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("unexpected TLSClientConfig: %+v", ht.TLSClientConfig)
	}

	t.Setenv("HTTP_PROXY", "http://198.51.100.1:8888")
	t.Setenv("http_proxy", "http://198.51.100.1:8888")
	t.Setenv("HTTPS_PROXY", "http://198.51.100.1:8888")
	t.Setenv("NO_PROXY", "")

	var dialedAddr atomic.Value
	ht.DialContext = func(_ context.Context, _, addr string) (net.Conn, error) {
		dialedAddr.Store(addr)
		clientConn, serverConn := net.Pipe()
		go func() {
			defer serverConn.Close()
			buf := make([]byte, 4096)
			_, _ = serverConn.Read(buf)
			_, _ = io.WriteString(serverConn, "HTTP/1.1 200 OK\r\nContent-Length: 9\r\nConnection: close\r\n\r\ndirect-ok")
		}()
		return clientConn, nil
	}
	client := &http.Client{Transport: rt}
	resp, err := client.Get("http://127.0.0.1:8080/check")
	if err != nil {
		t.Fatalf("direct loopback Get failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "direct-ok" {
		t.Fatalf("body = %q, want direct-ok", string(body))
	}
	if got, _ := dialedAddr.Load().(string); got != "127.0.0.1:8080" {
		t.Fatalf("dialed %q, want target 127.0.0.1:8080 (HTTP_PROXY must be ignored)", got)
	}

	strictRT := NewTransport(false, nil)
	strictClient := &http.Client{Transport: strictRT}
	if _, err := strictClient.Get("http://127.0.0.1:8080/check"); err == nil {
		t.Fatalf("strict transport allowed loopback request")
	}

	strictHT := strictRT.(guardedRoundTripper).base.(*http.Transport)
	blockedDials := []string{
		"127.0.0.1:80",
		"169.254.169.254:80",
		"100.64.0.1:443",
		"[::ffff:10.0.0.1]:443",
		"localhost..:443",
		"metadata.google.internal..:80",
		"...:443",
	}
	for _, addr := range blockedDials {
		if conn, err := strictHT.DialContext(context.Background(), "tcp", addr); err == nil {
			_ = conn.Close()
			t.Errorf("strict DialContext(%q) succeeded, want refusal", addr)
		}
	}

	var baseCalled atomic.Bool
	wrapped := WrapTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
		baseCalled.Store(true)
		return nil, nil
	}), false)
	if _, err := wrapped.RoundTrip(&http.Request{}); err == nil {
		t.Fatalf("WrapTransport allowed nil URL")
	}
	if baseCalled.Load() {
		t.Fatalf("base RoundTripper was called despite nil URL")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
