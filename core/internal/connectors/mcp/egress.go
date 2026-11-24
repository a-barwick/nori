package mcp

import (
	"fmt"
	"net/http"
	"time"

	"nori/internal/netguard"
	"nori/internal/state"
)

func validateRemoteURL(raw string, allowLoopback bool) error {
	if err := netguard.ValidateRemoteURL(raw, allowLoopback); err != nil {
		return fmt.Errorf("%w: %v", state.ErrInvalidResource, err)
	}
	return nil
}

func (s *Service) outboundTransport() http.RoundTripper {
	if s.Client != nil && s.Client.Transport != nil {
		return netguard.WrapTransport(s.Client.Transport, s.LocalProcesses)
	}
	s.transportOnce.Do(func() {
		s.transport = netguard.NewTransport(s.LocalProcesses, nil)
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
