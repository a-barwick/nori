package connections

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

	"golang.org/x/oauth2"
	"nori/internal/extensions"
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
		"/apps/google",
		"/work/task_42",
		"nori://oauth/callback",
	}
	for _, tc := range valid {
		if !ValidReturnTo(tc) {
			t.Errorf("ValidReturnTo(%q) = false, want true", tc)
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
		"/apps?",
		"/apps#frag",
		"/apps#",
		"/apps\r\nLocation: https://evil.com",
		"/apps\x00",
		"/apps\x7f",
		"/apps\tbad",
		"http://oauth/callback",
		"https://oauth/callback",
		"https://evil.com/apps",
		"javascript:alert(1)",
		"file:///etc/passwd",
		"data:text/html,hello",
		"other://oauth/callback",
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
		if ValidReturnTo(tc) {
			t.Errorf("ValidReturnTo(%q) = true, want false", tc)
		}
	}
}

func TestSafeReturnToAndBuildReturnRedirect(t *testing.T) {
	if got := SafeReturnTo("nori://oauth/callback"); got != "nori://oauth/callback" {
		t.Fatalf("SafeReturnTo(ios) = %q, want nori://oauth/callback", got)
	}
	if got := SafeReturnTo("/work/task_42"); got != "/work/task_42" {
		t.Fatalf("SafeReturnTo(/work/task_42) = %q, want /work/task_42", got)
	}
	if got := SafeReturnTo("https://evil.com"); got != "/apps" {
		t.Fatalf("SafeReturnTo(evil) = %q, want /apps", got)
	}

	gotIOS := BuildReturnRedirect("nori://oauth/callback", url.Values{
		"connection": {"conn-1"},
		"result":     {"connected"},
	})
	if gotIOS != "nori://oauth/callback?connection=conn-1&result=connected" {
		t.Fatalf("BuildReturnRedirect(ios) = %q", gotIOS)
	}

	gotWeb := BuildReturnRedirect("/apps", url.Values{
		"connection": {"conn-1"},
		"result":     {"connected"},
	})
	if gotWeb != "/apps?connection=conn-1&result=connected" {
		t.Fatalf("BuildReturnRedirect(web) = %q", gotWeb)
	}

	gotFallback := BuildReturnRedirect("nori://oauth/other", url.Values{
		"connection": {"conn-1"},
		"result":     {"failed"},
	})
	if gotFallback != "/apps?connection=conn-1&result=failed" {
		t.Fatalf("BuildReturnRedirect(invalid) = %q", gotFallback)
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

type stubOAuthIntegration struct {
	authURL    string
	tokenURL   string
	providerID string
	label      string
	identifyFn func() (Account, error)
}

func (s *stubOAuthIntegration) Info() Info {
	return Info{
		ID:               "google",
		Title:            "Google",
		Auth:             "oauth",
		MultipleAccounts: true,
		Availability:     "available",
		BaseScopes:       []string{"openid", "email"},
		Capabilities: []Capability{
			{ID: "mail.read", Title: "Read Mail", Effect: "read", Supported: true, Scopes: []string{"mail.read"}},
		},
	}
}

func (s *stubOAuthIntegration) OAuth(redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		RedirectURL:  redirectURL,
		Endpoint: oauth2.Endpoint{
			AuthURL:  s.authURL,
			TokenURL: s.tokenURL,
		},
	}
}

func (s *stubOAuthIntegration) Identify(context.Context, *http.Client) (Account, error) {
	if s.identifyFn != nil {
		return s.identifyFn()
	}
	return Account{ProviderID: s.providerID, Label: s.label}, nil
}

func (s *stubOAuthIntegration) Revoke(context.Context, *oauth2.Token) error { return nil }
func (s *stubOAuthIntegration) Resources(context.Context, *http.Client) ([]Resource, error) {
	return nil, nil
}
func (s *stubOAuthIntegration) Definitions() []extensions.Definition { return nil }

func newTestConnectionsService(t *testing.T, tokenFail bool) (*Service, *stubOAuthIntegration, *memCredStore, *time.Time) {
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

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if tokenFail {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-tok-1",
			"refresh_token": "refresh-tok-1",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         "openid email mail.read",
		})
	})

	integ := &stubOAuthIntegration{
		authURL:    "https://accounts.example.test/authorize",
		tokenURL:   "https://accounts.example.test/token",
		providerID: "prov-1",
		label:      "alice@example.com",
	}
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	svc := New(store, creds, "https://nori.example.test/api/v1/connections/oauth/callback", integ)
	svc.Now = func() time.Time { return now }
	svc.Transport = handlerRoundTripper{handler: mux}
	return svc, integ, creds, &now
}

