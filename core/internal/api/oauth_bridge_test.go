package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
	"nori/internal/connections"
	"nori/internal/connectors/mcp"
	"nori/internal/extensions"
	"nori/internal/permissions"
	"nori/internal/secret"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

func TestAPIValidReturnTo(t *testing.T) {
	for _, ok := range []string{"/apps", "/settings", "/work/task_1", "nori://oauth/callback"} {
		if !validReturnTo(ok) {
			t.Errorf("validReturnTo(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{
		"",
		"//evil.com",
		`/\evil.com`,
		"https://evil.com",
		"http://oauth/callback",
		"javascript:alert(1)",
		"file:///etc/passwd",
		"nori://other",
		"nori://oauth/other",
		"nori://oauth/callback/extra",
		"nori://oauth/callback@evil.com",
		"nori://user@oauth/callback",
		"nori://oauth:443/callback",
		"nori://oauth/callback#frag",
		"nori://oauth/callback?x=1",
	} {
		if validReturnTo(bad) {
			t.Errorf("validReturnTo(%q) = true, want false", bad)
		}
	}
}

type apiTestCredStore struct {
	mu    sync.Mutex
	items map[string]secret.Credential
}

func newAPITestCredStore() *apiTestCredStore {
	return &apiTestCredStore{items: map[string]secret.Credential{}}
}

func (m *apiTestCredStore) Get(_ context.Context, id string) (secret.Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.items[id]
	if !ok {
		return secret.Credential{}, secret.ErrCredentialNotFound
	}
	return c, nil
}

func (m *apiTestCredStore) Replace(_ context.Context, id string, expected int64, data []byte) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.items[id]
	if cur.Version != expected {
		return 0, secret.ErrCredentialConflict
	}
	next := secret.Credential{Version: cur.Version + 1, Data: append([]byte(nil), data...)}
	m.items[id] = next
	return next.Version, nil
}

func (m *apiTestCredStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

type apiHandlerTransport struct {
	handler http.Handler
}

func (rt apiHandlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	rt.handler.ServeHTTP(rec, req)
	return rec.Result(), nil
}

type apiStubIntegration struct {
	authURL  string
	tokenURL string
}

func (s apiStubIntegration) Info() connections.Info {
	return connections.Info{
		ID:           "google",
		Title:        "Google",
		Auth:         "oauth",
		Availability: "available",
		BaseScopes:   []string{"openid", "email"},
	}
}

func (s apiStubIntegration) OAuth(redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     "client-1",
		ClientSecret: "secret-1",
		RedirectURL:  redirectURL,
		Endpoint: oauth2.Endpoint{
			AuthURL:  s.authURL,
			TokenURL: s.tokenURL,
		},
	}
}

func (s apiStubIntegration) Identify(context.Context, *http.Client) (connections.Account, error) {
	return connections.Account{ProviderID: "prov-api-1", Label: "alice@example.com"}, nil
}

func (s apiStubIntegration) Revoke(context.Context, *oauth2.Token) error { return nil }
func (s apiStubIntegration) Resources(context.Context, *http.Client) ([]connections.Resource, error) {
	return nil, nil
}
func (s apiStubIntegration) Definitions() []extensions.Definition { return nil }

func TestAPIOAuthBridgeConnectionsAndMCP(t *testing.T) {
	backend := memstore.New()
	u := users.User{
		ID:          "11111111-1111-4111-8111-111111111111",
		Email:       "alice@example.com",
		DisplayName: "Alice",
		TimeZone:    "UTC",
	}
	store := state.Compose(records.NewShared(backend, u, 5.0), memstore.NewArtifacts())
	creds := newAPITestCredStore()

	connProviderMux := http.NewServeMux()
	connProviderMux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "conn-access-1",
			"refresh_token": "conn-refresh-1",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         "openid email",
		})
	})

	connSvc := connections.New(store, creds, "https://nori.example.test/api/v1/connections/oauth/callback", apiStubIntegration{
		authURL:  "https://accounts.example.test/authorize",
		tokenURL: "https://accounts.example.test/token",
	})
	connSvc.Transport = apiHandlerTransport{handler: connProviderMux}

	mcpBaseURL := "http://127.0.0.1:18100"
	mcpSDKServer := sdk.NewServer(&sdk.Implementation{Name: "api-mcp", Version: "1.0.0"}, nil)
	mcpEndpointHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return mcpSDKServer }, &sdk.StreamableHTTPOptions{Stateless: true})
	mcpProviderMux := http.NewServeMux()
	mcpProviderMux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resource":              mcpBaseURL + "/mcp",
			"authorization_servers": []string{mcpBaseURL},
			"scopes_supported":      []string{"read"},
		})
	})
	mcpProviderMux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                           mcpBaseURL,
			"authorization_endpoint":           mcpBaseURL + "/authorize",
			"token_endpoint":                   mcpBaseURL + "/token",
			"registration_endpoint":            mcpBaseURL + "/register",
			"response_types_supported":         []string{"code"},
			"grant_types_supported":            []string{"authorization_code", "refresh_token"},
			"code_challenge_methods_supported": []string{"S256"},
		})
	})
	mcpProviderMux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client_id":     "api-mcp-client",
			"client_secret": "api-mcp-secret",
		})
	})
	mcpProviderMux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "mcp-access-api",
			"refresh_token": "mcp-refresh-api",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	})
	mcpProviderMux.Handle("/mcp", mcpEndpointHandler)

	mcpSvc := &mcp.Service{
		Store:          store,
		Credentials:    creds,
		LocalProcesses: true,
		RedirectURL:    "https://nori.example.test/api/v1/connections/oauth/callback",
		Now:            time.Now,
		Client:         &http.Client{Transport: apiHandlerTransport{handler: mcpProviderMux}},
	}

	assistant := &Assistant{
		Store:       store,
		Connections: connSvc,
		Permissions: &permissions.Service{Store: store},
		MCP:         mcpSvc,
	}
	mux := http.NewServeMux()
	assistant.register(mux)

	doJSON := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s %s: %v", method, path, err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// 1. Connections HTTP OAuth flow with nori://oauth/callback.
	createConnRec := doJSON(http.MethodPost, "/v1/connections", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": 0,
		"value": map[string]any{
			"integration_id": "google",
			"title":          "Google iOS",
		},
	})
	if createConnRec.Code != http.StatusCreated && createConnRec.Code != http.StatusOK {
		t.Fatalf("create connection status=%d body=%s", createConnRec.Code, createConnRec.Body.String())
	}
	var createConnReceipt state.MutationReceipt
	_ = json.Unmarshal(createConnRec.Body.Bytes(), &createConnReceipt)
	var connProj connections.Projection
	_ = json.Unmarshal(createConnReceipt.Resource, &connProj)

	// Invalid return_to rejected at API boundary with 400.
	badConnRec := doJSON(http.MethodPost, "/v1/connections/"+connProj.ID+"/actions", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": connProj.Revision,
		"value": map[string]any{
			"action":    "connect",
			"return_to": "nori://oauth/other",
		},
	})
	if badConnRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid connection return_to, got %d: %s", badConnRec.Code, badConnRec.Body.String())
	}

	// Valid nori://oauth/callback starts connection OAuth.
	startConnRec := doJSON(http.MethodPost, "/v1/connections/"+connProj.ID+"/actions", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": connProj.Revision,
		"value": map[string]any{
			"action":    "connect",
			"return_to": connections.IOSOAuthCallbackURL,
		},
	})
	if startConnRec.Code != http.StatusAccepted {
		t.Fatalf("start connection OAuth status=%d want 202 body=%s", startConnRec.Code, startConnRec.Body.String())
	}
	var startConnReceipt state.MutationReceipt
	_ = json.Unmarshal(startConnRec.Body.Bytes(), &startConnReceipt)
	var connAttempt connections.AuthAttempt
	_ = json.Unmarshal(startConnReceipt.Auth, &connAttempt)
	connAuthURL, err := url.Parse(connAttempt.AuthorizeURL)
	if err != nil {
		t.Fatalf("parse connection authorize URL: %v", err)
	}
	connState := connAuthURL.Query().Get("state")

	// Direct 303 redirect from GET /v1/connections/oauth/callback.
	cbReq := httptest.NewRequest(http.MethodGet, "/v1/connections/oauth/callback?state="+url.QueryEscape(connState)+"&code=auth-code-1", nil)
	cbRec := httptest.NewRecorder()
	mux.ServeHTTP(cbRec, cbReq)
	if cbRec.Code != http.StatusSeeOther {
		t.Fatalf("connection callback status=%d want 303 body=%s", cbRec.Code, cbRec.Body.String())
	}
	wantConnLoc := "nori://oauth/callback?connection=" + connProj.ID + "&result=connected"
	if got := cbRec.Header().Get("Location"); got != wantConnLoc {
		t.Fatalf("connection callback Location=%q want %q", got, wantConnLoc)
	}

	// JSON redirect mode also returns exact nori://oauth/callback location on replay.
	cbJSONReq := httptest.NewRequest(http.MethodGet, "/v1/connections/oauth/callback?state="+url.QueryEscape(connState)+"&code=auth-code-1&nori_redirect=json", nil)
	cbJSONRec := httptest.NewRecorder()
	mux.ServeHTTP(cbJSONRec, cbJSONReq)
	if cbJSONRec.Code != http.StatusOK {
		t.Fatalf("connection callback json status=%d body=%s", cbJSONRec.Code, cbJSONRec.Body.String())
	}
	var jsonLoc struct {
		Location string `json:"location"`
	}
	_ = json.Unmarshal(cbJSONRec.Body.Bytes(), &jsonLoc)
	if jsonLoc.Location != wantConnLoc {
		t.Fatalf("connection callback json location=%q want %q", jsonLoc.Location, wantConnLoc)
	}

	// 2. MCP HTTP OAuth flow with nori://oauth/callback.
	createMCPRec := doJSON(http.MethodPost, "/v1/mcp", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": 0,
		"value": map[string]any{
			"name":      "iOS MCP",
			"transport": "http",
			"endpoint":  mcpBaseURL + "/mcp",
			"auth":      "oauth",
			"enabled":   true,
			"exposure":  map[string]any{"default": "direct"},
		},
	})
	if createMCPRec.Code != http.StatusCreated && createMCPRec.Code != http.StatusOK {
		t.Fatalf("create MCP status=%d body=%s", createMCPRec.Code, createMCPRec.Body.String())
	}
	var createMCPReceipt state.MutationReceipt
	_ = json.Unmarshal(createMCPRec.Body.Bytes(), &createMCPReceipt)
	var mcpSrv mcp.Server
	_ = json.Unmarshal(createMCPReceipt.Resource, &mcpSrv)

	// Invalid return_to rejected at MCP API boundary with 400.
	badMCPRec := doJSON(http.MethodPost, "/v1/mcp/"+mcpSrv.ID+"/actions", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": mcpSrv.Revision,
		"value": map[string]any{
			"action":    "sign_in",
			"return_to": "https://evil.com",
		},
	})
	if badMCPRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid MCP return_to, got %d: %s", badMCPRec.Code, badMCPRec.Body.String())
	}

	// Non-sign_in action rejects return_to.
	badMCPActionRec := doJSON(http.MethodPost, "/v1/mcp/"+mcpSrv.ID+"/actions", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": mcpSrv.Revision,
		"value": map[string]any{
			"action":    "reconnect",
			"return_to": connections.IOSOAuthCallbackURL,
		},
	})
	if badMCPActionRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for return_to on reconnect, got %d: %s", badMCPActionRec.Code, badMCPActionRec.Body.String())
	}

	// Valid nori://oauth/callback starts MCP OAuth via POST /v1/mcp/{id}/actions.
	startMCPRec := doJSON(http.MethodPost, "/v1/mcp/"+mcpSrv.ID+"/actions", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": mcpSrv.Revision,
		"value": map[string]any{
			"action":    "sign_in",
			"return_to": connections.IOSOAuthCallbackURL,
		},
	})
	if startMCPRec.Code != http.StatusOK {
		t.Fatalf("start MCP OAuth status=%d body=%s", startMCPRec.Code, startMCPRec.Body.String())
	}
	var startMCPReceipt state.MutationReceipt
	_ = json.Unmarshal(startMCPRec.Body.Bytes(), &startMCPReceipt)
	var mcpAttempt mcp.AuthAttempt
	_ = json.Unmarshal(startMCPReceipt.Auth, &mcpAttempt)
	mcpAuthURL, err := url.Parse(mcpAttempt.AuthorizeURL)
	if err != nil {
		t.Fatalf("parse MCP authorize URL: %v", err)
	}
	mcpState := mcpAuthURL.Query().Get("state")

	// Direct 303 redirect from GET /v1/connections/oauth/callback for MCP attempt.
	mcpCBReq := httptest.NewRequest(http.MethodGet, "/v1/connections/oauth/callback?state="+url.QueryEscape(mcpState)+"&code=mcp-code-1", nil)
	mcpCBRec := httptest.NewRecorder()
	mux.ServeHTTP(mcpCBRec, mcpCBReq)
	if mcpCBRec.Code != http.StatusSeeOther {
		t.Fatalf("MCP callback status=%d want 303 body=%s", mcpCBRec.Code, mcpCBRec.Body.String())
	}
	wantMCPLoc := "nori://oauth/callback?mcp=" + mcpSrv.ID + "&result=connected"
	if got := mcpCBRec.Header().Get("Location"); got != wantMCPLoc {
		t.Fatalf("MCP callback Location=%q want %q", got, wantMCPLoc)
	}

	// Provider error on MCP OAuth callback redirects cleanly to nori://oauth/callback.
	startMCPErrRec := doJSON(http.MethodPost, "/v1/mcp/"+mcpSrv.ID+"/actions", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": 0,
		"value": map[string]any{
			"action":    "sign_in",
			"return_to": connections.IOSOAuthCallbackURL,
		},
	})
	if startMCPErrRec.Code != http.StatusOK {
		t.Fatalf("start MCP OAuth error test status=%d body=%s", startMCPErrRec.Code, startMCPErrRec.Body.String())
	}
	var startMCPErrReceipt state.MutationReceipt
	_ = json.Unmarshal(startMCPErrRec.Body.Bytes(), &startMCPErrReceipt)
	var mcpErrAttempt mcp.AuthAttempt
	_ = json.Unmarshal(startMCPErrReceipt.Auth, &mcpErrAttempt)
	mcpErrURL, _ := url.Parse(mcpErrAttempt.AuthorizeURL)

	mcpErrCBReq := httptest.NewRequest(http.MethodGet, "/v1/connections/oauth/callback?state="+url.QueryEscape(mcpErrURL.Query().Get("state"))+"&error=access_denied", nil)
	mcpErrCBRec := httptest.NewRecorder()
	mux.ServeHTTP(mcpErrCBRec, mcpErrCBReq)
	if mcpErrCBRec.Code != http.StatusSeeOther {
		t.Fatalf("MCP error callback status=%d want 303 body=%s", mcpErrCBRec.Code, mcpErrCBRec.Body.String())
	}
	wantMCPErrLoc := "nori://oauth/callback?mcp=" + mcpSrv.ID + "&reason=access_denied&result=failed"
	if got := mcpErrCBRec.Header().Get("Location"); got != wantMCPErrLoc {
		t.Fatalf("MCP error callback Location=%q want %q", got, wantMCPErrLoc)
	}
}
