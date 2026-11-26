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
	"nori/internal/app"
	"nori/internal/connections"
	"nori/internal/platform"
	"nori/internal/serviceauth"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/localfs"
	"nori/internal/state/postgres"
	"nori/internal/users"
)

type resetTestArtifacts struct {
	*localfs.Store
	failPurge *bool
}

func (s resetTestArtifacts) Purge(ctx context.Context) error {
	if *s.failPurge {
		return errors.New("simulated artifact store failure")
	}
	return s.Store.Purge(ctx)
}

func TestUserDataResetPostgres(t *testing.T) {
	dsn := os.Getenv("NORI_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("NORI_TEST_POSTGRES_URL is unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := settings.Settings{
		Cloud: "local", Role: "all", TaskBudgetUSD: 1,
		Features: settings.Features{DisableJev: true},
		Models:   settings.Models{Provider: "openai", OpenAIKey: "unused-test-key"},
		Connections: settings.Connections{
			CredentialStore: "database", CredentialKey: strings.Repeat("42", 32),
		},
	}
	dir := t.TempDir()
	offline := false
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
			return resetTestArtifacts{Store: fs, failPurge: &offline}
		})
		svc := app.OpenServiceWithStorage(storage, nil, cfg)
		t.Cleanup(svc.Close)
		return svc, backend
	}
	svc, backend := openService()
	otherReplica, _ := openService()
	alice := users.User{
		ID: uuid.NewString(), Email: "reset-" + uuid.NewString() + "@example.com",
		DisplayName: "Previous display name", TimeZone: "Europe/London",
	}
	if err := svc.Users.Create(ctx, alice); err != nil {
		t.Fatal(err)
	}
	bob := createUser(t, svc, uuid.NewString(), "reset-"+uuid.NewString()+"@example.com")
	leases := backend.(docstore.LeaseStore)
	index := backend.(docstore.MemoryIndex)
	upload := state.UploadKey(uuid.NewString())
	events := "owners/" + alice.DataOwner() + "/events"
	docs := docstore.New(backend)
	if err := docs.RunTransaction(ctx, func(_ context.Context, tx *docstore.Transaction) error {
		head := docs.Collection("owners").Doc(alice.DataOwner()).Collection("event_head").Doc("head")
		_, _, err := tx.NextSequence(head, 12)
		if err != nil {
			return err
		}
		return tx.Create(docs.Collection(events).Doc("00000000000000000012"),
			state.LogEvent{Seq: 12, Kind: state.EventMessage, Ref: "old-message", CreatedAt: time.Now().UTC()})
	}); err != nil {
		t.Fatal(err)
	}

	api := func(svc *app.Service, method string, target, viewer users.User, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/users/"+target.ID+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(serviceauth.WithVerifiedEmail(ctx, viewer.Email))
		w := httptest.NewRecorder()
		svc.Handler(false).ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, w.Code, want, w.Body)
		}
		return w
	}
	prefs := func(svc *app.Service, u users.User) connections.Preferences {
		t.Helper()
		w := api(svc, "GET", u, u, "/v1/preferences", "", 200)
		var out connections.Preferences
		if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	savePrefs := func(svc *app.Service, u users.User, want int) {
		t.Helper()
		body := `{"request_id":"` + uuid.NewString() + `","expected_revision":0,"value":{
			"timezone":"America/Los_Angeles","working_hours":[],"default_destination_ids":[],
			"account_defaults":{},"budgets":{"task_usd":3},"onboarding_completed_at":"2026-11-25T12:00:00Z"}}`
		api(svc, "PUT", u, u, "/v1/preferences", body, want)
	}
	markerPath := func(u users.User) string { return "owners/" + u.DataOwner() + "/records_for_reset_test/record" }
	for _, u := range []users.User{alice, bob} {
		savePrefs(svc, u, 200)
		a, release, err := svc.AcquireByID(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		putErr := a.Store.PutFile(ctx, upload, "text/plain", []byte(u.Email))
		release()
		if putErr != nil {
			t.Fatal(putErr)
		}
		if err := backend.Apply(ctx, docstore.Mutation{Kind: "set", Path: markerPath(u), Fields: map[string]any{"value": u.Email}}); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		if err := index.UpsertMemoryChunk(ctx, docstore.MemoryChunk{
			Owner: u.DataOwner(), ChunkID: "core:secret", Layer: docstore.MemoryLayerCore,
			Body: u.Email, Provenance: "user", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	oldApp, release, err := otherReplica.AcquireByID(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := oldApp.Store.GetFile(ctx, upload)
	release()
	if readErr != nil {
		t.Fatal(readErr)
	}

	resetPath := "/v1/settings/reset"
	confirm := `{"confirmation":"RESET"}`
	api(svc, "POST", alice, alice, resetPath, `{"confirmation":"reset"}`, 400)
	api(svc, "POST", alice, bob, resetPath, confirm, 403)
	guard := otherReplica.Backend().(docstore.UserDataResetter)
	release, err = guard.LockUserData(ctx, alice.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	api(svc, "POST", alice, alice, resetPath, confirm, 409)
	release()

	run := uuid.NewString()
	if err := leases.WakeTask(ctx, alice.ID, run, time.Time{}); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := leases.ClaimTaskByID(ctx, alice.ID, run, "worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim task: %t, %v", ok, err)
	}
	api(svc, "POST", alice, alice, resetPath, confirm, 409)
	if err := leases.ReleaseTask(ctx, alice.ID, run, "worker", claim.WakeSeq, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := leases.WakeUser(ctx, alice.ID, docstore.LaneScheduler, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	offline = true
	api(svc, "POST", alice, alice, resetPath, confirm, 502)
	if p := prefs(otherReplica, alice); p.OnboardingCompletedAt == nil || p.DataResetAt != "" {
		t.Fatalf("interrupted reset changed preferences: %+v", p)
	}
	api(otherReplica, "GET", alice, alice, "/v1/snapshot", "", 200)
	savePrefs(otherReplica, alice, 409)
	if _, _, err := otherReplica.AcquireByID(ctx, alice.ID); !errors.Is(err, docstore.ErrUserDataResetPending) {
		t.Fatalf("worker can acquire resetting account: %v", err)
	}

	offline = false
	api(otherReplica, "POST", alice, alice, resetPath, confirm, 204)
	identity := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/users/resolve", strings.NewReader(`{}`))
	req = req.WithContext(serviceauth.WithVerifiedEmail(ctx, alice.Email))
	svc.Handler(false).ServeHTTP(identity, req)
	if identity.Code != 200 || !strings.Contains(identity.Body.String(), alice.ID) {
		t.Fatalf("original sign-in did not resolve: %d %s", identity.Code, identity.Body)
	}
	current, err := svc.Users.Get(ctx, alice.ID)
	if err != nil || current.Email != alice.Email || current.DisplayName != "" || current.TimeZone != "UTC" {
		t.Fatalf("reset did not clear the old profile while preserving sign-in: %+v, %v", current, err)
	}
	fresh := prefs(svc, alice)
	if fresh.Revision != 0 || fresh.Timezone != "UTC" || fresh.OnboardingCompletedAt != nil || fresh.DataResetAt == "" {
		t.Fatalf("preferences after reset: %+v", fresh)
	}
	if _, err := backend.Get(ctx, markerPath(alice)); !errors.Is(err, docstore.ErrNotFound) {
		t.Fatalf("account record survived reset: %v", err)
	}
	if rows, err := index.ListMemoryChunks(ctx, alice.DataOwner()); err != nil || len(rows) != 0 {
		t.Fatalf("memory index: %d rows, %v", len(rows), err)
	}
	if rows, err := leases.ListTaskLeases(ctx, alice.ID); err != nil || len(rows) != 0 {
		t.Fatalf("task leases: %d rows, %v", len(rows), err)
	}
	if rows, err := leases.ListUserWakes(ctx, alice.ID); err != nil || len(rows) != 0 {
		t.Fatalf("user wakes: %d rows, %v", len(rows), err)
	}
	rows, err := backend.Query(ctx, docstore.QuerySpec{Collection: events})
	if err != nil || len(rows) != 1 || rows[0].Fields["kind"] != state.EventDataReset || rows[0].Fields["seq"] != int64(13) {
		t.Fatalf("event history was not replaced by a reset signal at the next sequence: %+v, %v", rows, err)
	}
	for _, replica := range []*app.Service{svc, otherReplica} {
		a, release, err := replica.AcquireByID(ctx, alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = a.Store.GetFile(ctx, upload)
		release()
		if !errors.Is(err, state.ErrNotFound) {
			t.Fatalf("account upload survived reset: %v", err)
		}
	}
	for i, replica := range []*app.Service{svc, otherReplica, svc} {
		a, release, err := replica.AcquireByID(ctx, alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		switch i {
		case 0:
			err = a.Store.PutFile(ctx, upload, "text/plain", []byte("fresh data"))
		default:
			var data []byte
			data, err = a.Store.GetFile(ctx, upload)
			if err == nil && string(data) != "fresh data" {
				err = errors.New("fresh upload had wrong content")
			}
		}
		release()
		if err != nil {
			t.Fatalf("replica %d cannot use the new artifact key: %v", i, err)
		}
	}
	savePrefs(svc, alice, 200)
	if p := prefs(otherReplica, alice); p.DataResetAt != fresh.DataResetAt || p.OnboardingCompletedAt == nil {
		t.Fatalf("onboarding could not finish again: %+v", p)
	}
	if p := prefs(svc, bob); p.OnboardingCompletedAt == nil || p.DataResetAt != "" {
		t.Fatalf("another user's preferences changed: %+v", p)
	}
	if _, err := backend.Get(ctx, markerPath(bob)); err != nil {
		t.Fatalf("another user's record changed: %v", err)
	}
	if rows, err := index.ListMemoryChunks(ctx, bob.DataOwner()); err != nil || len(rows) != 1 {
		t.Fatalf("another user's memory index changed: %d rows, %v", len(rows), err)
	}
	bobApp, release, err := svc.AcquireByID(ctx, bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := bobApp.Store.GetFile(ctx, upload)
	release()
	if err != nil || string(data) != bob.Email {
		t.Fatalf("another user's upload changed: %v", err)
	}
}
