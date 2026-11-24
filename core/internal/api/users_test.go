package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"nori/internal/api"
	"nori/internal/users"
)

type mapDirectory struct {
	users    map[string]users.User
	getCalls atomic.Int32
}

func (d *mapDirectory) Get(_ context.Context, id string) (users.User, error) {
	d.getCalls.Add(1)
	u, ok := d.users[id]
	if !ok {
		return users.User{}, errors.New("not found")
	}
	return u, nil
}
func (d *mapDirectory) FindEmail(context.Context, string) (users.User, error) {
	return users.User{}, errors.New("not found")
}
func (d *mapDirectory) Create(context.Context, users.User) error   { return nil }
func (d *mapDirectory) List(context.Context) ([]users.User, error) { return nil, nil }
func (d *mapDirectory) Paused(context.Context) (bool, error)       { return false, nil }
func (d *mapDirectory) Close() error                               { return nil }

func TestUnprefixedAPIProcessReturns403(t *testing.T) {
	legacy := users.User{
		ID:          users.LegacyID,
		Email:       "legacy@example.com",
		DisplayName: "Legacy Owner",
		TimeZone:    "UTC",
	}
	dir := &mapDirectory{
		users: map[string]users.User{
			legacy.ID: legacy,
		},
	}
	var openCalls atomic.Int32
	var seenPath, seenUserID string

	handler := api.UserHandler(
		dir,
		func(_ context.Context, row users.User) (api.UserRoutes, func(), error) {
			openCalls.Add(1)
			return api.UserRoutes{
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					seenPath = r.URL.Path
					seenUserID = users.ID(r.Context())
					w.WriteHeader(http.StatusOK)
				}),
			}, func() {}, nil
		},
		false,
		"test-model",
	)

	// Un-prefixed POST /api/process must NOT fall back to users.LegacyID and must return 403.
	req := httptest.NewRequest(http.MethodPost, "/api/process", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /api/process status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
	if openCalls.Load() != 0 {
		t.Fatalf("POST /api/process unexpectedly opened user routes %d times", openCalls.Load())
	}
	if dir.getCalls.Load() != 0 {
		t.Fatalf("POST /api/process unexpectedly queried directory.Get %d times", dir.getCalls.Load())
	}

	// Prefixed POST /users/owner/api/process still routes cleanly to users.LegacyID.
	prefixedReq := httptest.NewRequest(http.MethodPost, "/users/"+users.LegacyID+"/api/process", strings.NewReader(`{}`))
	prefixedReq.Header.Set("Content-Type", "application/json")
	prefixedRec := httptest.NewRecorder()
	handler.ServeHTTP(prefixedRec, prefixedReq)
	if prefixedRec.Code != http.StatusOK {
		t.Fatalf("POST /users/%s/api/process status = %d, want 200 (body=%s)", users.LegacyID, prefixedRec.Code, prefixedRec.Body.String())
	}
	if openCalls.Load() != 1 || seenPath != "/api/process" || seenUserID != users.LegacyID {
		t.Fatalf("prefixed route saw openCalls=%d path=%q userID=%q, want 1 /api/process %q", openCalls.Load(), seenPath, seenUserID, users.LegacyID)
	}
}
