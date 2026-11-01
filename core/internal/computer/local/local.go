// Package local reaches a computer RPC that is already listening on a loopback
// address, such as cmd/computer-agent on a development machine. Nothing is
// started, stopped or tunnelled.
package local

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"

	"nori/internal/computer"
)

// New returns a Provider for the RPC at address, an HTTP origin on a literal
// loopback IP. The address is checked on each connection, so a process that
// never uses the computer does not need a valid one.
func New(address string) computer.Provider { return provider{address} }

type provider struct{ address string }

// Open and Attach are the same: there is no power state to change.
func (p provider) Open(ctx context.Context, run string) (*computer.Client, error) {
	return connect(ctx, p.address, run)
}

func (p provider) Attach(ctx context.Context, run string) (*computer.Client, error) {
	return connect(ctx, p.address, run)
}

func connect(ctx context.Context, address, run string) (*computer.Client, error) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("local computer URL must be an HTTP loopback origin")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("local computer URL must use a literal loopback IP")
	}
	u.Path = ""
	client := computer.NewClient(computer.Connection{BaseURL: u.String(), HTTP: &http.Client{
		Transport:     &http.Transport{}, // Never send local RPC through an environment proxy.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, run)
	health, err := client.Probe(ctx)
	switch {
	case err != nil && !errors.Is(err, computer.ErrUnhealthy):
		client.Close()
		return nil, fmt.Errorf("local computer unavailable: %w", err)
	case err != nil || !health.Ready:
		client.Close()
		return nil, errors.New("local computer is not ready")
	}
	return client, nil
}
