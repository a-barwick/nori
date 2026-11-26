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
	"strings"
	"sync"
	"time"

	"nori/internal/secret"
	"nori/internal/state"
)

const (
	ProviderVercelAIGateway = "vercel_ai_gateway"

	defaultCacheTTL          = 5 * time.Second
	defaultValidationTimeout = 10 * time.Second
)

var (
	ErrUnknownProvider  = errors.New("unknown model provider")
	ErrEmptyAPIKey      = errors.New("api_key is required")
	ErrInvalidAPIKey    = errors.New("invalid API key")
	ErrMissingAPIKey    = errors.New("missing_api_key: configure a Vercel AI Gateway API key in Settings")
	ErrValidationFailed = errors.New("could not validate API key with provider")
)

// ProviderDef describes a supported BYOK model provider.
type ProviderDef struct {
	ID    string
	Label string
}

// providerRegistry is the ordered list of supported BYOK providers.
// Add future providers (e.g. "anthropic", "openai", "openrouter") here.
var providerRegistry = []ProviderDef{
	{ID: ProviderVercelAIGateway, Label: "Vercel AI Gateway"},
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

type cacheEntry struct {
	found     bool
	record    storedKey
	fetchedAt time.Time
}

// Service manages a single user's BYOK model keys.
type Service struct {
	Store        secret.CredentialStore
	GatewayURL   string
	BYOKRequired bool
	HTTPClient   *http.Client
	Now          func() time.Time

	mu    sync.RWMutex
	cache map[string]cacheEntry
	gen   map[string]uint64
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

func (s *Service) getCachedOrGen(provider string) (cacheEntry, bool, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	gen := s.gen[provider]
	if s.cache == nil {
		return cacheEntry{}, false, gen
	}
	entry, ok := s.cache[provider]
	if !ok {
		return cacheEntry{}, false, gen
	}
	if s.now().Sub(entry.fetchedAt) > defaultCacheTTL {
		return cacheEntry{}, false, gen
	}
	return entry, true, gen
}

func (s *Service) setCachedIfGen(provider string, expectedGen uint64, entry cacheEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen[provider] != expectedGen {
		return
	}
	if s.cache == nil {
		s.cache = make(map[string]cacheEntry, len(providerRegistry))
	}
	s.cache[provider] = entry
}

func (s *Service) bumpAndSetCache(provider string, entry cacheEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen == nil {
		s.gen = make(map[string]uint64, len(providerRegistry))
	}
	s.gen[provider]++
	if s.cache == nil {
		s.cache = make(map[string]cacheEntry, len(providerRegistry))
	}
	s.cache[provider] = entry
}

func (s *Service) bumpAndInvalidateCache(provider string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen == nil {
		s.gen = make(map[string]uint64, len(providerRegistry))
	}
	s.gen[provider]++
	delete(s.cache, provider)
}

func (s *Service) loadRecord(ctx context.Context, provider string) (storedKey, bool, error) {
	entry, hit, gen := s.getCachedOrGen(provider)
	if hit {
		return entry.record, entry.found, nil
	}
	if s.Store == nil {
		return storedKey{}, false, nil
	}
	credID := CredentialID(provider)
	cred, err := s.Store.Get(ctx, credID)
	if errors.Is(err, secret.ErrCredentialNotFound) {
		s.setCachedIfGen(provider, gen, cacheEntry{found: false, fetchedAt: s.now()})
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
		s.setCachedIfGen(provider, gen, cacheEntry{found: false, fetchedAt: s.now()})
		return storedKey{}, false, nil
	}
	s.setCachedIfGen(provider, gen, cacheEntry{found: true, record: rec, fetchedAt: s.now()})
	return rec, true, nil
}

// List returns the BYOK requirement flag and status for every registered provider.
func (s *Service) List(ctx context.Context) (ListResponse, error) {
	out := ListResponse{
		BYOKRequired: s.BYOKRequired,
		Providers:    make([]ProviderStatus, 0, len(providerRegistry)),
	}
	for _, def := range providerRegistry {
		rec, found, err := s.loadRecord(ctx, def.ID)
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
	var validatedAt *time.Time
	if provider == ProviderVercelAIGateway && strings.TrimSpace(s.GatewayURL) != "" {
		if err := s.validateGatewayKey(ctx, apiKey); err != nil {
			return ProviderStatus{}, err
		}
		t := now
		validatedAt = &t
	}

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
	s.bumpAndSetCache(provider, cacheEntry{found: true, record: rec, fetchedAt: now})

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

// Delete removes the user's stored API key for provider and clears the cache.
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
	s.bumpAndInvalidateCache(provider)
	return ProviderStatus{
		Provider:   def.ID,
		Label:      def.Label,
		Configured: false,
	}, nil
}

// ResolveGatewayKey returns the user's decrypted Vercel AI Gateway API key if configured.
// If not configured, it returns ErrMissingAPIKey when BYOKRequired is true (never falling back
// to fallbackKey), or fallbackKey when BYOKRequired is false (or ErrMissingAPIKey if fallbackKey is empty).
func (s *Service) ResolveGatewayKey(ctx context.Context, fallbackKey string) (string, error) {
	rec, found, err := s.loadRecord(ctx, ProviderVercelAIGateway)
	if err != nil {
		return "", err
	}
	if found && rec.APIKey != "" {
		return rec.APIKey, nil
	}
	if s.BYOKRequired {
		return "", ErrMissingAPIKey
	}
	fallbackKey = strings.TrimSpace(fallbackKey)
	if fallbackKey == "" {
		return "", ErrMissingAPIKey
	}
	return fallbackKey, nil
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

func (s *Service) validateGatewayKey(ctx context.Context, apiKey string) error {
	valCtx, cancel := context.WithTimeout(ctx, defaultValidationTimeout)
	defer cancel()

	endpoint := strings.TrimRight(strings.TrimSpace(s.GatewayURL), "/") + "/models"
	req, err := http.NewRequestWithContext(valCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrValidationFailed, err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrValidationFailed, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrInvalidAPIKey
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
