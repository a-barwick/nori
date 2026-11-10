package computerd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Egress allowlist for sandboxed programs.
//
// A sandboxed program runs in its own network namespace with nothing but loopback, so by default it reaches nothing.
// When its package or server declares hosts, the namespace's first process is a shim (runSandboxShim) that brings
// loopback up, listens on 127.0.0.1:egressPort and forwards each connection over a unix socket in the sandbox
// directory to this proxy, which runs outside the namespace. The program gets HTTPS_PROXY and HTTP_PROXY pointing at
// the shim. The proxy accepts only CONNECT (and plain HTTP) to declared hosts, resolves names itself and refuses
// loopback, private and link-local addresses, so a declared name cannot be pointed at internal services. A program
// that ignores the proxy settings has no route anywhere.

const (
	egressPort       = 3128
	egressSocketName = ".nori-egress.sock"
	// egressFailMarker is what the shim prints when it cannot establish the forwarder; the program then never starts.
	egressFailMarker = "NORI-EGRESS-UNAVAILABLE:"
	egressFailExit   = 125
)

var hostPattern = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// validEgressHosts checks declared hosts: DNS names, optionally with a leading "*." for subdomains. No IP literals,
// ports or schemes.
func validEgressHosts(hosts []string) error {
	if len(hosts) == 0 || len(hosts) > 32 {
		return errors.New("an allowlist needs 1-32 hosts")
	}
	for _, h := range hosts {
		if !hostPattern.MatchString(strings.ToLower(h)) {
			return fmt.Errorf("%q is not a host name", h)
		}
	}
	return nil
}

func hostAllowed(host string, allow []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, a := range allow {
		a = strings.ToLower(a)
		if suffix, ok := strings.CutPrefix(a, "*."); ok {
			if strings.HasSuffix(host, "."+suffix) {
				return true
			}
		} else if host == a {
			return true
		}
	}
	return false
}

func publicIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast())
}

// egressProxy serves one sandbox run's allowlist on a unix socket.
type egressProxy struct {
	listener net.Listener
	allow    []string
	mu       sync.Mutex
	denied   []string
	wg       sync.WaitGroup
}

func startEgressProxy(socketPath string, allow []string) (*egressProxy, error) {
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	p := &egressProxy{listener: listener, allow: allow}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			p.wg.Add(1)
			go func() { defer p.wg.Done(); p.serve(conn) }()
		}
	}()
	return p, nil
}

func (p *egressProxy) close() []string {
	p.listener.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string{}, p.denied...)
}

func (p *egressProxy) deny(host string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.denied) < 20 {
		p.denied = append(p.denied, host)
	}
}

// dial resolves an allowed host itself and connects only to a public address.
func (p *egressProxy) dial(ctx context.Context, host, port string) (net.Conn, error) {
	if !hostAllowed(host, p.allow) {
		p.deny(host)
		return nil, fmt.Errorf("%s is not an allowed host", host)
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		if publicIP(a.IP) {
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(a.IP.String(), port))
		}
	}
	p.deny(host + " (non-public address)")
	return nil, fmt.Errorf("%s resolves only to non-public addresses", host)
}

func (p *egressProxy) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	reader := bufio.NewReader(conn)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if req.Method == http.MethodConnect {
		host, port, err := net.SplitHostPort(req.Host)
		if err != nil || (port != "443" && port != "80") {
			fmt.Fprint(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
			return
		}
		upstream, err := p.dial(ctx, host, port)
		if err != nil {
			fmt.Fprint(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
			return
		}
		defer upstream.Close()
		fmt.Fprint(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = conn.SetDeadline(time.Time{})
		pipe(conn, reader, upstream)
		return
	}
	// Plain HTTP through the proxy: an absolute URL to an allowed host on port 80.
	if req.URL == nil || req.URL.Scheme != "http" {
		fmt.Fprint(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
		return
	}
	port := req.URL.Port()
	if port == "" {
		port = "80"
	}
	upstream, err := p.dial(ctx, req.URL.Hostname(), port)
	if err != nil {
		fmt.Fprint(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer upstream.Close()
	req.RequestURI = ""
	req.Header.Del("Proxy-Connection")
	if err := req.Write(upstream); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	pipe(conn, reader, upstream)
}

func pipe(client net.Conn, buffered io.Reader, upstream net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, buffered); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
}
