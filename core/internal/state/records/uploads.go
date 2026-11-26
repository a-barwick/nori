package records

import (
	"context"
	"time"

	"nori/internal/state"
	"nori/internal/state/docstore"
)

func (d *Database) uploads() *docstore.CollectionRef { return d.root().Collection("uploads") }

func (d *Database) CreateUpload(ctx context.Context, upload state.Upload) (state.Upload, error) {
	saved := upload
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		doc, err := tx.Get(d.uploads().Doc(upload.ID))
		if err == nil {
			if err = doc.DataTo(&saved); err != nil {
				return err
			}
			if saved.Lifecycle == state.UploadExpiring || saved.Lifecycle == state.UploadExpired {
				return state.ErrNotFound
			}
			if saved.SHA256 != upload.SHA256 {
				return state.ErrDuplicate
			}
			return nil
		}
		if !notFound(err) {
			return err
		}
		saved = upload
		return tx.Create(d.uploads().Doc(upload.ID), upload)
	})
	return saved, err
}

func (d *Database) GetUpload(ctx context.Context, id string) (state.Upload, error) {
	doc, err := d.uploads().Doc(id).Get(ctx)
	if notFound(err) {
		return state.Upload{}, state.ErrNotFound
	}
	if err != nil {
		return state.Upload{}, err
	}
	var upload state.Upload
	if err := doc.DataTo(&upload); err != nil {
		return state.Upload{}, err
	}
	if !uploadAvailable(upload) {
		return state.Upload{}, state.ErrNotFound
	}
	return upload, nil
}

func uploadAvailable(upload state.Upload) bool {
	return upload.Lifecycle == "" || upload.Lifecycle == state.UploadReady || upload.Lifecycle == state.UploadRetained
}

func (d *Database) CompleteUpload(ctx context.Context, id string) (state.Upload, error) {
	var upload state.Upload
	err := d.db.RunTransaction(ctx, func(_ context.Context, tx *docstore.Transaction) error {
		ref := d.uploads().Doc(id)
		doc, err := tx.Get(ref)
		if notFound(err) {
			return state.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := doc.DataTo(&upload); err != nil {
			return err
		}
		if uploadAvailable(upload) {
			return nil
		}
		if upload.Lifecycle != state.UploadPending {
			return state.ErrNotFound
		}
		upload.Lifecycle = state.UploadReady
		return tx.Update(ref, []docstore.Update{{Path: "lifecycle", Value: upload.Lifecycle}})
	})
	return upload, err
}

func (d *Database) ClaimExpiredUploads(ctx context.Context, pendingBefore, readyBefore time.Time, limit int) ([]string, error) {
	var ids []string
	err := d.db.RunTransaction(ctx, func(_ context.Context, tx *docstore.Transaction) error {
		ids = nil
		for _, spec := range []struct {
			lifecycle state.UploadLifecycle
			before    time.Time
		}{
			{state.UploadExpiring, time.Time{}},
			{state.UploadPending, pendingBefore},
			{state.UploadReady, readyBefore},
		} {
			query := d.uploads().Where("lifecycle", "==", spec.lifecycle).Limit(limit - len(ids))
			if !spec.before.IsZero() {
				query = query.Where("created_at", "<", spec.before).OrderBy("created_at", docstore.Asc)
			}
			docs, err := tx.Documents(query).GetAll()
			if err != nil {
				return err
			}
			for _, doc := range docs {
				ids = append(ids, doc.Ref.ID)
			}
			if len(ids) >= limit {
				break
			}
		}
		for _, id := range ids {
			if err := tx.Update(d.uploads().Doc(id), []docstore.Update{{Path: "lifecycle", Value: state.UploadExpiring}}); err != nil {
				return err
			}
		}
		return nil
	})
	return ids, err
}

func (d *Database) FinishExpiredUpload(ctx context.Context, id string) error {
	_, err := d.uploads().Doc(id).Set(ctx, map[string]any{"id": id, "lifecycle": state.UploadExpired})
	return err
}

func (d *Database) readAttachments(tx *docstore.Transaction, ids []string) ([]state.Attachment, []string, error) {
	attachments := make([]state.Attachment, 0, len(ids))
	var retain []string
	for _, id := range ids {
		doc, err := tx.Get(d.uploads().Doc(id))
		if notFound(err) {
			return nil, nil, state.ErrAttachmentMissing
		}
		if err != nil {
			return nil, nil, err
		}
		var upload state.Upload
		if err = doc.DataTo(&upload); err != nil {
			return nil, nil, err
		}
		if !uploadAvailable(upload) {
			return nil, nil, state.ErrAttachmentMissing
		}
		if upload.Lifecycle == state.UploadReady {
			retain = append(retain, id)
		}
		attachments = append(attachments, upload.Attachment)
	}
	return attachments, retain, nil
}
