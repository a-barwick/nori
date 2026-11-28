package records_test

import (
	"context"
	"testing"
	"time"

	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/users"
)

func TestFinishExpiredUploadRefusesToOverwriteRetainedOrLegacy(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return now }
	backend := memstore.New(memstore.WithClock(nowFn))
	t.Cleanup(func() { _ = backend.Close() })

	u := users.User{ID: "44444444-4444-4444-8444-444444444401", Email: "upload-guard@example.com", TimeZone: "UTC"}
	dir := records.NewSharedUsers(backend)
	dir.Now = nowFn
	if err := dir.Create(ctx, u); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	db := records.NewShared(backend, u, 5.0)
	db.Now = nowFn
	client := docstore.New(backend)
	uploadsCol := client.Collection("owners").Doc(u.DataOwner()).Collection("uploads")

	seedUpload := func(id string, lc state.UploadLifecycle, size int64) {
		t.Helper()
		up := state.Upload{
			Attachment: state.Attachment{ID: id, Name: id + ".txt", MediaType: "text/plain", Size: size},
			Lifecycle:  lc,
			CreatedAt:  now.Add(-48 * time.Hour),
		}
		if _, err := uploadsCol.Doc(id).Set(ctx, up); err != nil {
			t.Fatalf("seed upload %s: %v", id, err)
		}
	}

	const (
		idLegacy   = "11111111-1111-4111-8111-111111111111"
		idRetained = "22222222-2222-4222-8222-222222222222"
		idReady    = "33333333-3333-4333-8333-333333333333"
		idExpiring = "44444444-4444-4444-8444-444444444444"
		idPending  = "55555555-5555-4555-8555-555555555555"
	)
	seedUpload(idLegacy, "", 1000)
	seedUpload(idRetained, state.UploadRetained, 2000)
	seedUpload(idReady, state.UploadReady, 3000)
	seedUpload(idExpiring, state.UploadExpiring, 4000)
	seedUpload(idPending, state.UploadPending, 5000)
	if err := records.SetUserStorageBytes(ctx, backend, u, 15000); err != nil {
		t.Fatalf("SetUserStorageBytes: %v", err)
	}

	// FinishExpiredUpload MUST be a safe no-op for legacy "", Retained, and Ready uploads.
	for _, tc := range []struct {
		id   string
		want state.UploadLifecycle
	}{
		{idLegacy, ""},
		{idRetained, state.UploadRetained},
		{idReady, state.UploadReady},
	} {
		if err := db.FinishExpiredUpload(ctx, tc.id); err != nil {
			t.Fatalf("FinishExpiredUpload(%s): %v", tc.id, err)
		}
		doc, err := uploadsCol.Doc(tc.id).Get(ctx)
		if err != nil {
			t.Fatalf("Get(%s): %v", tc.id, err)
		}
		var got state.Upload
		if err := doc.DataTo(&got); err != nil {
			t.Fatalf("DataTo(%s): %v", tc.id, err)
		}
		if got.Lifecycle != tc.want {
			t.Fatalf("upload %s lifecycle = %q, want preserved %q", tc.id, got.Lifecycle, tc.want)
		}
	}
	if bytesAfterProtected, err := records.GetUserStorageBytes(ctx, backend, u); err != nil || bytesAfterProtected != 15000 {
		t.Fatalf("active_bytes after no-op FinishExpiredUpload = %d (err=%v), want 15000", bytesAfterProtected, err)
	}

	// FinishExpiredUpload MUST expire Expiring and Pending uploads and release their bytes once.
	for _, id := range []string{idExpiring, idPending} {
		if err := db.FinishExpiredUpload(ctx, id); err != nil {
			t.Fatalf("FinishExpiredUpload(%s): %v", id, err)
		}
		doc, err := uploadsCol.Doc(id).Get(ctx)
		if err != nil {
			t.Fatalf("Get(%s): %v", id, err)
		}
		var got state.Upload
		if err := doc.DataTo(&got); err != nil {
			t.Fatalf("DataTo(%s): %v", id, err)
		}
		if got.Lifecycle != state.UploadExpired {
			t.Fatalf("upload %s lifecycle = %q, want expired", id, got.Lifecycle)
		}
	}
	// Repeating FinishExpiredUpload on an already-expired upload must not double-decrement.
	if err := db.FinishExpiredUpload(ctx, idExpiring); err != nil {
		t.Fatalf("repeat FinishExpiredUpload: %v", err)
	}
	if bytesAfterExpired, err := records.GetUserStorageBytes(ctx, backend, u); err != nil || bytesAfterExpired != 6000 {
		t.Fatalf("active_bytes after expiring 4000+5000 = %d (err=%v), want 6000", bytesAfterExpired, err)
	}
}
