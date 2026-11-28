package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/users"
)

type fakeRefs struct {
	users     []users.User
	snapshots map[string]UserSnapshot
	released  []string
}

func (f *fakeRefs) ListUsers(context.Context) ([]users.User, error) {
	return f.users, nil
}

func (f *fakeRefs) InspectUser(_ context.Context, user users.User) (UserSnapshot, func(), error) {
	snap := f.snapshots[user.ID]
	snap.User = user
	if snap.ReferencedKeys == nil {
		snap.ReferencedKeys = map[string]struct{}{}
	}
	return snap, func() { f.released = append(f.released, user.ID) }, nil
}

type fakeObjectStore struct {
	byPrefix map[string][]Object
	deleted  []string
}

func (f *fakeObjectStore) ListObjects(_ context.Context, _ string, prefix string) ([]Object, error) {
	return append([]Object(nil), f.byPrefix[prefix]...), nil
}

func (f *fakeObjectStore) DeleteObject(_ context.Context, _, name, gen string) error {
	f.deleted = append(f.deleted, name+"@"+gen)
	return nil
}

func TestReconcileDryRunReferencedRecentAndOrphans(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	alice := users.User{ID: "11111111-1111-4111-8111-111111111111", Email: "alice@example.com", TimeZone: "UTC"}
	lockedUser := users.User{ID: "22222222-2222-4222-8222-222222222222", Email: "bob@example.com", TimeZone: "UTC"}
	resettingUser := users.User{ID: "33333333-3333-4333-8333-333333333333", Email: "carol@example.com", TimeZone: "UTC", ResetPending: true}
	legacy := users.User{ID: users.LegacyID, Email: "owner@example.com", TimeZone: "UTC"}

	alicePrefix := "artifacts/users/" + alice.ID + "/"
	refs := &fakeRefs{
		users: []users.User{alice, lockedUser, resettingUser, legacy},
		snapshots: map[string]UserSnapshot{
			alice.ID: {
				ReferencedKeys: map[string]struct{}{
					"uploads/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa":                                  {},
					"results/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb":                                  {},
					"cccccccc-cccc-4ccc-8ccc-cccccccccccc/dddddddd-dddd-4ddd-8ddd-dddddddddddd.png": {},
				},
			},
			lockedUser.ID: {Locked: true},
		},
	}
	oldest := now.Add(-72 * time.Hour)
	newest := now.Add(-30 * time.Hour)
	store := &fakeObjectStore{
		byPrefix: map[string][]Object{
			alicePrefix: {
				// Referenced upload, result, and screenshot — must never be flagged.
				{Name: alicePrefix + "uploads/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Generation: "1", Size: 500, Updated: now.Add(-100 * time.Hour)},
				{Name: alicePrefix + "results/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Generation: "2", Size: 600, Updated: now.Add(-100 * time.Hour)},
				{Name: alicePrefix + "cccccccc-cccc-4ccc-8ccc-cccccccccccc/dddddddd-dddd-4ddd-8ddd-dddddddddddd.png", Generation: "3", Size: 700, Updated: now.Add(-100 * time.Hour)},
				// Unreferenced but recent (<24h) — must never be flagged.
				{Name: alicePrefix + "uploads/eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", Generation: "4", Size: 800, Updated: now.Add(-2 * time.Hour)},
				// Unreferenced and older than 24h — must be flagged.
				{Name: alicePrefix + "uploads/10000000-0000-4000-8000-000000000001", Generation: "5", Size: 1024, Updated: oldest},
				{Name: alicePrefix + "uploads/10000000-0000-4000-8000-000000000002", Generation: "6", Size: 2048, Updated: newest},
			},
			"artifacts/users/" + lockedUser.ID + "/": {
				{Name: "artifacts/users/" + lockedUser.ID + "/uploads/99999999-9999-4999-8999-999999999999", Generation: "9", Size: 999, Updated: oldest},
			},
		},
	}

	report, err := Reconcile(context.Background(), refs, store, Options{
		Bucket:      "nori-bucket",
		GraceWindow: 24 * time.Hour,
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Reconcile dry-run: %v", err)
	}
	if !report.DryRun {
		t.Fatalf("expected DryRun=true")
	}
	if report.UsersScanned != 1 || report.UsersSkippedLocked != 2 {
		t.Fatalf("users scanned=%d skipped=%d, want 1 and 2", report.UsersScanned, report.UsersSkippedLocked)
	}
	if report.ObjectsScanned != 6 {
		t.Fatalf("ObjectsScanned = %d, want 6", report.ObjectsScanned)
	}
	if report.OrphanCount != 2 || report.OrphanBytes != 3072 {
		t.Fatalf("orphans count=%d bytes=%d, want 2 and 3072", report.OrphanCount, report.OrphanBytes)
	}
	if !report.OldestOrphan.Equal(oldest) || !report.NewestOrphan.Equal(newest) {
		t.Fatalf("timestamps oldest=%v newest=%v, want %v and %v", report.OldestOrphan, report.NewestOrphan, oldest, newest)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("dry-run must not delete anything, deleted %v", store.deleted)
	}
}

func TestReconcileDeleteSafetyGuardsAndCap(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	alice := users.User{ID: "11111111-1111-4111-8111-111111111111", Email: "alice@example.com", TimeZone: "UTC"}
	prefix := "artifacts/users/" + alice.ID + "/"

	refs := &fakeRefs{
		users:     []users.User{alice},
		snapshots: map[string]UserSnapshot{alice.ID: {}},
	}
	store := &fakeObjectStore{
		byPrefix: map[string][]Object{
			prefix: {
				{Name: prefix + "uploads/10000000-0000-4000-8000-000000000001", Generation: "11", Size: 100, Updated: now.Add(-48 * time.Hour)},
				{Name: prefix + "uploads/10000000-0000-4000-8000-000000000002", Generation: "12", Size: 200, Updated: now.Add(-48 * time.Hour)},
				{Name: prefix + "uploads/10000000-0000-4000-8000-000000000003", Generation: "13", Size: 300, Updated: now.Add(-48 * time.Hour)},
			},
		},
	}

	// --delete without --confirm must refuse and delete nothing.
	if _, err := Reconcile(context.Background(), refs, store, Options{
		Bucket:    "nori-bucket",
		Delete:    true,
		Confirm:   false,
		MaxDelete: 10,
		Now:       func() time.Time { return now },
	}); err == nil || !strings.Contains(err.Error(), "--confirm") {
		t.Fatalf("expected --confirm guard error, got %v", err)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("expected 0 deletions without --confirm, got %v", store.deleted)
	}

	// --delete with --confirm and --max-delete=2 deletes exactly 2 and marks cap reached.
	report, err := Reconcile(context.Background(), refs, store, Options{
		Bucket:    "nori-bucket",
		Delete:    true,
		Confirm:   true,
		MaxDelete: 2,
		Now:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Reconcile delete: %v", err)
	}
	if report.DryRun || report.OrphanCount != 3 || report.DeletedCount != 2 || report.DeletedBytes != 300 || !report.DeleteCapReached {
		t.Fatalf("unexpected delete report: %+v", report)
	}
	if len(store.deleted) != 2 {
		t.Fatalf("deleted = %v, want 2 items", store.deleted)
	}
}

func TestReconcileEnforcesCrossUserPrefixIsolation(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	alice := users.User{ID: "11111111-1111-4111-8111-111111111111", Email: "alice@example.com", TimeZone: "UTC"}
	bobID := "22222222-2222-4222-8222-222222222222"
	alicePrefix := "artifacts/users/" + alice.ID + "/"

	refs := &fakeRefs{
		users:     []users.User{alice},
		snapshots: map[string]UserSnapshot{alice.ID: {}},
	}
	store := &fakeObjectStore{
		byPrefix: map[string][]Object{
			alicePrefix: {
				{Name: "artifacts/users/" + bobID + "/uploads/10000000-0000-4000-8000-000000000001", Generation: "50", Size: 100, Updated: now.Add(-72 * time.Hour)},
			},
		},
	}

	_, err := Reconcile(context.Background(), refs, store, Options{
		Bucket:    "nori-bucket",
		Delete:    true,
		Confirm:   true,
		MaxDelete: 10,
		Now:       func() time.Time { return now },
	})
	if err == nil || !strings.Contains(err.Error(), "foreign key") {
		t.Fatalf("expected foreign key isolation error, got %v", err)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("must never delete foreign user's object, deleted %v", store.deleted)
	}
}

type fakeFullRefs struct {
	*fakeRefs
	expiredKeys  map[string][]string
	expiredBytes map[string]int64
	storedQuota  map[string]int64
}

func (f *fakeFullRefs) ExpireUnattachedUploads(_ context.Context, user users.User, _ time.Time, _ bool) ([]string, int64, error) {
	return f.expiredKeys[user.ID], f.expiredBytes[user.ID], nil
}

func (f *fakeFullRefs) ReconcileUserStorageBytes(_ context.Context, user users.User, actual int64) (int64, error) {
	prev := f.storedQuota[user.ID]
	f.storedQuota[user.ID] = actual
	return prev, nil
}

func TestReconcileExpiresUnattachedUploadsAndReconcilesQuota(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	alice := users.User{ID: "11111111-1111-4111-8111-111111111111", Email: "alice@example.com", TimeZone: "UTC"}
	prefix := "artifacts/users/" + alice.ID + "/"

	refs := &fakeFullRefs{
		fakeRefs: &fakeRefs{
			users: []users.User{alice},
			snapshots: map[string]UserSnapshot{
				alice.ID: {
					ReferencedKeys: map[string]struct{}{
						"uploads/11111111-1111-4111-8111-111111111111": {}, // retained
						"uploads/22222222-2222-4222-8222-222222222222": {}, // >24h unattached, to expire
					},
				},
			},
		},
		expiredKeys: map[string][]string{
			alice.ID: {"uploads/22222222-2222-4222-8222-222222222222"},
		},
		expiredBytes: map[string]int64{
			alice.ID: 4000,
		},
		storedQuota: map[string]int64{
			alice.ID: 9999, // drifted quota
		},
	}
	store := &fakeObjectStore{
		byPrefix: map[string][]Object{
			prefix: {
				{Name: prefix + "uploads/11111111-1111-4111-8111-111111111111", Generation: "1", Size: 1500, Updated: now.Add(-48 * time.Hour)},
				{Name: prefix + "uploads/22222222-2222-4222-8222-222222222222", Generation: "2", Size: 4000, Updated: now.Add(-25 * time.Hour)},
			},
		},
	}

	report, err := Reconcile(context.Background(), refs, store, Options{
		Bucket:           "nori-bucket",
		GraceWindow:      24 * time.Hour,
		UnattachedTTL:    24 * time.Hour,
		ExpireUnattached: true,
		ReconcileQuotas:  true,
		Delete:           true,
		Confirm:          true,
		MaxDelete:        10,
		Now:              func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if report.ExpiredUploadsCount != 1 || report.ExpiredUploadsBytes != 4000 {
		t.Fatalf("expired=%d/%d, want 1/4000", report.ExpiredUploadsCount, report.ExpiredUploadsBytes)
	}
	if report.DeletedCount != 1 || report.DeletedBytes != 4000 {
		t.Fatalf("deleted=%d/%d, want 1/4000", report.DeletedCount, report.DeletedBytes)
	}
	if report.QuotasReconciled != 1 || refs.storedQuota[alice.ID] != 1500 {
		t.Fatalf("quota=%d (reconciled=%d), want 1500 (reconciled=1)", refs.storedQuota[alice.ID], report.QuotasReconciled)
	}
}

func TestReconcileDryRunSkipsQuotaWritesAndCountsUploadsOnly(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	alice := users.User{ID: "11111111-1111-4111-8111-111111111111", Email: "alice@example.com", TimeZone: "UTC"}
	prefix := "artifacts/users/" + alice.ID + "/"

	refs := &fakeFullRefs{
		fakeRefs: &fakeRefs{
			users: []users.User{alice},
			snapshots: map[string]UserSnapshot{
				alice.ID: {
					ReferencedKeys: map[string]struct{}{
						"uploads/11111111-1111-4111-8111-111111111111":                                  {},
						"results/22222222-2222-4222-8222-222222222222":                                  {},
						"33333333-3333-4333-8333-333333333333/44444444-4444-4444-8444-444444444444.png": {},
					},
				},
			},
		},
		storedQuota: map[string]int64{alice.ID: 9999},
	}
	store := &fakeObjectStore{
		byPrefix: map[string][]Object{
			prefix: {
				{Name: prefix + "uploads/11111111-1111-4111-8111-111111111111", Generation: "1", Size: 1500, Updated: now.Add(-48 * time.Hour)},
				{Name: prefix + "results/22222222-2222-4222-8222-222222222222", Generation: "2", Size: 7000, Updated: now.Add(-48 * time.Hour)},
				{Name: prefix + "33333333-3333-4333-8333-333333333333/44444444-4444-4444-8444-444444444444.png", Generation: "3", Size: 8000, Updated: now.Add(-48 * time.Hour)},
			},
		},
	}

	// 1. Dry-run MUST NOT write quotas even when ReconcileQuotas=true!
	dryRep, err := Reconcile(context.Background(), refs, store, Options{
		Bucket:          "nori-bucket",
		ReconcileQuotas: true,
		Delete:          false,
		Now:             func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("dry-run Reconcile: %v", err)
	}
	if !dryRep.DryRun || dryRep.QuotasReconciled != 0 || refs.storedQuota[alice.ID] != 9999 {
		t.Fatalf("dry-run modified quota! report=%+v stored=%d", dryRep, refs.storedQuota[alice.ID])
	}

	// 2. Live reconcile counts ONLY uploads/ bytes (1500), ignoring results/ (7000) and run PNGs (8000).
	liveRep, err := Reconcile(context.Background(), refs, store, Options{
		Bucket:          "nori-bucket",
		ReconcileQuotas: true,
		Delete:          true,
		Confirm:         true,
		MaxDelete:       10,
		Now:             func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("live Reconcile: %v", err)
	}
	if liveRep.QuotasReconciled != 1 || refs.storedQuota[alice.ID] != 1500 {
		t.Fatalf("live quota = %d (reconciled=%d), want 1500 (uploads-only)", refs.storedQuota[alice.ID], liveRep.QuotasReconciled)
	}
}

func TestPostgresRefStoreExpireUnattachedProtectsLegacyAndRetained(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return now }
	backend := memstore.New(memstore.WithClock(nowFn))
	t.Cleanup(func() { _ = backend.Close() })

	alice := users.User{ID: "11111111-1111-4111-8111-111111111111", Email: "alice@example.com", TimeZone: "UTC"}
	dir := records.NewSharedUsers(backend)
	dir.Now = nowFn
	if err := dir.Create(ctx, alice); err != nil {
		t.Fatalf("Create alice: %v", err)
	}

	client := docstore.New(backend)
	uploadsCol := client.Collection("owners").Doc(alice.DataOwner()).Collection("uploads")
	seed := func(id string, lc state.UploadLifecycle, age time.Duration, size int64) {
		t.Helper()
		_, err := uploadsCol.Doc(id).Set(ctx, state.Upload{
			Attachment: state.Attachment{ID: id, Name: id + ".bin", MediaType: "application/octet-stream", Size: size},
			Lifecycle:  lc,
			CreatedAt:  now.Add(-age),
		})
		if err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	const (
		idLegacy   = "10000000-0000-4000-8000-000000000001"
		idRetained = "10000000-0000-4000-8000-000000000002"
		idReadyOld = "10000000-0000-4000-8000-000000000003"
		idPendOld  = "10000000-0000-4000-8000-000000000004"
		idReadyNew = "10000000-0000-4000-8000-000000000005"
	)
	seed(idLegacy, "", 72*time.Hour, 1000)
	seed(idRetained, state.UploadRetained, 72*time.Hour, 2000)
	seed(idReadyOld, state.UploadReady, 48*time.Hour, 3000)
	seed(idPendOld, state.UploadPending, 48*time.Hour, 4000)
	seed(idReadyNew, state.UploadReady, 2*time.Hour, 5000)
	if err := records.SetUserStorageBytes(ctx, backend, alice, 15000); err != nil {
		t.Fatalf("SetUserStorageBytes: %v", err)
	}

	refs := newPostgresReferenceStore(backend)
	cutoff := now.Add(-24 * time.Hour)

	// 1. Dry-run matches ONLY idReadyOld and idPendOld; never legacy "" or Retained.
	dryKeys, dryFreed, err := refs.ExpireUnattachedUploads(ctx, alice, cutoff, true)
	if err != nil {
		t.Fatalf("dry-run ExpireUnattachedUploads: %v", err)
	}
	if len(dryKeys) != 2 || dryFreed != 7000 {
		t.Fatalf("dry-run expired = (%v, %d), want 2 keys / 7000 bytes", dryKeys, dryFreed)
	}

	// 2. Live run claims via ClaimExpiredUploads and finishes ONLY idReadyOld and idPendOld.
	liveKeys, liveFreed, err := refs.ExpireUnattachedUploads(ctx, alice, cutoff, false)
	if err != nil {
		t.Fatalf("live ExpireUnattachedUploads: %v", err)
	}
	if len(liveKeys) != 2 || liveFreed != 7000 {
		t.Fatalf("live expired = (%v, %d), want 2 keys / 7000 bytes", liveKeys, liveFreed)
	}

	// Verify lifecycles in store: legacy "", Retained, and recent Ready MUST be untouched!
	for _, tc := range []struct {
		id   string
		want state.UploadLifecycle
	}{
		{idLegacy, ""},
		{idRetained, state.UploadRetained},
		{idReadyNew, state.UploadReady},
		{idReadyOld, state.UploadExpired},
		{idPendOld, state.UploadExpired},
	} {
		doc, err := uploadsCol.Doc(tc.id).Get(ctx)
		if err != nil {
			t.Fatalf("Get %s: %v", tc.id, err)
		}
		var up state.Upload
		if err := doc.DataTo(&up); err != nil {
			t.Fatalf("DataTo %s: %v", tc.id, err)
		}
		if up.Lifecycle != tc.want {
			t.Fatalf("upload %s lifecycle = %q, want %q", tc.id, up.Lifecycle, tc.want)
		}
	}
	if remaining, err := records.GetUserStorageBytes(ctx, backend, alice); err != nil || remaining != 8000 {
		t.Fatalf("remaining active_bytes = %d (err=%v), want 8000", remaining, err)
	}
}