func TestConnectionsOAuthStartAndCallbackIOSBridge(t *testing.T) {
	ctx := context.Background()
	svc, _, creds, now := newTestConnectionsService(t, false)

	createReceipt, err := svc.Create(ctx, "req-create-1", Editable{
		IntegrationID: "google",
		Title:         "Google Work",
	})
	if err != nil {
		t.Fatalf("Create connection: %v", err)
	}
	var created Projection
	if err := json.Unmarshal(createReceipt.Resource, &created); err != nil {
		t.Fatalf("unmarshal connection: %v", err)
	}

	// Reject disallowed return_to targets.
	for _, bad := range []string{"https://evil.com", "nori://oauth/other", "nori://oauth/callback/extra", "//evil.com"} {
		if _, err := svc.Act(ctx, created.ID, "req-bad-"+bad, created.Revision, Action{
			Action:   "connect",
			ReturnTo: bad,
		}); !errors.Is(err, state.ErrInvalidResource) {
			t.Fatalf("Act(return_to=%q) err=%v, want ErrInvalidResource", bad, err)
		}
	}

	// Start OAuth with exact nori://oauth/callback.
	startReceipt, err := svc.Act(ctx, created.ID, "req-start-1", created.Revision, Action{
		Action:       "connect",
		Capabilities: []string{"mail.read"},
		ReturnTo:     IOSOAuthCallbackURL,
	})
	if err != nil {
		t.Fatalf("Act(connect, nori://oauth/callback): %v", err)
	}
	var attempt AuthAttempt
	if err := json.Unmarshal(startReceipt.Auth, &attempt); err != nil {
		t.Fatalf("unmarshal auth attempt: %v", err)
	}
	authURL, err := url.Parse(attempt.AuthorizeURL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	oauthState := authURL.Query().Get("state")
	if oauthState == "" {
		t.Fatalf("missing OAuth state")
	}

	// Success callback redirects cleanly to nori://oauth/callback with query params.
	loc, err := svc.Callback(ctx, oauthState, "code-123", "")
	if err != nil {
		t.Fatalf("Callback success: %v", err)
	}
	wantSuccess := "nori://oauth/callback?connection=" + created.ID + "&result=connected"
	if loc != wantSuccess {
		t.Fatalf("Callback success redirect = %q, want %q", loc, wantSuccess)
	}

	// Replayed callback returns the same iOS redirect.
	replayLoc, err := svc.Callback(ctx, oauthState, "code-123", "")
	if err != nil {
		t.Fatalf("Callback replay: %v", err)
	}
	if replayLoc != wantSuccess {
		t.Fatalf("Callback replay redirect = %q, want %q", replayLoc, wantSuccess)
	}

	// Verify connection state is connected and token stored.
	proj, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get connection: %v", err)
	}
	if proj.Status != StatusConnected {
		t.Fatalf("status = %q, want %q", proj.Status, StatusConnected)
	}
	if _, err := creds.Get(ctx, created.ID); err != nil {
		t.Fatalf("expected stored credential: %v", err)
	}

	// Provider cancellation redirects cleanly to nori://oauth/callback?connection=...&result=cancelled.
	cancelStart, err := svc.Act(ctx, created.ID, "req-cancel-start", proj.Revision, Action{
		Action:   "repair",
		ReturnTo: IOSOAuthCallbackURL,
	})
	if err != nil {
		t.Fatalf("Act(repair) for cancel: %v", err)
	}
	var cancelAttempt AuthAttempt
	_ = json.Unmarshal(cancelStart.Auth, &cancelAttempt)
	cancelURL, _ := url.Parse(cancelAttempt.AuthorizeURL)
	cancelLoc, err := svc.Callback(ctx, cancelURL.Query().Get("state"), "", "access_denied")
	if err != nil {
		t.Fatalf("Callback access_denied: %v", err)
	}
	wantCancelled := "nori://oauth/callback?connection=" + created.ID + "&result=cancelled"
	if cancelLoc != wantCancelled {
		t.Fatalf("Callback cancelled redirect = %q, want %q", cancelLoc, wantCancelled)
	}

	// Expired attempt redirects cleanly to nori://oauth/callback?connection=...&result=failed.
	proj, _ = svc.Get(ctx, created.ID)
	expStart, err := svc.Act(ctx, created.ID, "req-exp-start", proj.Revision, Action{
		Action:   "repair",
		ReturnTo: IOSOAuthCallbackURL,
	})
	if err != nil {
		t.Fatalf("Act(repair) for expiry: %v", err)
	}
	var expAttempt AuthAttempt
	_ = json.Unmarshal(expStart.Auth, &expAttempt)
	expURL, _ := url.Parse(expAttempt.AuthorizeURL)
	*now = now.Add(20 * time.Minute)
	expLoc, err := svc.Callback(ctx, expURL.Query().Get("state"), "code-expired", "")
	if err != nil {
		t.Fatalf("Callback expired: %v", err)
	}
	wantFailed := "nori://oauth/callback?connection=" + created.ID + "&result=failed"
	if expLoc != wantFailed {
		t.Fatalf("Callback expired redirect = %q, want %q", expLoc, wantFailed)
	}

	// Merge sign-in into existing account also redirects cleanly to nori://oauth/callback.
	dupReceipt, err := svc.Create(ctx, "req-create-dup", Editable{
		IntegrationID: "google",
		Title:         "Google Duplicate",
	})
	if err != nil {
		t.Fatalf("Create dup connection: %v", err)
	}
	var dup Projection
	_ = json.Unmarshal(dupReceipt.Resource, &dup)
	dupStart, err := svc.Act(ctx, dup.ID, "req-dup-start", dup.Revision, Action{
		Action:   "connect",
		ReturnTo: IOSOAuthCallbackURL,
	})
	if err != nil {
		t.Fatalf("Act(connect dup): %v", err)
	}
	var dupAttempt AuthAttempt
	_ = json.Unmarshal(dupStart.Auth, &dupAttempt)
	dupURL, _ := url.Parse(dupAttempt.AuthorizeURL)
	mergeLoc, err := svc.Callback(ctx, dupURL.Query().Get("state"), "code-merge", "")
	if err != nil {
		t.Fatalf("Callback merge: %v", err)
	}
	if mergeLoc != wantSuccess {
		t.Fatalf("Callback merge redirect = %q, want %q", mergeLoc, wantSuccess)
	}
}

