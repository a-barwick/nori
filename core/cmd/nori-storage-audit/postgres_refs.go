package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/users"
)

type postgresReferenceStore struct {
	backend docstore.Backend
	users   *records.Users
}

func newPostgresReferenceStore(backend docstore.Backend) *postgresReferenceStore {
	return &postgresReferenceStore{
		backend: backend,
		users:   records.NewSharedUsers(backend),
	}
}

func (p *postgresReferenceStore) ListUsers(ctx context.Context) ([]users.User, error) {
	return p.users.List(ctx)
}

func (p *postgresReferenceStore) InspectUser(ctx context.Context, user users.User) (UserSnapshot, func(), error) {
	snap := UserSnapshot{
		User:           user,
		ReferencedKeys: make(map[string]struct{}),
	}
	if user.ResetPending {
		snap.Locked = true
		return snap, nil, nil
	}

	var release func()
	if resetter, ok := p.backend.(docstore.UserDataResetter); ok {
		rel, err := resetter.LockUserData(ctx, user.ID, false)
		if errors.Is(err, docstore.ErrUserDataBusy) {
			snap.Locked = true
			return snap, nil, nil
		}
		if err != nil {
			return snap, nil, err
		}
		release = rel
	}

	// Re-check user record under lock so we never race with a reset that just started.
	freshUser, err := p.users.Get(ctx, user.ID)
	if err != nil {
		if release != nil {
			release()
		}
		return snap, nil, err
	}
	if freshUser.ResetPending {
		if release != nil {
			release()
		}
		snap.Locked = true
		return snap, nil, nil
	}
	type accountDeleteChecker interface {
		IsAccountDeletePending(ctx context.Context, userID string) (bool, error)
	}
	if checker, ok := p.backend.(accountDeleteChecker); ok {
		pending, err := checker.IsAccountDeletePending(ctx, user.ID)
		if err != nil {
			if release != nil {
				release()
			}
			return snap, nil, err
		}
		if pending {
			if release != nil {
				release()
			}
			snap.Locked = true
			return snap, nil, nil
		}
	}

	client := docstore.New(p.backend)
	root := client.Collection("owners").Doc(user.DataOwner())

	// Collect all non-expired upload references.
	if err := forEachDoc(root.Collection("uploads").Documents(ctx), func(doc *docstore.DocumentSnapshot) error {
		var upload state.Upload
		if err := doc.DataTo(&upload); err != nil {
			return err
		}
		if upload.Lifecycle != state.UploadExpired {
			snap.ReferencedKeys[state.UploadKey(doc.Ref.ID)] = struct{}{}
		}
		return nil
	}); err != nil {
		if release != nil {
			release()
		}
		return snap, nil, err
	}

	// Collect deleted library result IDs so purged/deleted library items are not treated as active references.
	deletedResults := map[string]struct{}{}
	if err := forEachDoc(root.Collection("resources").Where("collection", "==", "library_overlay").Documents(ctx), func(doc *docstore.DocumentSnapshot) error {
		var res state.Resource
		if err := doc.DataTo(&res); err != nil {
			return nil
		}
		var overlay struct {
			DeletedAt *time.Time `json:"deleted_at"`
		}
		if json.Unmarshal(res.Body, &overlay) == nil && overlay.DeletedAt != nil && res.ID != "" {
			deletedResults[res.ID] = struct{}{}
		}
		return nil
	}); err != nil {
		if release != nil {
			release()
		}
		return snap, nil, err
	}

	// Collect all non-deleted result file references (and screenshot result references).
	if err := forEachDoc(root.Collection("results").Documents(ctx), func(doc *docstore.DocumentSnapshot) error {
		if _, deleted := deletedResults[doc.Ref.ID]; deleted {
			return nil
		}
		var res state.Result
		if err := doc.DataTo(&res); err != nil {
			return err
		}
		snap.ReferencedKeys[state.ResultKey(doc.Ref.ID)] = struct{}{}
		if res.Kind == state.ResultScreenshot && res.RunID != "" && res.ArtifactID != "" {
			snap.ReferencedKeys[res.RunID+"/"+res.ArtifactID+".png"] = struct{}{}
		}
		return nil
	}); err != nil {
		if release != nil {
			release()
		}
		return snap, nil, err
	}

	// Collect all run PNG artifact references.
	if err := forEachDoc(root.Collection("runs").Documents(ctx), func(doc *docstore.DocumentSnapshot) error {
		var run state.Run
		if err := doc.DataTo(&run); err != nil {
			return err
		}
		runID := run.ID
		if runID == "" {
			runID = doc.Ref.ID
		}
		for _, a := range run.Artifacts {
			if a.ID != "" {
				snap.ReferencedKeys[runID+"/"+a.ID+".png"] = struct{}{}
			}
		}
		return nil
	}); err != nil {
		if release != nil {
			release()
		}
		return snap, nil, err
	}

	return snap, release, nil
}

// ExpireUnattachedUploads finds pending/ready uploads older than cutoff (never touching retained
// uploads attached to messages/tasks). When dryRun is false, marks them expired and decrements
// nori_user_storage via FinishExpiredUpload.
func (p *postgresReferenceStore) ExpireUnattachedUploads(ctx context.Context, user users.User, cutoff time.Time, dryRun bool) ([]string, int64, error) {
	client := docstore.New(p.backend)
	root := client.Collection("owners").Doc(user.DataOwner())
	var expiredIDs []string
	var expiredKeys []string
	var freedBytes int64

	if err := forEachDoc(root.Collection("uploads").Documents(ctx), func(doc *docstore.DocumentSnapshot) error {
		var upload state.Upload
		if err := doc.DataTo(&upload); err != nil {
			return err
		}
		switch upload.Lifecycle {
		case state.UploadRetained, state.UploadExpired:
			return nil
		}
		if upload.CreatedAt.IsZero() || !upload.CreatedAt.Before(cutoff) {
			return nil
		}
		expiredIDs = append(expiredIDs, doc.Ref.ID)
		expiredKeys = append(expiredKeys, state.UploadKey(doc.Ref.ID))
		if upload.Size > 0 {
			freedBytes += upload.Size
		}
		return nil
	}); err != nil {
		return nil, 0, err
	}

	if !dryRun && len(expiredIDs) > 0 {
		db := records.NewShared(p.backend, user, 1)
		for _, id := range expiredIDs {
			if err := db.FinishExpiredUpload(ctx, id); err != nil {
				return expiredKeys, freedBytes, err
			}
		}
	}
	return expiredKeys, freedBytes, nil
}

// ReconcileUserStorageBytes updates nori_user_storage.active_bytes to actualActiveBytes
// and returns the previously recorded active_bytes value.
func (p *postgresReferenceStore) ReconcileUserStorageBytes(ctx context.Context, user users.User, actualActiveBytes int64) (int64, error) {
	prev, err := records.GetUserStorageBytes(ctx, p.backend, user)
	if err != nil {
		return 0, err
	}
	if err := records.SetUserStorageBytes(ctx, p.backend, user, actualActiveBytes); err != nil {
		return prev, err
	}
	return prev, nil
}

func forEachDoc(iter *docstore.DocumentIterator, fn func(*docstore.DocumentSnapshot) error) error {
	defer iter.Stop()
	for {
		doc, err := iter.Next()
		if errors.Is(err, docstore.Done) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(doc); err != nil {
			return err
		}
	}
}
