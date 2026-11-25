package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"nori/internal/secret"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

func TestValidReturnTo(t *testing.T) {
	valid := []string{
		"/",
		"/apps",
		"/settings",
		"/work/task_42",
		"nori://oauth/callback",
	}
	for _, tc := range valid {
		if !validReturnTo(tc) {
			t.Errorf("validReturnTo(%q) = false, want true", tc)
		}
	}

	invalid := []string{
		"",
		"apps",
		"//evil.com",
		"///evil.com",
		`/\evil.com`,
		`\evil.com`,
		"/%2f/evil.com",
		"/%5cevil.com",
		"/apps?next=https://evil.com",
		"/apps#frag",
		"/apps\r\nLocation: https://evil.com",
		"/apps\x00",
		"/apps\x7f",
		"/apps\tbad",
		"http://oauth/callback",
		"https://oauth/callback",
		"https://evil.com/apps",
		"javascript:alert(1)",
		"file:///etc/passwd",
		"nori://other",
		"nori://other/callback",
		"nori://oauth",
		"nori://oauth/",
		"nori://oauth/other",
		"nori://oauth/callback/",
		"nori://oauth/callback/extra",
		"nori://oauth/callback/../other",
		"nori://oauth/callback@evil.com",
		"nori://user@oauth/callback",
		"nori://user:pass@oauth/callback",
		"nori://oauth@evil.com/callback",
		"nori://oauth:443/callback",
		"nori://oauth:/callback",
		"nori://oauth/callback?foo=bar",
		"nori://oauth/callback?",
		"nori://oauth/callback#frag",
		"nori://oauth/callback#",
		"nori://oauth/%63allback",
		"nori:oauth/callback",
		"NORI://oauth/callback",
		"nori://OAUTH/callback",
		"nori://oauth/callback\r\nX-Test: 1",
		"nori://oauth/callback ",
	}
	for _, tc := range invalid {
		if validReturnTo(tc) {
			t.Errorf("validReturnTo(%q) = true, want false", tc)
		}
	}
}

func TestSafeReturnToAndBuildReturnRedirect(t *testing.T) {
	if got := safeReturnTo("nori://oauth/callback"); got != "nori://oauth/callback" {
		t.Fatalf("safeReturnTo(ios) = %q, want nori://oauth/callback", got)
	}
	if got := safeReturnTo("/work/task_42"); got != "/work/task_42" {
		t.Fatalf("safeReturnTo(/work/task_42) = %q, want /work/task_42", got)
	}
	if got := safeReturnTo("https://evil.com"); got != "/apps" {
		t.Fatalf("safeReturnTo(evil) = %q, want /apps", got)
	}

	gotIOS := buildReturnRedirect("nori://oauth/callback", url.Values{
		"mcp":    {"srv-1"},
		"result": {"connected"},
	})
	if gotIOS != "nori://oauth/callback?mcp=srv-1&result=connected" {
		t.Fatalf("buildReturnRedirect(ios) = %q", gotIOS)
	}

	gotErr := buildReturnRedirect("nori://oauth/callback", url.Values{
		"mcp":    {"srv-1"},
		"result": {"failed"},
		"reason": {"access_denied"},
	})
	if gotErr != "nori://oauth/callback?mcp=srv-1&reason=access_denied&result=failed" {
		t.Fatalf("buildReturnRedirect(ios err) = %q", gotErr)
	}

	gotWeb := buildReturnRedirect("/apps", url.Values{
		"mcp":    {"srv-1"},
		"result": {"connected"},
	})
	if gotWeb != "/apps?mcp=srv-1&result=connected" {
		t.Fatalf("buildReturnRedirect(web) = %q", gotWeb)
	}
}

