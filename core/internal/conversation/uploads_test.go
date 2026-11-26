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
