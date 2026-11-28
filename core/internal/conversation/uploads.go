package conversation

import (
	"context"
	"errors"
	"time"

	"nori/internal/state"
)

func (t *Tasks) SaveUpload(ctx context.Context, id, name, declared string, data []byte) (state.Upload, error) {
	upload, err := state.PrepareUpload(id, name, declared, data, t.now())
	if err != nil {
		return state.Upload{}, err
	}
	upload, err = t.Store.CreateUpload(ctx, upload)
	if err != nil {
		return state.Upload{}, err
	}
	if err := state.PutUploadBytes(ctx, t.Store, upload, data); err != nil {
		return state.Upload{}, err
	}
	completed, err := t.Store.CompleteUpload(ctx, id)
	if errors.Is(err, state.ErrNotFound) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		err = errors.Join(err, t.Store.DeleteFile(cleanup, state.UploadKey(id)))
	}
	return completed, err
}

// UnattachedUploadTTL is the retention window before unattached uploads expire
// and have their underlying storage bytes reclaimed.
const UnattachedUploadTTL = 24 * time.Hour

func (t *Tasks) CleanupUploads(ctx context.Context) error {
	now := t.now()
	cutoff := now.Add(-UnattachedUploadTTL)
	ids, err := t.Store.ClaimExpiredUploads(ctx, cutoff, cutoff, 50)
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if err := t.Store.DeleteFile(ctx, state.UploadKey(id)); err != nil {
			failures = append(failures, err)
			continue
		}
		if err := t.Store.FinishExpiredUpload(ctx, id); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
