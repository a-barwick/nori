package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/api"
	"nori/internal/app"
	"nori/internal/platform"
	"nori/internal/push"
	"nori/internal/serviceauth"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/localfs"
	"nori/internal/state/postgres"
	"nori/internal/users"
)

func TestAccountDeletePostgres(t *testing.T) {
	dsn := os.Getenv("NORI_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("NORI_TEST_POSTGRES_URL is unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := settings.Settings{
		Cloud: "local", Role: "all", TaskBudgetUSD: 1,
		Features: settings.Features{DisableJev: true},
		Models:   settings.Models{BYOKRequired: true},
		Connections: settings.Connections{
			CredentialStore: "database", CredentialKey: strings.Repeat("42", 32),
		},
	}
	dir := t.TempDir()
	failPurge := false
	openService := func() (*app.Service, docstore.Backend) {
		t.Helper()
		backend, err := postgres.OpenBackend(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		storage := platform.NewStorage(backend, func(u users.User) state.ArtifactStore {
			fs, err := localfs.Open(filepath.Join(dir, u.FilePrefix()))
			if err != nil {
				t.Fatal(err)
			}
			return resetTestArtifacts{Store: fs, failPurge: &failPurge}
		})
		svc := app.OpenServiceWithStorage(storage, nil, cfg)
		t.Cleanup(svc.Close)
		return svc, backend
	}

	svc, _ := openService()
	otherReplica, _ := openService()

	ownerUser := users.User{
		ID: users.LegacyID, Email: "owner-" + uuid.NewString() + "@example.com",
		DisplayName: "Owner", TimeZone: "UTC",
	}
	alice := users.User{
		ID: uuid.NewString(), Email: "delete-alice-" + uuid.NewString() + "@example.com",
		DisplayName: "Alice", TimeZone: "America/Los_Angeles",
	}
	bob := users.User{
		ID: uuid.NewString(), Email: "delete-bob-" + uuid.NewString() + "@example.com",
		DisplayName: "Bob", TimeZone: "UTC",
	}
	for _, u := range []users.User{ownerUser, alice, bob} {
		if err := svc.Users.Create(ctx, u); err != nil {
			// Legacy owner might already exist in a shared DB; ignore duplicate for owner.
			if u.ID != users.LegacyID {
				t.Fatal(err)
			}
			existing, gErr := svc.Users.Get(ctx, users.LegacyID)
			if gErr != nil {
				t.Fatal(gErr)
			}
			ownerUser = existing
		}
	}

	callAPI := func(target *app.Service, method string, u users.User, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/users/"+u.ID+path, strings.NewReader(body))
		req = req.WithContext(serviceauth.WithVerifiedEmail(req.Context(), u.Email))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		target.Handler(false).ServeHTTP(rec, req)
		return rec
	}
	decodeCode := func(rec *httptest.ResponseRecorder) string {
		var out struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Code
	}

	// Seed Alice with preferences data so owner shard has records.
	savePrefsBody := `{"request_id":"` + uuid.NewString() + `","expected_revision":0,"value":{"timezone":"America/Los_Angeles","working_hours":[],"default_destination_ids":[],"account_defaults":{},"budgets":{"task_usd":3},"onboarding_completed_at":"2026-11-25T12:00:00Z"}}`
	if seed := callAPI(svc, http.MethodPut, alice, "/v1/preferences", savePrefsBody); seed.Code != http.StatusOK {
		t.Fatalf("seed alice prefs: status=%d body=%s", seed.Code, seed.Body.String())
	}
	// Seed push devices for both Alice and Bob.
	aliceToken := "del-alice-tok-" + uuid.NewString()
	bobToken := "del-bob-tok-" + uuid.NewString()
	if reg := callAPI(svc, http.MethodPost, alice, "/v1/devices/push", `{"token":"`+aliceToken+`","device_id":"alice-iphone","environment":"production"}`); reg.Code != http.StatusOK && reg.Code != http.StatusCreated {
		t.Fatalf("seed alice push device: status=%d body=%s", reg.Code, reg.Body.String())
	}
	if reg := callAPI(svc, http.MethodPost, bob, "/v1/devices/push", `{"token":"`+bobToken+`","device_id":"bob-iphone","environment":"production"}`); reg.Code != http.StatusOK && reg.Code != http.StatusCreated {
		t.Fatalf("seed bob push device: status=%d body=%s", reg.Code, reg.Body.String())
	}

	// 1. Legacy owner account cannot be deleted.
	rec := callAPI(svc, http.MethodPost, ownerUser, "/v1/settings/account/delete", `{"confirmation":"`+ownerUser.Email+`"}`)
	if rec.Code != http.StatusForbidden || decodeCode(rec) != "account_delete_forbidden" {
		t.Fatalf("owner delete: status=%d code=%q body=%s", rec.Code, decodeCode(rec), rec.Body.String())
	}

	// 2. Wrong email confirmation is rejected with 400.
	rec = callAPI(svc, http.MethodPost, alice, "/v1/settings/account/delete", `{"confirmation":"wrong@example.com"}`)
	if rec.Code != http.StatusBadRequest || decodeCode(rec) != "validation_failed" {
		t.Fatalf("wrong email: status=%d code=%q body=%s", rec.Code, decodeCode(rec), rec.Body.String())
	}

	// 3. Active lock / busy user returns 409 account_delete_busy.
	guard := otherReplica.Backend().(docstore.UserDataResetter)
	unlockShared, err := guard.LockUserData(ctx, alice.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	rec = callAPI(svc, http.MethodPost, alice, "/v1/settings/account/delete", `{"confirmation":"`+alice.Email+`"}`)
	unlockShared()
	if rec.Code != http.StatusConflict || decodeCode(rec) != "account_delete_busy" {
		t.Fatalf("busy delete: status=%d code=%q body=%s", rec.Code, decodeCode(rec), rec.Body.String())
	}

	// 4. Artifact purge failure marks delete_pending, returns 502 account_delete_pending,
	//    blocks other API & worker operations for Alice, and leaves Bob unaffected.
	failPurge = true
	rec = callAPI(svc, http.MethodPost, alice, "/v1/settings/account/delete", `{"confirmation":"`+alice.Email+`"}`)
	if rec.Code != http.StatusBadGateway || decodeCode(rec) != "account_delete_pending" {
		t.Fatalf("failed purge: status=%d code=%q body=%s", rec.Code, decodeCode(rec), rec.Body.String())
	}
	blocked := callAPI(otherReplica, http.MethodGet, alice, "/v1/preferences", "")
	if blocked.Code != http.StatusConflict || decodeCode(blocked) != "account_delete_pending" {
		t.Fatalf("blocked request while pending: status=%d code=%q body=%s", blocked.Code, decodeCode(blocked), blocked.Body.String())
	}
	if _, _, err := otherReplica.AcquireByID(ctx, alice.ID); !errors.Is(err, api.ErrAccountDeletePending) {
		t.Fatalf("AcquireByID while pending: got %v, want ErrAccountDeletePending", err)
	}
	bobPrefs := callAPI(otherReplica, http.MethodGet, bob, "/v1/preferences", "")
	if bobPrefs.Code != http.StatusOK {
		t.Fatalf("bob preferences affected: status=%d body=%s", bobPrefs.Code, bobPrefs.Body.String())
	}

	// 5. Retry with resume_pending: true succeeds, removes user row and all data.
	failPurge = false
	rec = callAPI(svc, http.MethodPost, alice, "/v1/settings/account/delete", `{"confirmation":"`+strings.ToUpper(alice.Email)+`","resume_pending":true}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("retry delete: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := svc.Users.Get(ctx, alice.ID); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("deleted user still exists in directory: err=%v", err)
	}
	afterDelete := callAPI(otherReplica, http.MethodGet, alice, "/v1/preferences", "")
	if afterDelete.Code != http.StatusForbidden {
		t.Fatalf("post-delete API status = %d, want 403", afterDelete.Code)
	}
	// Bob is still completely intact, including his push device; Alice's push device is deleted.
	bobAfter := callAPI(svc, http.MethodGet, bob, "/v1/preferences", "")
	if bobAfter.Code != http.StatusOK {
		t.Fatalf("bob preferences after alice delete: status=%d body=%s", bobAfter.Code, bobAfter.Body.String())
	}
	ps, err := push.OpenPostgresStore(ctx, dsn, 2)
	if err != nil {
		t.Fatalf("OpenPostgresStore: %v", err)
	}
	defer ps.Close()
	if aliceDevs, err := ps.ListDevices(ctx, alice.ID); err != nil || len(aliceDevs) != 0 {
		t.Fatalf("alice push devices after account delete = %+v, err=%v; want 0", aliceDevs, err)
	}
	if bobDevs, err := ps.ListDevices(ctx, bob.ID); err != nil || len(bobDevs) != 1 {
		t.Fatalf("bob push devices after alice delete = %+v, err=%v; want 1", bobDevs, err)
	}
}

func TestAccountDeleteAcceptsVerifiedCurrentIdentityEmail(t *testing.T) {
	dsn := os.Getenv("NORI_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("NORI_TEST_POSTGRES_URL is unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	backend, err := postgres.OpenBackend(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	storage := platform.NewStorage(backend, func(u users.User) state.ArtifactStore {
		files, err := localfs.Open(filepath.Join(root, u.FilePrefix()))
		if err != nil {
			t.Fatal(err)
		}
		return files
	})
	svc := app.OpenServiceWithStorage(storage, nil, settings.Settings{
		Cloud: "local", Role: "all", TaskBudgetUSD: 1,
		Features: settings.Features{DisableJev: true}, Models: settings.Models{BYOKRequired: true},
		Connections: settings.Connections{CredentialStore: "database", CredentialKey: strings.Repeat("42", 32)},
	})
	t.Cleanup(svc.Close)
	createUser := func() (users.User, users.Identity) {
		t.Helper()
		id := uuid.NewString()
		identity := users.Identity{Issuer: "https://accounts.google.com", Subject: "subject-" + id}
		row := users.User{
			ID: id, Email: "old-" + id + "@example.com", TimeZone: "UTC",
			IdentityIssuer: identity.Issuer, IdentitySubject: identity.Subject,
		}
		if err := svc.Users.Create(ctx, row); err != nil {
			t.Fatal(err)
		}
		return row, identity
	}
	deleteRequest := func(row users.User, confirmation string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/users/"+row.ID+"/v1/settings/account/delete", strings.NewReader(`{"confirmation":"`+confirmation+`"}`))
		req.Header.Set("Content-Type", "application/json")
		return req
	}
	assertStatus := func(rec *httptest.ResponseRecorder, want int) {
		t.Helper()
		if rec.Code != want {
			t.Fatalf("status = %d, want %d: %s", rec.Code, want, rec.Body.String())
		}
	}

	native, nativeIdentity := createUser()
	currentEmail := "current-" + native.ID + "@example.com"
	req := deleteRequest(native, currentEmail)
	req = req.WithContext(serviceauth.WithVerifiedIdentity(req.Context(), currentEmail, nativeIdentity))
	rec := httptest.NewRecorder()
	svc.Handler(false).ServeHTTP(rec, req)
	assertStatus(rec, http.StatusNoContent)

	web, webIdentity := createUser()
	webEmail := "current-" + web.ID + "@example.com"
	auth := serviceauth.Config{Key: strings.Repeat("x", 32), Audience: "nori-test"}
	token, err := auth.Token()
	if err != nil {
		t.Fatal(err)
	}
	serviceDelete := func(subject string) *httptest.ResponseRecorder {
		t.Helper()
		req := deleteRequest(web, webEmail)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Nori-Identity-Issuer", webIdentity.Issuer)
		req.Header.Set("X-Nori-Identity-Subject", subject)
		req.Header.Set("X-Nori-Identity-Email", webEmail)
		rec := httptest.NewRecorder()
		auth.Protect(svc.Handler(false)).ServeHTTP(rec, req)
		return rec
	}
	assertStatus(serviceDelete("different-subject"), http.StatusBadRequest)
	assertStatus(serviceDelete(webIdentity.Subject), http.StatusNoContent)
}
