package conversation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

type failUploadCompletion struct {
	state.ConversationStore
	fail bool
}

func (s *failUploadCompletion) CompleteUpload(ctx context.Context, id string) (state.Upload, error) {
	if s.fail {
		s.fail = false
		return state.Upload{}, errors.New("metadata commit failed")
	}
	return s.ConversationStore.CompleteUpload(ctx, id)
}

func TestUploadRetryCleanupAndRetention(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := &failUploadCompletion{ConversationStore: state.Compose(records.NewShared(memstore.New(), users.User{ID: uuid.NewString()}, 1), memstore.NewArtifacts()), fail: true}
	tasks := &Tasks{Store: store, Now: func() time.Time { return now }}
	payload := []byte("fixture")
	attached := uuid.NewString()
	if _, err := tasks.SaveUpload(ctx, attached, "test.txt", "text/plain", payload); err == nil {
		t.Fatal("completion fault was not reported")
	}
	if _, err := store.GetUpload(ctx, attached); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("pending upload is visible: %v", err)
	}
	if _, err := tasks.SaveUpload(ctx, attached, "test.txt", "text/plain", payload); err != nil {
		t.Fatalf("same-id retry failed: %v", err)
	}
	if err := store.AcceptConversationInput(ctx, uuid.NewString(), "keep this", []string{attached}); err != nil {
		t.Fatal(err)
	}

	unattached := uuid.NewString()
	if _, err := tasks.SaveUpload(ctx, unattached, "unused.txt", "text/plain", payload); err != nil {
		t.Fatal(err)
	}
	now = now.Add(8 * 24 * time.Hour)
	if err := tasks.CleanupUploads(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetFile(ctx, state.UploadKey(unattached)); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("unattached bytes survived retention: %v", err)
	}
	if _, err := store.GetFile(ctx, state.UploadKey(attached)); err != nil {
		t.Fatalf("attachment was lost: %v", err)
	}
}

func TestUnattachedUpload24hExpiryAndStorageQuota(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	backend := memstore.New(memstore.WithClock(func() time.Time { return now }))
	t.Cleanup(func() { _ = backend.Close() })

	dir := records.NewSharedUsers(backend)
	u := users.User{ID: uuid.NewString(), Email: "quota@example.com", DisplayName: "Quota", TimeZone: "UTC"}
	if err := dir.Create(ctx, u); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	db := records.NewShared(backend, u, 1)
	db.Now = func() time.Time { return now }
	db.EnforceBilling = true
	store := state.Compose(db, memstore.NewArtifacts())
	tasks := &Tasks{Store: store, Now: func() time.Time { return now }}

	// Save an unattached upload; active bytes should increment.
	payload := []byte("hello storage quota")
	unattachedID := uuid.NewString()
	if _, err := tasks.SaveUpload(ctx, unattachedID, "notes.txt", "text/plain", payload); err != nil {
		t.Fatalf("SaveUpload: %v", err)
	}
	used, err := records.GetUserStorageBytes(ctx, backend, u)
	if err != nil || used != int64(len(payload)) {
		t.Fatalf("used = %d (err=%v), want %d", used, err, len(payload))
	}

	// Simulate user near trial 100 MiB cap: next upload exceeding cap must fail with ErrStorageQuotaExceeded.
	if err := records.SetUserStorageBytes(ctx, backend, u, (100<<20)-5); err != nil {
		t.Fatalf("SetUserStorageBytes: %v", err)
	}
	if _, err := tasks.SaveUpload(ctx, uuid.NewString(), "over.txt", "text/plain", payload); !errors.Is(err, records.ErrStorageQuotaExceeded) {
		t.Fatalf("expected ErrStorageQuotaExceeded, got %v", err)
	}
	// Existing data remains readable even when at/over cap.
	if _, err := store.GetFile(ctx, state.UploadKey(unattachedID)); err != nil {
		t.Fatalf("existing upload must remain readable when over cap: %v", err)
	}

	// Reset used bytes to actual upload size and advance 25h (>24h unattached TTL).
	if err := records.SetUserStorageBytes(ctx, backend, u, int64(len(payload))); err != nil {
		t.Fatalf("SetUserStorageBytes: %v", err)
	}
	now = now.Add(25 * time.Hour)
	if err := tasks.CleanupUploads(ctx); err != nil {
		t.Fatalf("CleanupUploads at 25h: %v", err)
	}
	if _, err := store.GetFile(ctx, state.UploadKey(unattachedID)); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("expected unattached upload expired at 25h, got err=%v", err)
	}
	usedAfter, err := records.GetUserStorageBytes(ctx, backend, u)
	if err != nil || usedAfter != 0 {
		t.Fatalf("usedAfter expiry = %d (err=%v), want 0", usedAfter, err)
	}
}
