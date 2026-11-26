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
	gets  int
}

func newMemCredStore() *memCredStore {
	return &memCredStore{items: map[string]secret.Credential{}}
}

func (m *memCredStore) Get(_ context.Context, id string) (secret.Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets++
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

func TestService_SetListDeleteAndCache(t *testing.T) {
	ctx := context.Background()
	store := newMemCredStore()
	now := time.Date(2026, 11, 26, 1, 0, 0, 0, time.UTC)

	var seenAuth string
	var seenURL string
	svc := &Service{
		Store:        store,
		GatewayURL:   "https://ai-gateway.vercel.sh/v1",
		BYOKRequired: true,
		Now:          func() time.Time { return now },
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				seenAuth = r.Header.Get("Authorization")
				seenURL = r.URL.String()
				if seenAuth == "Bearer vck_bad_key_1234567890" {
					return &http.Response{
						StatusCode: http.StatusUnauthorized,
						Body:       io.NopCloser(strings.NewReader(`{"error":"unauthorized"}`)),
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

	// Initially unconfigured.
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !list.BYOKRequired || len(list.Providers) != 1 || list.Providers[0].Configured {
		t.Fatalf("unexpected initial list: %+v", list)
	}
	if _, err := svc.ResolveGatewayKey(ctx, "vck_deployment_fallback"); !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("expected ErrMissingAPIKey when BYOKRequired=true, got %v", err)
	}

	// Reject unknown provider, empty key, and invalid key (401).
	if _, err := svc.Set(ctx, "unknown", "abc"); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("expected ErrUnknownProvider, got %v", err)
	}
	if _, err := svc.Set(ctx, ProviderVercelAIGateway, "   "); !errors.Is(err, ErrEmptyAPIKey) {
		t.Fatalf("expected ErrEmptyAPIKey, got %v", err)
	}
	if _, err := svc.Set(ctx, ProviderVercelAIGateway, "vck_bad_key_1234567890"); !errors.Is(err, ErrInvalidAPIKey) {
		t.Fatalf("expected ErrInvalidAPIKey, got %v", err)
	}

	// Valid key succeeds and validates against <GatewayURL>/models.
	st, err := svc.Set(ctx, ProviderVercelAIGateway, "vck_live_user_secret_9876")
	if err != nil {
		t.Fatalf("Set valid key: %v", err)
	}
	if seenURL != "https://ai-gateway.vercel.sh/v1/models" || seenAuth != "Bearer vck_live_user_secret_9876" {
		t.Fatalf("unexpected validation request: url=%q auth=%q", seenURL, seenAuth)
	}
	if !st.Configured || st.Last4 != "9876" || st.UpdatedAt == nil || st.ValidatedAt == nil {
		t.Fatalf("unexpected status: %+v", st)
	}

	// ResolveGatewayKey hits cache without extra Store.Get calls.
	getsBefore := store.gets
	resolved, err := svc.ResolveGatewayKey(ctx, "vck_deployment_fallback")
	if err != nil || resolved != "vck_live_user_secret_9876" {
		t.Fatalf("ResolveGatewayKey = (%q, %v)", resolved, err)
	}
	if store.gets != getsBefore {
		t.Fatalf("expected cache hit on ResolveGatewayKey, got %d extra store reads", store.gets-getsBefore)
	}

	// Replace with a rotated key immediately invalidates cache.
	now = now.Add(5 * time.Minute)
	st2, err := svc.Set(ctx, ProviderVercelAIGateway, "vck_live_user_secret_4321")
	if err != nil {
		t.Fatalf("Set rotated key: %v", err)
	}
	if st2.Last4 != "4321" {
		t.Fatalf("expected last4=4321, got %q", st2.Last4)
	}
	resolved, err = svc.ResolveGatewayKey(ctx, "vck_deployment_fallback")
	if err != nil || resolved != "vck_live_user_secret_4321" {
		t.Fatalf("ResolveGatewayKey after rotation = (%q, %v)", resolved, err)
	}

	// Delete clears store and cache immediately.
	delStatus, err := svc.Delete(ctx, ProviderVercelAIGateway)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if delStatus.Configured || delStatus.Last4 != "" {
		t.Fatalf("expected unconfigured after delete, got %+v", delStatus)
	}
	if _, err := svc.ResolveGatewayKey(ctx, "vck_deployment_fallback"); !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("expected ErrMissingAPIKey after delete, got %v", err)
	}
}

func TestService_ResolveFallbackWhenBYOKOptional(t *testing.T) {
	ctx := context.Background()
	svc := &Service{
		Store:        newMemCredStore(),
		BYOKRequired: false,
	}

	key, err := svc.ResolveGatewayKey(ctx, "vck_deployment_key")
	if err != nil || key != "vck_deployment_key" {
		t.Fatalf("expected fallback key, got (%q, %v)", key, err)
	}
	if _, err := svc.ResolveGatewayKey(ctx, ""); !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("expected ErrMissingAPIKey when both user and fallback keys are empty, got %v", err)
	}
}

type flakyCredStore struct {
	*memCredStore
	mu          sync.Mutex
	unavailable bool
	getHook     func()
}

