package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nori/internal/modelkeys"
	"nori/internal/secret"
)

type memModelKeyStore struct {
	mu    sync.Mutex
	items map[string]secret.Credential
}

func newMemModelKeyStore() *memModelKeyStore {
	return &memModelKeyStore{items: map[string]secret.Credential{}}
}

func (m *memModelKeyStore) Get(_ context.Context, id string) (secret.Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.items[id]
	if !ok {
		return secret.Credential{}, secret.ErrCredentialNotFound
	}
	return secret.Credential{Version: c.Version, Data: append([]byte(nil), c.Data...)}, nil
}

func (m *memModelKeyStore) Replace(_ context.Context, id string, expected int64, data []byte) (int64, error) {
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

func (m *memModelKeyStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestModelKeysAPI_CRUDAndErrorCodes(t *testing.T) {
	now := time.Date(2026, 11, 26, 1, 30, 0, 0, time.UTC)
	svc := &modelkeys.Service{
		Store:        newMemModelKeyStore(),
		GatewayURL:   "https://ai-gateway.vercel.sh/v1",
		BYOKRequired: true,
		Now:          func() time.Time { return now },
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") == "Bearer vck_invalid_0000000000000000" {
					return &http.Response{
						StatusCode: http.StatusUnauthorized,
						Body:       io.NopCloser(strings.NewReader(`{"error":"bad key"}`)),
						Header:     http.Header{},
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"data":[]}`)),
					Header:     http.Header{},
				}, nil
			}),
		},
	}

	h := Handler(Deps{
		Assistant: &Assistant{
			ModelKeys: svc,
		},
	})

	// 1. GET /v1/settings/model-keys -> 200 OK, unconfigured.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/settings/model-keys", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", rec.Code, rec.Body.String())
	}
	var listResp modelkeys.ListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if !listResp.BYOKRequired || len(listResp.Providers) != 1 || listResp.Providers[0].Provider != modelkeys.ProviderVercelAIGateway || listResp.Providers[0].Configured {
		t.Fatalf("unexpected initial list: %+v", listResp)
	}

	// 2. PUT unknown provider -> 400.
	reqBadProv := httptest.NewRequest(http.MethodPut, "/v1/settings/model-keys/bogus", bytes.NewReader([]byte(`{"api_key":"vck_1234567890123456"}`)))
	reqBadProv.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, reqBadProv)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown provider, got %d body=%s", rec.Code, rec.Body.String())
	}

	// 3. PUT empty key -> 400.
	reqEmpty := httptest.NewRequest(http.MethodPut, "/v1/settings/model-keys/vercel_ai_gateway", bytes.NewReader([]byte(`{"api_key":"  "}`)))
	reqEmpty.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, reqEmpty)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty key, got %d body=%s", rec.Code, rec.Body.String())
	}

	// 4. PUT invalid key (gateway returns 401) -> 422.
	reqInvalid := httptest.NewRequest(http.MethodPut, "/v1/settings/model-keys/vercel_ai_gateway", bytes.NewReader([]byte(`{"api_key":"vck_invalid_0000000000000000"}`)))
	reqInvalid.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, reqInvalid)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for invalid key, got %d body=%s", rec.Code, rec.Body.String())
	}

	// 5. PUT valid key -> 200 OK, never leaks raw key.
	secretKey := "vck_valid_user_secret_key_7890"
	reqOK := httptest.NewRequest(http.MethodPut, "/v1/settings/model-keys/vercel_ai_gateway", bytes.NewReader([]byte(`{"api_key":"`+secretKey+`"}`)))
	reqOK.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, reqOK)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid key, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secretKey) {
		t.Fatalf("PUT response leaked raw api_key: %s", rec.Body.String())
	}
	var status modelkeys.ProviderStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !status.Configured || status.Last4 != "7890" || status.UpdatedAt == nil || status.ValidatedAt == nil {
		t.Fatalf("unexpected status: %+v", status)
	}

	// 6. GET after PUT shows configured without raw key.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/settings/model-keys", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET after PUT status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secretKey) {
		t.Fatalf("GET response leaked raw api_key: %s", rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list after PUT: %v", err)
	}
	if !listResp.Providers[0].Configured || listResp.Providers[0].Last4 != "7890" {
		t.Fatalf("unexpected list after PUT: %+v", listResp)
	}

	// 7. DELETE -> 200 OK, unconfigured.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/settings/model-keys/vercel_ai_gateway", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE status=%d body=%s", rec.Code, rec.Body.String())
	}
	var delStatus modelkeys.ProviderStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &delStatus); err != nil {
		t.Fatalf("decode delete status: %v", err)
	}
	if delStatus.Configured || delStatus.Last4 != "" {
		t.Fatalf("unexpected delete status: %+v", delStatus)
	}
}

type unavailableModelKeyStore struct{}

func (unavailableModelKeyStore) Get(context.Context, string) (secret.Credential, error) {
	return secret.Credential{}, secret.ErrCredentialUnavailable
}
func (unavailableModelKeyStore) Replace(context.Context, string, int64, []byte) (int64, error) {
	return 0, secret.ErrCredentialUnavailable
}
func (unavailableModelKeyStore) Delete(context.Context, string) error {
	return secret.ErrCredentialUnavailable
}

func TestModelKeysAPI_ServiceUnavailableErrors(t *testing.T) {
	// 1. Upstream provider validation outage -> 503 provider_unavailable (retryable).
	svcProviderDown := &modelkeys.Service{
		Store:        newMemModelKeyStore(),
		GatewayURL:   "https://ai-gateway.vercel.sh/v1",
		BYOKRequired: true,
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadGateway,
					Body:       io.NopCloser(strings.NewReader(`{"error":"upstream"}`)),
					Header:     http.Header{},
				}, nil
			}),
		},
	}
	h1 := Handler(Deps{Assistant: &Assistant{ModelKeys: svcProviderDown}})
	req1 := httptest.NewRequest(http.MethodPut, "/v1/settings/model-keys/vercel_ai_gateway", bytes.NewReader([]byte(`{"api_key":"vck_valid_key_123456"}`)))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	h1.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for ErrValidationFailed, got %d body=%s", rec1.Code, rec1.Body.String())
	}
	var errBody1 apiError
	if err := json.Unmarshal(rec1.Body.Bytes(), &errBody1); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if errBody1.Code != "provider_unavailable" || !errBody1.Retryable {
		t.Fatalf("unexpected provider_unavailable payload: %+v", errBody1)
	}

	// 2. Credential store outage -> 503 credential_store_unavailable (retryable).
	svcStoreDown := &modelkeys.Service{
		Store:        unavailableModelKeyStore{},
		GatewayURL:   "https://ai-gateway.vercel.sh/v1",
		BYOKRequired: true,
	}
	h2 := Handler(Deps{Assistant: &Assistant{ModelKeys: svcStoreDown}})
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/v1/settings/model-keys", nil))
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for ErrCredentialUnavailable, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	var errBody2 apiError
	if err := json.Unmarshal(rec2.Body.Bytes(), &errBody2); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if errBody2.Code != "credential_store_unavailable" || !errBody2.Retryable {
		t.Fatalf("unexpected credential_store_unavailable payload: %+v", errBody2)
	}
}
