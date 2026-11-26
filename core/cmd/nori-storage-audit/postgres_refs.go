package main

import (
	"context"
	"errors"

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

	// Collect all result file references (and screenshot result references).
	if err := forEachDoc(root.Collection("results").Documents(ctx), func(doc *docstore.DocumentSnapshot) error {
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
