package modelkeys

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"nori/internal/secret"
	"nori/internal/state"
)

type memCredStore struct {
	mu    sync.Mutex
	items map[string]secret.Credential
}

func newMemCredStore() *memCredStore {
	return &memCredStore{items: map[string]secret.Credential{}}
}

func (m *memCredStore) Get(_ context.Context, id string) (secret.Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.items[id]
	if !ok {
		return secret.Credential{}, secret.ErrCredentialNotFound
	}
	return secret.Credential{Version: c.Version, Data: append([]byte(nil), c.Data...)}, nil
}

func (m *memCredStore) Replace(_ context.Context, id string, expected int64, data []byte) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.items[id]
	if cur.Version != expected {
		return 0, secret.ErrCredentialConflict
	}
	next := expected + 1
	m.items[id] = secret.Credential{Version: next, Data: append([]byte(nil), data...)}
	return next, nil
}

func (m *memCredStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCredentialIDValid(t *testing.T) {
	for _, p := range Providers() {
		id := CredentialID(p.ID)
		if !state.ValidResourceID(id) {
			t.Fatalf("CredentialID(%q) = %q is not a valid state.ResourceID", p.ID, id)
		}
	}
}

func TestService_ValidatesAndResolvesProviderKeys(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct{ provider, url, header, value string }{
		{ProviderOpenAI, "https://api.openai.com/v1/me", "Authorization", "Bearer user_key_1234"},
		{ProviderAnthropic, "https://api.anthropic.com/v1/models?limit=1", "x-api-key", "user_key_1234"},
		{ProviderGoogle, "https://generativelanguage.googleapis.com/v1beta/models?pageSize=1", "x-goog-api-key", "user_key_1234"},
		{ProviderVercelAIGateway, "https://ai-gateway.vercel.sh/v1/credits", "Authorization", "Bearer user_key_1234"},
		{ProviderOpenRouter, "https://openrouter.ai/api/v1/key", "Authorization", "Bearer user_key_1234"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			store := newMemCredStore()
			now := time.Date(2026, 11, 26, 1, 0, 0, 0, time.UTC)
			svc := &Service{Store: store, Now: func() time.Time { return now }, HTTPClient: &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.String() != tc.url || r.Method != http.MethodGet {
						t.Fatalf("validation request = %s %s", r.Method, r.URL)
					}
					status := http.StatusUnauthorized
					if r.Header.Get(tc.header) == tc.value {
						status = http.StatusOK
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("{}"))}, nil
				}),
			}}
			worker := &Service{Store: store}
			if _, err := svc.Set(ctx, tc.provider, "rejected_key"); !errors.Is(err, ErrInvalidAPIKey) {
				t.Fatalf("rejected key error = %v", err)
			}
			status, err := svc.Set(ctx, tc.provider, "user_key_1234")
			if err != nil || !status.Configured || status.Last4 != "1234" || status.ValidatedAt == nil {
				t.Fatalf("saved status = %+v, %v", status, err)
			}
			if key, err := worker.ResolveKey(ctx, tc.provider); key != "user_key_1234" || err != nil {
				t.Fatalf("could not resolve saved key: %v", err)
			}
			if _, err := svc.Delete(ctx, tc.provider); err != nil {
				t.Fatal(err)
			}
			if _, err := worker.ResolveKey(ctx, tc.provider); !errors.Is(err, ErrModelAccess) {
				t.Fatalf("worker resolved a deleted key: %v", err)
			}
		})
	}
}