func TestActionValid(t *testing.T) {
	cases := []struct {
		name string
		act  Action
		want bool
	}{
		{"sign_in default", Action{Action: "sign_in"}, true},
		{"sign_in web path", Action{Action: "sign_in", ReturnTo: "/apps"}, true},
		{"sign_in ios callback", Action{Action: "sign_in", ReturnTo: "nori://oauth/callback"}, true},
		{"sign_in bad scheme", Action{Action: "sign_in", ReturnTo: "https://evil.com"}, false},
		{"sign_in bad nori path", Action{Action: "sign_in", ReturnTo: "nori://oauth/other"}, false},
		{"reconnect rejects return_to", Action{Action: "reconnect", ReturnTo: "nori://oauth/callback"}, false},
		{"set_exposure rejects return_to", Action{Action: "set_exposure", Tool: "ping", Exposure: "direct", ReturnTo: "/apps"}, false},
		{"set_exposure valid", Action{Action: "set_exposure", Tool: "ping", Exposure: "direct"}, true},
	}
	for _, tc := range cases {
		if got := tc.act.Valid(); got != tc.want {
			t.Errorf("%s: Valid() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

type memCredStore struct {
	mu      sync.Mutex
	items   map[string]secret.Credential
	failPut error
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
	return c, nil
}

func (m *memCredStore) Replace(_ context.Context, id string, expected int64, data []byte) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failPut != nil {
		return 0, m.failPut
	}
	cur := m.items[id]
	if cur.Version != expected {
		return 0, secret.ErrCredentialConflict
	}
	next := secret.Credential{Version: cur.Version + 1, Data: append([]byte(nil), data...)}
	m.items[id] = next
	return next.Version, nil
}

func (m *memCredStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

type handlerRoundTripper struct {
	handler http.Handler
}

func (rt handlerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	rt.handler.ServeHTTP(rec, req)
	return rec.Result(), nil
}

func newTestMCPService(t *testing.T, tokenFail bool) (*Service, *memCredStore, *time.Time, string) {
	t.Helper()
	backend := memstore.New()
	u := users.User{
		ID:          "11111111-1111-4111-8111-111111111111",
		Email:       "alice@example.com",
		DisplayName: "Alice",
		TimeZone:    "UTC",
	}
	store := state.Compose(records.NewShared(backend, u, 5.0), memstore.NewArtifacts())
	creds := newMemCredStore()

	baseURL := "http://127.0.0.1:18099"
	mcpSrv := sdk.NewServer(&sdk.Implementation{Name: "test-mcp", Version: "1.0.0"}, nil)
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return mcpSrv }, &sdk.StreamableHTTPOptions{Stateless: true})

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resource":              baseURL + "/mcp",
			"authorization_servers": []string{baseURL},
			"scopes_supported":      []string{"read"},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                           baseURL,
			"authorization_endpoint":           baseURL + "/authorize",
			"token_endpoint":                   baseURL + "/token",
			"registration_endpoint":            baseURL + "/register",
			"response_types_supported":         []string{"code"},
			"grant_types_supported":            []string{"authorization_code", "refresh_token"},
			"code_challenge_methods_supported": []string{"S256"},
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client_id":     "mcp-client-1",
			"client_secret": "mcp-secret-1",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if tokenFail {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "mcp-access-1",
			"refresh_token": "mcp-refresh-1",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	})
	mux.Handle("/mcp", mcpHandler)

	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	svc := &Service{
		Store:          store,
		Credentials:    creds,
		LocalProcesses: true,
		RedirectURL:    "http://127.0.0.1:3000/v1/connections/oauth/callback",
		Now:            func() time.Time { return now },
		Client:         &http.Client{Transport: handlerRoundTripper{handler: mux}},
	}
	return svc, creds, &now, baseURL
}

func TestMCPOAuthSignInAndCallbackIOSBridge(t *testing.T) {
	ctx := context.Background()
	svc, creds, now, baseURL := newTestMCPService(t, false)

	saveReceipt, err := svc.Save(ctx, "", "req-save-mcp", 0, Editable{
		Name:      "Remote OAuth MCP",
		Transport: "http",
		Endpoint:  baseURL + "/mcp",
		Auth:      "oauth",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Save MCP server: %v", err)
	}
	var srv Server
	if err := json.Unmarshal(saveReceipt.Resource, &srv); err != nil {
		t.Fatalf("unmarshal MCP server: %v", err)
	}

	// Reject disallowed return_to targets.
	for _, bad := range []string{"https://evil.com", "nori://oauth/other", "nori://oauth/callback/extra", "nori://oauth/callback@evil.com", "//evil.com"} {
		if _, err := svc.SignIn(ctx, srv.ID, bad); !errors.Is(err, state.ErrInvalidResource) {
			t.Fatalf("SignIn(returnTo=%q) err=%v, want ErrInvalidResource", bad, err)
		}
	}

	// Start via Act("sign_in") with nori://oauth/callback.
	actReceipt, err := svc.Act(ctx, srv.ID, "req-act-signin", srv.Revision, "sign_in", "", "", iosOAuthCallbackURL)
	if err != nil {
		t.Fatalf("Act(sign_in, nori://oauth/callback): %v", err)
	}
	var attempt AuthAttempt
	if err := json.Unmarshal(actReceipt.Auth, &attempt); err != nil {
		t.Fatalf("unmarshal AuthAttempt: %v", err)
	}
	authURL, err := url.Parse(attempt.AuthorizeURL)
	if err != nil {
		t.Fatalf("parse AuthorizeURL: %v", err)
	}
	oauthState := authURL.Query().Get("state")
	if oauthState == "" {
		t.Fatalf("missing OAuth state")
	}

	// Success callback redirects cleanly to nori://oauth/callback.
	loc, err := svc.Callback(ctx, oauthState, "code-ok", "")
	if err != nil {
		t.Fatalf("Callback success: %v", err)
	}
	wantSuccess := "nori://oauth/callback?mcp=" + srv.ID + "&result=connected"
	if loc != wantSuccess {
		t.Fatalf("Callback success redirect = %q, want %q", loc, wantSuccess)
	}

	// Provider error redirects cleanly to nori://oauth/callback with reason.
	errAttempt, err := svc.SignIn(ctx, srv.ID, iosOAuthCallbackURL)
	if err != nil {
		t.Fatalf("SignIn for provider error: %v", err)
	}
	errURL, _ := url.Parse(errAttempt.AuthorizeURL)
	errLoc, err := svc.Callback(ctx, errURL.Query().Get("state"), "", "access_denied")
	if err != nil {
		t.Fatalf("Callback provider error: %v", err)
	}
	wantDenied := "nori://oauth/callback?mcp=" + srv.ID + "&reason=access_denied&result=failed"
	if errLoc != wantDenied {
		t.Fatalf("Callback provider error redirect = %q, want %q", errLoc, wantDenied)
	}

	// Expired attempt redirects cleanly to nori://oauth/callback.
	expAttempt, err := svc.SignIn(ctx, srv.ID, iosOAuthCallbackURL)
	if err != nil {
		t.Fatalf("SignIn for expiry: %v", err)
	}
	expURL, _ := url.Parse(expAttempt.AuthorizeURL)
	*now = now.Add(20 * time.Minute)
	expLoc, err := svc.Callback(ctx, expURL.Query().Get("state"), "code-expired", "")
	if err != nil {
		t.Fatalf("Callback expired: %v", err)
	}
	wantExpired := "nori://oauth/callback?mcp=" + srv.ID + "&reason=expired&result=failed"
	if expLoc != wantExpired {
		t.Fatalf("Callback expired redirect = %q, want %q", expLoc, wantExpired)
	}

	// Credential store failure on iOS callback still redirects cleanly instead of stranding ASWebAuthenticationSession.
	credAttempt, err := svc.SignIn(ctx, srv.ID, iosOAuthCallbackURL)
	if err != nil {
		t.Fatalf("SignIn for cred failure: %v", err)
	}
	credURL, _ := url.Parse(credAttempt.AuthorizeURL)
	creds.failPut = errors.New("boom")
	credLoc, err := svc.Callback(ctx, credURL.Query().Get("state"), "code-cred-fail", "")
	if err != nil {
		t.Fatalf("Callback cred store failure returned error instead of redirect: %v", err)
	}
	wantCredFail := "nori://oauth/callback?mcp=" + srv.ID + "&reason=credential_store&result=failed"
	if credLoc != wantCredFail {
		t.Fatalf("Callback cred failure redirect = %q, want %q", credLoc, wantCredFail)
	}
}

func TestMCPOAuthTokenExchangeFailureIOSBridge(t *testing.T) {
	ctx := context.Background()
	svc, _, _, baseURL := newTestMCPService(t, true)

	saveReceipt, err := svc.Save(ctx, "", "req-save-mcp-fail", 0, Editable{
		Name:      "Remote OAuth MCP Fail",
		Transport: "http",
		Endpoint:  baseURL + "/mcp",
		Auth:      "oauth",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Save MCP server: %v", err)
	}
	var srv Server
	_ = json.Unmarshal(saveReceipt.Resource, &srv)

	attempt, err := svc.SignIn(ctx, srv.ID, iosOAuthCallbackURL)
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	authURL, _ := url.Parse(attempt.AuthorizeURL)

	loc, err := svc.Callback(ctx, authURL.Query().Get("state"), "bad-code", "")
	if err != nil {
		t.Fatalf("Callback token exchange failure returned error: %v", err)
	}
	want := "nori://oauth/callback?mcp=" + srv.ID + "&reason=token_exchange&result=failed"
	if loc != want {
		t.Fatalf("Callback token failure redirect = %q, want %q", loc, want)
	}
}
