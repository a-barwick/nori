package platform

import (
	"context"
	"errors"

	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/users"
)

// PurgeUserArtifacts removes stored uploads, results, and screenshots for a user.
func (p *Storage) PurgeUserArtifacts(ctx context.Context, cfg settings.Settings, user users.User) error {
	if err := user.Validate(); err != nil {
		return err
	}
	var store state.ArtifactStore
	var err error
	if p.artifacts != nil {
		store = p.artifacts(user)
	} else {
		store, err = openLocalArtifacts(cfg, user)
		if err == nil && store == nil {
			store, err = p.openObjects(ctx, cfg, user)
		}
	}
	if err != nil {
		return err
	}
	if store == nil {
		return errors.New("artifact store is unavailable")
	}
	defer store.Close()
	if user.ID == users.LegacyID {
		if legacy, ok := store.(interface{ PurgeLegacy(context.Context) error }); ok {
			return legacy.PurgeLegacy(ctx)
		}
	}
	purger, ok := store.(interface{ Purge(context.Context) error })
	if !ok {
		return errors.New("artifact store does not support reset")
	}
	return purger.Purge(ctx)
}
