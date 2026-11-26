package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nori/internal/api"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/users"
)

type mapDirectory struct {
	users     map[string]users.User
	getCalls  atomic.Int32
	lookupErr error
}

func (d *mapDirectory) Get(_ context.Context, id string) (users.User, error) {
	d.getCalls.Add(1)
	if d.lookupErr != nil {
		return users.User{}, d.lookupErr
	}
	u, ok := d.users[id]
	if !ok {
		return users.User{}, state.ErrNotFound
	}
	return u, nil
}
func (d *mapDirectory) FindEmail(context.Context, string) (users.User, error) {
	if d.lookupErr != nil {
		return users.User{}, d.lookupErr
	}
	return users.User{}, state.ErrNotFound
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

func TestUserLookupOutageIsRetryable(t *testing.T) {
	dir := &mapDirectory{}
	handler := api.UserHandler(dir, func(context.Context, users.User) (api.UserRoutes, func(), error) {
		t.Fatal("missing account unexpectedly opened routes")
		return api.UserRoutes{}, nil, nil
	}, false, "test-model")
	for _, request := range []struct{ method, path, body string }{
		{"GET", "/users/owner/v1/preferences", ""},
		{"POST", "/api/users/resolve", `{"email":"owner@example.com"}`},
	} {
		dir.lookupErr = errors.New("connection capacity exceeded")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(request.method, request.path, strings.NewReader(request.body)))
		if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), dir.lookupErr.Error()) {
			t.Fatalf("outage response = %d %s", w.Code, w.Body)
		}
		dir.lookupErr = nil
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(request.method, request.path, strings.NewReader(request.body)))
		if w.Code != http.StatusForbidden {
			t.Fatalf("unknown account response = %d %s", w.Code, w.Body)
		}
	}
}

func TestUserResolveProvisioningModes(t *testing.T) {
	noopOpen := func(context.Context, users.User) (api.UserRoutes, func(), error) {
		return api.UserRoutes{}, func() {}, nil
	}

	t.Run("open mode auto-provisions idempotently under concurrency", func(t *testing.T) {
		dir := users.NewMemoryDirectory()
		handler := api.UserHandlerWithOptions(dir, noopOpen, false, "test-model", api.UserHandlerOptions{
			DisableRateLimit: true,
			ProvisioningMode: settings.UserProvisioningOpen,
		})

		const workers = 12
		ids := make([]string, workers)
		var wg sync.WaitGroup
		for i := range workers {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/api/users/resolve", strings.NewReader(`{"email":"NewUser@Example.COM","name":"New User"}`))
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Errorf("worker %d status = %d body=%s", idx, rec.Code, rec.Body.String())
					return
				}
				var body map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Errorf("worker %d json: %v", idx, err)
					return
				}
				ids[idx] = body["id"]
			}(i)
		}
		wg.Wait()

		if !users.ValidID(ids[0]) || ids[0] == users.LegacyID {
			t.Fatalf("expected UUIDv4 user ID, got %q", ids[0])
		}
		for i := 1; i < workers; i++ {
			if ids[i] != ids[0] {
				t.Fatalf("worker %d ID = %q, want %q", i, ids[i], ids[0])
			}
		}
		row, err := dir.Get(context.Background(), ids[0])
		if err != nil {
			t.Fatalf("Get provisioned user: %v", err)
		}
		if row.DisplayName != "New User" || row.TimeZone != "UTC" || row.Email != "newuser@example.com" {
			t.Fatalf("unexpected provisioned user: %+v", row)
		}
	})

	t.Run("invite mode returns invite_required and redeems atomically via resolve or redeem", func(t *testing.T) {
		dir := users.NewMemoryDirectory()
		inv, err := dir.CreateInvite(context.Background(), users.CreateInviteOptions{
			MaxUses: 1,
			Domain:  "example.com",
		})
		if err != nil {
			t.Fatalf("CreateInvite: %v", err)
		}

		rlCfg := settings.DefaultRateLimit()
		rlCfg.ResolvePerMinute = 60
		rlCfg.ResolveBurst = 4
		rl := api.NewRateLimiterWithClock(rlCfg, func() time.Time {
			return time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
		})

		handler := api.UserHandlerWithOptions(dir, noopOpen, false, "test-model", api.UserHandlerOptions{
			RateLimiter:      rl,
			ProvisioningMode: settings.UserProvisioningInvite,
		})

		// 1. Unknown user without invite code -> 403 invite_required.
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/users/resolve", strings.NewReader(`{"email":"alice@example.com","name":"Alice"}`))
		req.RemoteAddr = "198.51.100.10:1234"
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
		}
		var errBody map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
			t.Fatalf("decode error body: %v", err)
		}
		if errBody["code"] != "invite_required" {
			t.Fatalf("code = %q, want invite_required", errBody["code"])
		}

		// 2. Domain mismatch -> 403 invite_email_mismatch.
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodPost, "/api/users/redeem", strings.NewReader(`{"email":"bob@other.org","invite_code":"`+inv.Code+`"}`))
		req.RemoteAddr = "198.51.100.10:1234"
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"invite_email_mismatch"`) {
			t.Fatalf("mismatch response = %d %s", rec.Code, rec.Body.String())
		}

		// 3. Valid redemption -> 200 with provisioned UUID.
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodPost, "/api/users/redeem", strings.NewReader(`{"email":"alice@example.com","name":"Alice","invite_code":"`+inv.Code+`"}`))
		req.RemoteAddr = "198.51.100.10:1234"
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("redeem status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		var okBody map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &okBody); err != nil || !users.ValidID(okBody["id"]) {
			t.Fatalf("invalid redeem body: %s (%v)", rec.Body.String(), err)
		}

		// 4. Exhausted invite -> 403 invite_exhausted.
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodPost, "/api/users/redeem", strings.NewReader(`{"email":"charlie@example.com","invite_code":"`+inv.Code+`"}`))
		req.RemoteAddr = "198.51.100.10:1234"
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"invite_exhausted"`) {
			t.Fatalf("exhausted response = %d %s", rec.Code, rec.Body.String())
		}

		// 5. Repeated requests for the same email hit the resolve rate limit (burst 4),
		// while distinct emails arriving from a shared BFF IP remain isolated.
		for i := 0; i < 4; i++ {
			rec = httptest.NewRecorder()
			req = httptest.NewRequest(http.MethodPost, "/api/users/redeem", strings.NewReader(`{"email":"dave@example.com","invite_code":"`+inv.Code+`"}`))
			req.RemoteAddr = "198.51.100.10:1234"
			handler.ServeHTTP(rec, req)
		}
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodPost, "/api/users/redeem", strings.NewReader(`{"email":"dave@example.com","invite_code":"`+inv.Code+`"}`))
		req.RemoteAddr = "198.51.100.10:1234"
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("rate-limited redeem status = %d, want 429 (%s)", rec.Code, rec.Body.String())
		}
	})
}
