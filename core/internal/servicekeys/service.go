// Package servicekeys manages per-user BYOK API keys for non-LLM external services (e.g. Exa search).
// Keys are encrypted at rest via secret.CredentialStore (rowstore) and never returned in plaintext
// from list, set, or delete operations.
package servicekeys

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"nori/internal/secret"
	"nori/internal/state"
)

const (
	ServiceExa = "exa"

	defaultExaValidationURL  = "https://api.exa.ai/search"
	defaultValidationTimeout = 10 * time.Second
)

var (
	ErrUnknownService   = errors.New("unknown service")
	ErrEmptyAPIKey      = errors.New("api_key is required")
	ErrInvalidAPIKey    = errors.New("invalid API key")
	ErrMissingAPIKey    = errors.New("service API key is not configured")
	ErrValidationFailed = errors.New("could not validate API key with service")
)

// ServiceDef describes a supported BYOK external service.
type ServiceDef struct {
	ID    string
	Label string
}

var serviceRegistry = []ServiceDef{
	{ID: ServiceExa, Label: "Exa"},
}

var serviceByID = func() map[string]ServiceDef {
	m := make(map[string]ServiceDef, len(serviceRegistry))
	for _, s := range serviceRegistry {
		m[s.ID] = s
	}
	return m
}()

// Services returns a copy of supported BYOK external services in display order.
func Services() []ServiceDef {
	out := make([]ServiceDef, len(serviceRegistry))
	copy(out, serviceRegistry)
	return out
}

// LookupService returns the service definition if id is supported.
func LookupService(id string) (ServiceDef, bool) {
	s, ok := serviceByID[strings.ToLower(strings.TrimSpace(id))]
	return s, ok
}

// CredentialID returns the secret.CredentialStore ID for service.
// Matches state.ValidResourceID and stays disjoint from modelkeys' "llm_" namespace.
func CredentialID(service string) string {
	return "svc_" + strings.ToLower(strings.TrimSpace(service))
}

// ServiceStatus is the safe, public projection of a user's key for one service.
// It intentionally omits the raw API key.
type ServiceStatus struct {
	Service     string     `json:"service"`
	Label       string     `json:"label"`
	Configured  bool       `json:"configured"`
	Last4       string     `json:"last4,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
	ValidatedAt *time.Time `json:"validated_at,omitempty"`
}

// ListResponse is the payload for GET /v1/settings/service-keys.
type ListResponse struct {
	Services []ServiceStatus `json:"services"`
}

type storedKey struct {
	APIKey      string     `json:"api_key"`
	Last4       string     `json:"last4"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ValidatedAt *time.Time `json:"validated_at,omitempty"`
}

type Service struct {
	Store                 secret.CredentialStore
	HTTPClient            *http.Client
	Now                   func() time.Time
	OnMutate              func(ctx context.Context, service string)
	ExaValidationEndpoint string
}

func (s *Service) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) httpClient() *http.Client {
	if s != nil && s.HTTPClient != nil {
		return s.HTTPClient
	}
	return &http.Client{Timeout: defaultValidationTimeout}
}

func (s *Service) notifyMutated(ctx context.Context, service string) {
	if s != nil && s.OnMutate != nil {
		s.OnMutate(ctx, service)
	}
}

func (s *Service) readRecord(ctx context.Context, service string) (storedKey, bool, error) {
	if s == nil || s.Store == nil {
		return storedKey{}, false, nil
	}
	cred, err := s.Store.Get(ctx, CredentialID(service))
	if errors.Is(err, secret.ErrCredentialNotFound) {
		return storedKey{}, false, nil
	}
	if err != nil {
		return storedKey{}, false, err
	}
	var rec storedKey
	if err := json.Unmarshal(cred.Data, &rec); err != nil {
		return storedKey{}, false, fmt.Errorf("decode stored service key: %w", err)
	}
	if strings.TrimSpace(rec.APIKey) == "" {
		return storedKey{}, false, nil
	}
	return rec, true, nil
}

// List returns masked status for every supported external service.
func (s *Service) List(ctx context.Context) (ListResponse, error) {
	out := ListResponse{
		Services: make([]ServiceStatus, 0, len(serviceRegistry)),
	}
	for _, def := range serviceRegistry {
		st, err := s.Status(ctx, def.ID)
		if err != nil {
			return ListResponse{}, err
		}
		out.Services = append(out.Services, st)
	}
	return out, nil
}

// Status returns masked status for one service.
func (s *Service) Status(ctx context.Context, service string) (ServiceStatus, error) {
	def, ok := LookupService(service)
	if !ok {
		return ServiceStatus{}, fmt.Errorf("%w: %s", ErrUnknownService, service)
	}
	rec, found, err := s.readRecord(ctx, def.ID)
	if err != nil {
		return ServiceStatus{}, err
	}
	if !found {
		return ServiceStatus{
			Service:    def.ID,
			Label:      def.Label,
			Configured: false,
		}, nil
	}
	updated := rec.UpdatedAt
	return ServiceStatus{
		Service:     def.ID,
		Label:       def.Label,
		Configured:  true,
		Last4:       rec.Last4,
		UpdatedAt:   &updated,
		ValidatedAt: rec.ValidatedAt,
	}, nil
}