func TestConnectionsOAuthTokenExchangeFailureIOSBridge(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _ := newTestConnectionsService(t, true)

	createReceipt, err := svc.Create(ctx, "req-create-fail", Editable{
		IntegrationID: "google",
		Title:         "Google Fail",
	})
	if err != nil {
		t.Fatalf("Create connection: %v", err)
	}
	var created Projection
	_ = json.Unmarshal(createReceipt.Resource, &created)

	startReceipt, err := svc.Act(ctx, created.ID, "req-start-fail", created.Revision, Action{
		Action:   "connect",
		ReturnTo: IOSOAuthCallbackURL,
	})
	if err != nil {
		t.Fatalf("Act(connect): %v", err)
	}
	var attempt AuthAttempt
	_ = json.Unmarshal(startReceipt.Auth, &attempt)
	authURL, _ := url.Parse(attempt.AuthorizeURL)

	loc, err := svc.Callback(ctx, authURL.Query().Get("state"), "bad-code", "")
	if err != nil {
		t.Fatalf("Callback token exchange failure returned error instead of redirect: %v", err)
	}
	want := "nori://oauth/callback?connection=" + created.ID + "&result=failed"
	if loc != want {
		t.Fatalf("Callback token failure redirect = %q, want %q", loc, want)
	}
}
