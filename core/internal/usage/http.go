package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"time"

	"nori/internal/state"
)

type Config struct {
	Mode, Endpoint, Token string
	MaxCallsPerHour       int
	MaxUSDPerDay          float64
}

func (c Config) Validate() error {
	if c.MaxCallsPerHour < 0 {
		return errors.New("NORI_USER_MAX_CALLS_PER_HOUR must be a non-negative integer")
	}
	if math.IsNaN(c.MaxUSDPerDay) || math.IsInf(c.MaxUSDPerDay, 0) || c.MaxUSDPerDay < 0 {
		return errors.New("NORI_USER_MAX_USD_PER_DAY must be a non-negative dollar amount")
	}
	switch c.Mode {
	case "", "store":
		if c.Endpoint != "" || c.Token != "" {
			return errors.New("host usage settings require NORI_USAGE_METER=http")
		}
	case "http":
		u, e := url.Parse(c.Endpoint)
		if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("NORI_USAGE_ENDPOINT must be an HTTP endpoint without credentials, query or fragment")
		}
		ip := net.ParseIP(u.Hostname())
		loopback := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
		if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
			return errors.New("NORI_USAGE_ENDPOINT requires HTTPS or loopback HTTP")
		}
		if c.Token == "" {
			return errors.New("NORI_USAGE_TOKEN must be configured for host metering")
		}
	default:
		return errors.New("NORI_USAGE_METER must be store or http")
	}
	return nil
}

func Open(c Config, userID string, local Meter) (Meter, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	meter := local
	reader, _ := local.(WindowReader)
	_, hasReserver := local.(AtomicReserver)
	switch {
	case hasReserver:
		meter = WithBillingAdmission(meter, c.MaxCallsPerHour, c.MaxUSDPerDay, nil)
	case reader != nil && (c.MaxCallsPerHour > 0 || c.MaxUSDPerDay > 0):
		meter = WithAdmission(meter, reader, c.MaxCallsPerHour, c.MaxUSDPerDay, nil)
	}
	if c.Mode != "http" {
		return meter, nil
	}
	return WithHost(meter, &HTTP{Endpoint: c.Endpoint, Token: c.Token, UserID: userID, Client: &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}), nil
}

type HTTP struct {
	Endpoint, Token, UserID string
	Client                  *http.Client
}

func (h *HTTP) RecordUsage(ctx context.Context, u state.Usage) error {
	body, err := json.Marshal(struct {
		UserID string      `json:"user_id"`
		Usage  state.Usage `json:"usage"`
	}{h.UserID, u})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.Endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid host usage request")
	}
	req.Header.Set("Authorization", "Bearer "+h.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", h.UserID+":"+u.ID+":"+u.RequestStatus)
	res, err := h.Client.Do(req)
	if err != nil {
		return errors.New("host usage endpoint unavailable")
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("host usage endpoint returned HTTP %d", res.StatusCode)
	}
	return nil
}