// Set validates and encrypts the user's API key for service, replacing any existing key.
func (s *Service) Set(ctx context.Context, service, apiKey string) (ServiceStatus, error) {
	def, ok := LookupService(service)
	if !ok {
		return ServiceStatus{}, fmt.Errorf("%w: %s", ErrUnknownService, service)
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ServiceStatus{}, ErrEmptyAPIKey
	}
	credID := CredentialID(def.ID)
	if !state.ValidResourceID(credID) {
		return ServiceStatus{}, fmt.Errorf("%w: invalid credential ID %q", state.ErrInvalidResource, credID)
	}
	if s.Store == nil {
		return ServiceStatus{}, secret.ErrCredentialUnavailable
	}

	now := s.now()
	if err := s.validateKey(ctx, def.ID, apiKey); err != nil {
		return ServiceStatus{}, err
	}
	validatedAt := &now

	rec := storedKey{
		APIKey:      apiKey,
		Last4:       last4(apiKey),
		UpdatedAt:   now,
		ValidatedAt: validatedAt,
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return ServiceStatus{}, err
	}
	if err := s.saveCredential(ctx, credID, payload); err != nil {
		return ServiceStatus{}, err
	}
	s.notifyMutated(ctx, def.ID)

	updated := rec.UpdatedAt
	return ServiceStatus{
		Service:     def.ID,
		Label:       def.Label,
		Configured:  true,
		Last4:       rec.Last4,
		UpdatedAt:   &updated,
		ValidatedAt: rec.ValidatedAt,
	}, nil
}

// Delete removes the user's stored API key for service and fires the notify hook.
func (s *Service) Delete(ctx context.Context, service string) (ServiceStatus, error) {
	def, ok := LookupService(service)
	if !ok {
		return ServiceStatus{}, fmt.Errorf("%w: %s", ErrUnknownService, service)
	}
	if s.Store == nil {
		return ServiceStatus{}, secret.ErrCredentialUnavailable
	}
	if err := s.Store.Delete(ctx, CredentialID(def.ID)); err != nil {
		return ServiceStatus{}, err
	}
	s.notifyMutated(ctx, def.ID)
	return ServiceStatus{
		Service:    def.ID,
		Label:      def.Label,
		Configured: false,
	}, nil
}

// ResolveKey reads the decrypted BYOK key for service, returning ("", false, nil) if not configured.
func (s *Service) ResolveKey(ctx context.Context, service string) (string, bool, error) {
	def, ok := LookupService(service)
	if !ok {
		return "", false, ErrUnknownService
	}
	rec, found, err := s.readRecord(ctx, def.ID)
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, nil
	}
	return rec.APIKey, true, nil
}

func (s *Service) saveCredential(ctx context.Context, credID string, payload []byte) error {
	for attempt := 0; attempt < 2; attempt++ {
		var expected int64
		existing, err := s.Store.Get(ctx, credID)
		if err == nil {
			expected = existing.Version
		} else if !errors.Is(err, secret.ErrCredentialNotFound) {
			return err
		}
		_, err = s.Store.Replace(ctx, credID, expected, payload)
		if err == nil {
			return nil
		}
		if !errors.Is(err, secret.ErrCredentialConflict) {
			return err
		}
	}
	return secret.ErrCredentialConflict
}

func (s *Service) validateKey(ctx context.Context, service, apiKey string) error {
	if !secret.ValidBearerToken(apiKey) || len(apiKey) > 2048 {
		return ErrInvalidAPIKey
	}
	valCtx, cancel := context.WithTimeout(ctx, defaultValidationTimeout)
	defer cancel()

	switch service {
	case ServiceExa:
		endpoint := strings.TrimSpace(s.ExaValidationEndpoint)
		if endpoint == "" {
			endpoint = defaultExaValidationURL
		}
		body := []byte(`{"query":"test","numResults":1}`)
		req, err := http.NewRequestWithContext(valCtx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return ErrValidationFailed
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", apiKey)

		client := *s.httpClient()
		client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
		resp, err := client.Do(req)
		if err != nil {
			return ErrValidationFailed
		}
		defer func() {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
		}()

		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrInvalidAPIKey
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("%w: service returned HTTP %d", ErrValidationFailed, resp.StatusCode)
		}
		return nil
	default:
		return fmt.Errorf("%w: %s", ErrUnknownService, service)
	}
}

func last4(s string) string {
	if len(s) <= 4 {
		return ""
	}
	return s[len(s)-4:]
}
