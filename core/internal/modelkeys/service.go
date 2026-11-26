// Package modelkeys manages per-user BYOK (Bring Your Own Key) LLM provider API keys.
// Keys are encrypted at rest via secret.CredentialStore and never returned in plaintext
// from list, set, or delete operations.
package modelkeys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"nori/internal/secret"
	"nori/internal/state"
)

const (
	ProviderOpenAI          = "openai"
	ProviderAnthropic       = "anthropic"
	ProviderGoogle          = "google"
	ProviderVercelAIGateway = "vercel_ai_gateway"
	ProviderOpenRouter      = "openrouter"
	AccessFailureCode       = "model_key_required"

	defaultValidationTimeout = 10 * time.Second
)

var (
	ErrUnknownProvider  = errors.New("unknown model provider")
	ErrEmptyAPIKey      = errors.New("api_key is required")
	ErrInvalidAPIKey    = errors.New("invalid API key")
	ErrMissingAPIKey    = errors.New("Add an API key in More → Models to use Chat and Work.")
	ErrModelAccess      = errors.New("Model access needs attention.")
	ErrValidationFailed = errors.New("could not validate API key with provider")
)

func IsAccessFailure(message string) bool {
	return strings.HasPrefix(message, ErrModelAccess.Error()) || strings.HasPrefix(message, ErrMissingAPIKey.Error())
}

// ProviderDef describes a supported BYOK model provider.
type ProviderDef struct {
	ID    string
	Label string
}

// providerRegistry is the ordered list of supported BYOK providers.
var providerRegistry = []ProviderDef{
	{ID: ProviderOpenAI, Label: "OpenAI"},
	{ID: ProviderAnthropic, Label: "Anthropic"},
	{ID: ProviderGoogle, Label: "Google"},
	{ID: ProviderVercelAIGateway, Label: "Vercel AI Gateway"},
	{ID: ProviderOpenRouter, Label: "OpenRouter"},
}

var providerByID = func() map[string]ProviderDef {
	m := make(map[string]ProviderDef, len(providerRegistry))
	for _, p := range providerRegistry {
		m[p.ID] = p
	}
	return m
}()

// Providers returns a copy of the registered BYOK providers in display order.
func Providers() []ProviderDef {
	out := make([]ProviderDef, len(providerRegistry))
	copy(out, providerRegistry)
	return out
}

// LookupProvider returns the provider definition if id is registered.
func LookupProvider(id string) (ProviderDef, bool) {
	p, ok := providerByID[id]
	return p, ok
}

// CredentialID returns the secret.CredentialStore ID for a provider.
// The format matches state.ValidResourceID without colons.
func CredentialID(provider string) string {
	return "llm_" + provider
}

// ProviderStatus is the safe, public projection of a user's key for one provider.
// It intentionally omits the raw API key.
type ProviderStatus struct {
	Provider    string     `json:"provider"`
	Label       string     `json:"label"`
	Configured  bool       `json:"configured"`
	Last4       string     `json:"last4,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
	ValidatedAt *time.Time `json:"validated_at,omitempty"`
}

// ListResponse is the response payload for GET /v1/settings/model-keys.
type ListResponse struct {
	BYOKRequired bool             `json:"byok_required"`
	Providers    []ProviderStatus `json:"providers"`
}

type storedKey struct {
	APIKey      string     `json:"api_key"`
	Last4       string     `json:"last4"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ValidatedAt *time.Time `json:"validated_at,omitempty"`
}

