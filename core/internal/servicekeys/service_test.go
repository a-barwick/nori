package servicekeys_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"nori/internal/secret/rowstore"
	"nori/internal/servicekeys"
	"nori/internal/sim/memstore"
	"nori/internal/state/records"
	"nori/internal/users"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newEncryptedRowstore(t *testing.T) *rowstore.Store {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	ring, err := rowstore.ParseKeyRing(hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	u := users.User{
		ID:       "11111111-1111-4111-8111-111111111111",
		Email:    "exa-byok@example.com",
		TimeZone: "UTC",
	}
	db := records.NewShared(memstore.New(), u, 1.0)
	return &rowstore.Store{
		Resources: db,
		Keys:      ring,
		Owner:     u.DataOwner(),
	}
}

func TestServiceExaLifecycleEncryptedAndMasked(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			seenKey := r.Header.Get("x-api-key")
			status := http.StatusBadGateway
			body := `{}`
			if seenKey == "exa-valid-secret-9876" {
				status = http.StatusOK
				body = `{"results":[]}`
			} else if seenKey == "exa-bad-key-0000" {
				status = http.StatusUnauthorized
			}
			return &http.Response{
				StatusCode: status,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	}

	var notified []string
	store := newEncryptedRowstore(t)
	svc := &servicekeys.Service{
		Store:                 store,
		HTTPClient:            client,
		Now:                   func() time.Time { return now },
		ExaValidationEndpoint: "https://api.exa.ai/search",
		OnMutate: func(_ context.Context, service string) {
			notified = append(notified, service)
		},
	}

	// Initially unconfigured.
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Services) != 1 || list.Services[0].Service != servicekeys.ServiceExa || list.Services[0].Configured {
		t.Fatalf("unexpected initial list: %+v", list)
	}

	// Unknown service rejected.
	if _, err := svc.Set(ctx, "bogus", "abc12345"); !errors.Is(err, servicekeys.ErrUnknownService) {
		t.Fatalf("expected ErrUnknownService, got %v", err)
	}

	// Invalid key (401) rejected and not saved.
	if _, err := svc.Set(ctx, servicekeys.ServiceExa, "exa-bad-key-0000"); !errors.Is(err, servicekeys.ErrInvalidAPIKey) {
		t.Fatalf("expected ErrInvalidAPIKey, got %v", err)
	}
	if _, found, err := svc.ResolveKey(ctx, servicekeys.ServiceExa); err != nil || found {
		t.Fatalf("invalid key must not persist: found=%v err=%v", found, err)
	}

	// Upstream 502 returns ErrValidationFailed.
	if _, err := svc.Set(ctx, servicekeys.ServiceExa, "exa-upstream-502"); !errors.Is(err, servicekeys.ErrValidationFailed) {
		t.Fatalf("expected ErrValidationFailed, got %v", err)
	}

	// Valid key persists encrypted and returns masked status only.
	st, err := svc.Set(ctx, servicekeys.ServiceExa, "exa-valid-secret-9876")
	if err != nil {
		t.Fatalf("Set valid key: %v", err)
	}
	if !st.Configured || st.Last4 != "9876" || st.ValidatedAt == nil || st.UpdatedAt == nil {
		t.Fatalf("unexpected status: %+v", st)
	}
	rawJSON, _ := json.Marshal(st)
	if strings.Contains(string(rawJSON), "exa-valid-secret-9876") {
		t.Fatalf("ServiceStatus leaked raw secret: %s", string(rawJSON))
	}

	// ResolveKey decrypts cleanly.
	key, found, err := svc.ResolveKey(ctx, servicekeys.ServiceExa)
	if err != nil || !found || key != "exa-valid-secret-9876" {
		t.Fatalf("ResolveKey = (%q, %v, %v)", key, found, err)
	}

	// List also never leaks secret.
	list, err = svc.List(ctx)
	if err != nil {
		t.Fatalf("List after set: %v", err)
	}
	listJSON, _ := json.Marshal(list)
	if strings.Contains(string(listJSON), "exa-valid-secret-9876") {
		t.Fatalf("ListResponse leaked raw secret: %s", string(listJSON))
	}
	if !list.Services[0].Configured || list.Services[0].Last4 != "9876" {
		t.Fatalf("unexpected list after set: %+v", list.Services[0])
	}

	// Delete removes secret and triggers OnMutate hook.
	delStatus, err := svc.Delete(ctx, servicekeys.ServiceExa)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if delStatus.Configured || delStatus.Last4 != "" {
		t.Fatalf("unexpected status after delete: %+v", delStatus)
	}
	if _, found, err := svc.ResolveKey(ctx, servicekeys.ServiceExa); err != nil || found {
		t.Fatalf("ResolveKey after delete = found:%v err:%v", found, err)
	}
	if len(notified) != 2 || notified[0] != "exa" || notified[1] != "exa" {
		t.Fatalf("expected OnMutate called on Set and Delete, got %v", notified)
	}
}
