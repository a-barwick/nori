package app

import (
	"context"
	"fmt"
	"time"

	"nori/internal/api"
	"nori/internal/state/docstore"
	"nori/internal/users"
)

type userActivityKey struct{}

func (s *Service) userDataActivity(ctx context.Context, row users.User) (context.Context, users.User, func(), error) {
	if isAccountDeleteRequest(ctx) {
		return ctx, row, func() {}, nil
	}
	if current, ok := ctx.Value(userActivityKey{}).(users.User); ok && current.ID == row.ID {
		return ctx, current, func() {}, nil
	}
	store, ok := s.Backend().(docstore.UserDataResetter)
	if !ok {
		return ctx, row, func() {}, nil
	}
	release, err := store.LockUserData(ctx, row.ID, false)
	if err != nil {
		return ctx, row, nil, err
	}
	current, err := s.Users.Get(ctx, row.ID)
	if err != nil {
		release()
		return ctx, row, nil, err
	}
	if pending, pErr := s.isAccountDeletePending(ctx, current.ID); pErr != nil {
		release()
		return ctx, row, nil, pErr
	} else if pending {
		ctx = withAccountDeletePending(ctx)
	}
	return context.WithValue(ctx, userActivityKey{}, current), current, release, nil
}

func (s *Service) resetUserData(ctx context.Context, row users.User, resumePending bool) error {
	store, ok := s.Backend().(docstore.UserDataResetter)
	if !ok {
		return fmt.Errorf("user data reset requires a reset-capable store")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	release, err := store.LockUserData(ctx, row.ID, true)
	if err != nil {
		return err
	}
	defer release()
	row, err = s.Users.Get(ctx, row.ID)
	if err != nil {
		return err
	}
	if pending, pErr := s.isAccountDeletePending(ctx, row.ID); pErr != nil {
		return pErr
	} else if pending {
		return api.ErrAccountDeletePending
	}
	if resumePending && !row.ResetPending {
		return docstore.ErrUserDataResetNotPending
	}
	if err := store.BeginUserDataReset(ctx, row.ID); err != nil {
		return err
	}
	if err := s.storage.PurgeUserArtifacts(ctx, s.cfg, row); err != nil {
		return fmt.Errorf("%w: purge user artifacts: %w", docstore.ErrUserDataResetPending, err)
	}
	if err := store.FinishUserDataReset(users.WithID(ctx, row.DataOwner()), row.ID, row.DataOwner()); err != nil {
		return fmt.Errorf("%w: clear account records: %w", docstore.ErrUserDataResetPending, err)
	}
	_ = s.storage.SetUserStorageBytes(ctx, row, 0)
	s.storage.EvictUserArtifactKey(row)
	s.storage.EvictUserDEKs(row.ID)
	release()
	store.PublishUserDataReset(ctx, row.DataOwner())
	return nil
}