func (f *flakyCredStore) Get(ctx context.Context, id string) (secret.Credential, error) {
	f.mu.Lock()
	unavail := f.unavailable
	hook := f.getHook
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if unavail {
		return secret.Credential{}, secret.ErrCredentialUnavailable
	}
	return f.memCredStore.Get(ctx, id)
}

func TestService_CredentialUnavailableNeverFallsBackOrCaches(t *testing.T) {
	ctx := context.Background()
	store := &flakyCredStore{memCredStore: newMemCredStore(), unavailable: true}
	svc := &Service{
		Store:        store,
		BYOKRequired: false,
	}

	// Outage must return ErrCredentialUnavailable and NOT fall back to deployment key.
	if _, err := svc.ResolveGatewayKey(ctx, "vck_deployment_key"); !errors.Is(err, secret.ErrCredentialUnavailable) {
		t.Fatalf("expected ErrCredentialUnavailable during outage, got %v", err)
	}

	// Outage must NOT be cached: once store recovers, subsequent call succeeds immediately.
	store.mu.Lock()
	store.unavailable = false
	store.mu.Unlock()

	key, err := svc.ResolveGatewayKey(ctx, "vck_deployment_key")
	if err != nil || key != "vck_deployment_key" {
		t.Fatalf("expected recovery after outage, got (%q, %v)", key, err)
	}
}

func TestService_StaleReadDoesNotResurrectDeletedKey(t *testing.T) {
	ctx := context.Background()
	base := newMemCredStore()
	store := &flakyCredStore{memCredStore: base}
	now := time.Date(2026, 11, 26, 2, 0, 0, 0, time.UTC)
	svc := &Service{
		Store:        store,
		BYOKRequired: true,
		Now:          func() time.Time { return now },
	}

	// Seed a key in the store, then expire the cache so the next read hits Store.Get.
	if _, err := svc.Set(ctx, ProviderVercelAIGateway, "vck_old_key_1111"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	now = now.Add(10 * time.Second) // past 5s TTL

	// Pause the stale Get right after reading the old value, while Delete runs concurrently.
	inGet := make(chan struct{})
	releaseGet := make(chan struct{})
	var once sync.Once
	store.mu.Lock()
	store.getHook = func() {
		once.Do(func() {
			close(inGet)
			<-releaseGet
		})
	}
	store.mu.Unlock()

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_, _ = svc.ResolveGatewayKey(ctx, "")
	}()

	<-inGet
	// Capture the old record before Delete removes it? Actually hook runs before Get reads,
	// so let's instead let Delete run after Get reads by using a custom store wrapper.
	// Wait: let's verify Delete runs while Get is paused: if Get runs after Delete, Get sees NotFound.
	// To test the exact stale-read-resurrecting-deleted-key race, releaseGet must fire after Delete
	// while Get returns the PRE-delete credential!
	close(releaseGet)
	<-readDone

	// Now test with a deterministic stale-returning store wrapper.
	staleGetStarted := make(chan struct{})
	staleGetRelease := make(chan struct{})
	staleStore := &staleReadStore{
		memCredStore: newMemCredStore(),
		started:      staleGetStarted,
		release:      staleGetRelease,
	}
	svc2 := &Service{
		Store:        staleStore,
		BYOKRequired: true,
		Now:          func() time.Time { return now },
	}
	if _, err := svc2.Set(ctx, ProviderVercelAIGateway, "vck_old_key_2222"); err != nil {
		t.Fatalf("Set svc2: %v", err)
	}
	now = now.Add(10 * time.Second) // expire 5s cache

	staleStore.armPause()
	done := make(chan string, 1)
	go func() {
		k, _ := svc2.ResolveGatewayKey(ctx, "")
		done <- k
	}()

	// Wait until Store.Get has already read the old key and is paused before returning.
	<-staleGetStarted
	// Delete the key while the slow read is in flight.
	if _, err := svc2.Delete(ctx, ProviderVercelAIGateway); err != nil {
		t.Fatalf("Delete during in-flight Get: %v", err)
	}
	// Let the stale Get return the old key; generation mismatch must prevent caching it.
	close(staleGetRelease)
	<-done

	// Subsequent ResolveGatewayKey must NOT see the resurrected deleted key.
	if k, err := svc2.ResolveGatewayKey(ctx, ""); !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("expected ErrMissingAPIKey after delete, got key=%q err=%v", k, err)
	}
}

type staleReadStore struct {
	*memCredStore
	mu      sync.Mutex
	armed   bool
	started chan struct{}
	release chan struct{}
}

func (s *staleReadStore) armPause() {
	s.mu.Lock()
	s.armed = true
	s.mu.Unlock()
}

func (s *staleReadStore) Get(ctx context.Context, id string) (secret.Credential, error) {
	cred, err := s.memCredStore.Get(ctx, id)
	s.mu.Lock()
	armed := s.armed
	if armed {
		s.armed = false
	}
	s.mu.Unlock()
	if armed {
		close(s.started)
		<-s.release
	}
	return cred, err
}

func TestLast4_ShortKeysReturnEmpty(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"", ""},
		{"a", ""},
		{"abcd", ""},
		{"abcde", "bcde"},
		{"vck_live_9876", "9876"},
	} {
		if got := last4(tc.in); got != tc.want {
			t.Errorf("last4(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