type Service struct {
	Store      secret.CredentialStore
	HTTPClient *http.Client
	Now        func() time.Time
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

func (s *Service) readRecord(ctx context.Context, provider string) (storedKey, bool, error) {
	if s == nil || s.Store == nil {
		return storedKey{}, false, nil
	}
	credID := CredentialID(provider)
	cred, err := s.Store.Get(ctx, credID)
	if errors.Is(err, secret.ErrCredentialNotFound) {
		return storedKey{}, false, nil
	}
	if err != nil {
		return storedKey{}, false, err
	}
	var rec storedKey
	if err := json.Unmarshal(cred.Data, &rec); err != nil {
		return storedKey{}, false, fmt.Errorf("decode stored model key: %w", err)
	}
	if strings.TrimSpace(rec.APIKey) == "" {
		return storedKey{}, false, nil
	}
	return rec, true, nil
}

// List returns the BYOK requirement flag and status for every registered provider.
func (s *Service) List(ctx context.Context) (ListResponse, error) {
	out := ListResponse{
		BYOKRequired: true,
		Providers:    make([]ProviderStatus, 0, len(providerRegistry)),
	}
	for _, def := range providerRegistry {
		rec, found, err := s.readRecord(ctx, def.ID)
		if err != nil {
			return ListResponse{}, err
		}
		if !found {
			out.Providers = append(out.Providers, ProviderStatus{
				Provider:   def.ID,
				Label:      def.Label,
				Configured: false,
			})
			continue
		}
		updated := rec.UpdatedAt
		out.Providers = append(out.Providers, ProviderStatus{
			Provider:    def.ID,
			Label:       def.Label,
			Configured:  true,
			Last4:       rec.Last4,
			UpdatedAt:   &updated,
			ValidatedAt: rec.ValidatedAt,
		})
	}
	return out, nil
}

// Set validates and encrypts the user's API key for provider, replacing any existing key.
func (s *Service) Set(ctx context.Context, provider, apiKey string) (ProviderStatus, error) {
	def, ok := LookupProvider(provider)
	if !ok {
		return ProviderStatus{}, fmt.Errorf("%w: %s", ErrUnknownProvider, provider)
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ProviderStatus{}, ErrEmptyAPIKey
	}
	credID := CredentialID(provider)
	if !state.ValidResourceID(credID) {
		return ProviderStatus{}, fmt.Errorf("%w: invalid credential ID %q", state.ErrInvalidResource, credID)
	}
	if s.Store == nil {
		return ProviderStatus{}, secret.ErrCredentialUnavailable
	}

	now := s.now()
	if err := s.validateKey(ctx, provider, apiKey); err != nil {
		return ProviderStatus{}, err
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
		return ProviderStatus{}, err
	}

	if err := s.saveCredential(ctx, credID, payload); err != nil {
		return ProviderStatus{}, err
	}

	updated := rec.UpdatedAt
	return ProviderStatus{
		Provider:    def.ID,
		Label:       def.Label,
		Configured:  true,
		Last4:       rec.Last4,
		UpdatedAt:   &updated,
		ValidatedAt: rec.ValidatedAt,
	}, nil
}

// Delete removes the user's stored API key for provider.
func (s *Service) Delete(ctx context.Context, provider string) (ProviderStatus, error) {
	def, ok := LookupProvider(provider)
	if !ok {
		return ProviderStatus{}, fmt.Errorf("%w: %s", ErrUnknownProvider, provider)
	}
	if s.Store == nil {
		return ProviderStatus{}, secret.ErrCredentialUnavailable
	}
	if err := s.Store.Delete(ctx, CredentialID(provider)); err != nil {
		return ProviderStatus{}, err
	}
	return ProviderStatus{
		Provider:   def.ID,
		Label:      def.Label,
		Configured: false,
	}, nil
}

// ResolveKey reads the user's current credential so revocations and rotations
// take effect across API and worker processes.
func (s *Service) ResolveKey(ctx context.Context, provider string) (string, error) {
	def, ok := LookupProvider(provider)
	if !ok {
		return "", ErrUnknownProvider
	}
	rec, found, err := s.readRecord(ctx, provider)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("%w Add a %s key or choose another model in More → Models.", ErrModelAccess, def.Label)
	}
	return rec.APIKey, nil
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

var apiKeyValue = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+=*$`)

func (s *Service) validateKey(ctx context.Context, provider, apiKey string) error {
	if !apiKeyValue.MatchString(apiKey) || len(apiKey) > 2048 {
		return ErrInvalidAPIKey
	}
	valCtx, cancel := context.WithTimeout(ctx, defaultValidationTimeout)
	defer cancel()

	endpoint := ""
	header, value := "Authorization", "Bearer "+apiKey
	switch provider {
	case ProviderOpenAI:
		endpoint = "https://api.openai.com/v1/me"
	case ProviderAnthropic:
		endpoint = "https://api.anthropic.com/v1/models?limit=1"
		header, value = "x-api-key", apiKey
	case ProviderGoogle:
		endpoint = "https://generativelanguage.googleapis.com/v1beta/models?pageSize=1"
		header, value = "x-goog-api-key", apiKey
	case ProviderVercelAIGateway:
		endpoint = "https://ai-gateway.vercel.sh/v1/credits"
	case ProviderOpenRouter:
		endpoint = "https://openrouter.ai/api/v1/key"
	}
	req, err := http.NewRequestWithContext(valCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ErrValidationFailed
	}
	req.Header.Set(header, value)
	if provider == ProviderAnthropic {
		req.Header.Set("anthropic-version", "2023-06-01")
	}

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
	case http.StatusBadRequest:
		if provider == ProviderGoogle {
			return ErrInvalidAPIKey
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: provider returned HTTP %d", ErrValidationFailed, resp.StatusCode)
	}
	return nil
}

func last4(s string) string {
	if len(s) <= 4 {
		return ""
	}
	return s[len(s)-4:]
}
