package records

import (
	"context"

	"nori/internal/state"
	"nori/internal/state/docstore"
)

func (d *Database) uploads() *docstore.CollectionRef { return d.root().Collection("uploads") }

// CreateUpload saves the record once. A repeat of the same upload returns the saved record, and a different file under
// the same ID is refused.
func (d *Database) CreateUpload(ctx context.Context, upload state.Upload) (state.Upload, error) {
	saved := upload
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		doc, err := tx.Get(d.uploads().Doc(upload.ID))
		if err == nil {
			if err = doc.DataTo(&saved); err != nil {
				return err
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
	return upload, doc.DataTo(&upload)
}

// readAttachments completes its reads before the transaction writes.
func (d *Database) readAttachments(tx *docstore.Transaction, ids []string) ([]state.Attachment, error) {
	attachments := make([]state.Attachment, 0, len(ids))
	for _, id := range ids {
		doc, err := tx.Get(d.uploads().Doc(id))
		if notFound(err) {
			return nil, state.ErrAttachmentMissing
		}
		if err != nil {
			return nil, err
		}
		var upload state.Upload
		if err = doc.DataTo(&upload); err != nil {
			return nil, err
		}
		attachments = append(attachments, upload.Attachment)
	}
	return attachments, nil
}
