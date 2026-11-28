package records

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"nori/internal/billing"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/users"
)

// ErrStorageQuotaExceeded is returned when adding an upload would exceed the
// user's per-tier active GCS storage cap. Existing data stays readable.
var ErrStorageQuotaExceeded = errors.New("storage quota exceeded")

func (d *Database) uploads() *docstore.CollectionRef { return d.root().Collection("uploads") }

func (d *Database) storageMetaDoc() *docstore.DocumentRef {
	return d.root().Collection("meta").Doc("user_storage")
}

func unwrapPool(b docstore.Backend) *pgxpool.Pool {
	for b != nil {
		if pp, ok := b.(interface{ Pool() *pgxpool.Pool }); ok {
			return pp.Pool()
		}
		uw, ok := b.(docstore.BackendUnwrapper)
		if !ok {
			return nil
		}
		b = uw.Unwrap()
	}
	return nil
}

func unwrapBillingStore(b docstore.Backend) billing.Store {
	for b != nil {
		if bs, ok := b.(billing.Store); ok {
			return bs
		}
		if pp, ok := b.(interface{ Pool() *pgxpool.Pool }); ok && pp.Pool() != nil {
			return billing.NewPostgresStore(pp.Pool())
		}
		uw, ok := b.(docstore.BackendUnwrapper)
		if !ok {
			return nil
		}
		b = uw.Unwrap()
	}
	return nil
}

// ReserveUserStorageBytesPG atomically checks and increments nori_user_storage.active_bytes
// for userID under a row lock. When capBytes > 0 and active_bytes + sizeBytes > capBytes,
// it rejects with ErrStorageQuotaExceeded without modifying active_bytes.
func ReserveUserStorageBytesPG(ctx context.Context, pool *pgxpool.Pool, userID string, sizeBytes, capBytes int64) (int64, error) {
	if pool == nil || userID == "" || sizeBytes <= 0 {
		return 0, nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO nori_user_storage (user_id, active_bytes, updated_at)
		VALUES ($1, 0, now())
		ON CONFLICT (user_id) DO NOTHING`, userID); err != nil {
		return 0, err
	}
	var active int64
	if err := tx.QueryRow(ctx, `SELECT active_bytes FROM nori_user_storage WHERE user_id = $1 FOR UPDATE`, userID).Scan(&active); err != nil {
		return 0, err
	}
	if capBytes > 0 && active+sizeBytes > capBytes {
		return active, ErrStorageQuotaExceeded
	}
	var updated int64
	if err := tx.QueryRow(ctx, `
		UPDATE nori_user_storage
		SET active_bytes = active_bytes + $2, updated_at = now()
		WHERE user_id = $1
		RETURNING active_bytes`, userID, sizeBytes).Scan(&updated); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return updated, nil
}

// ReleaseUserStorageBytesPG atomically decrements nori_user_storage.active_bytes for userID (clamped at 0).
func ReleaseUserStorageBytesPG(ctx context.Context, pool *pgxpool.Pool, userID string, deltaBytes int64) error {
	if pool == nil || userID == "" || deltaBytes <= 0 {
		return nil
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO nori_user_storage (user_id, active_bytes, updated_at)
		VALUES ($1, 0, now())
		ON CONFLICT (user_id) DO UPDATE
		SET active_bytes = GREATEST(nori_user_storage.active_bytes - $2, 0),
		    updated_at = now()`, userID, deltaBytes)
	return err
}

// GetUserStorageBytesPG returns active_bytes from nori_user_storage (0 if no row exists).
func GetUserStorageBytesPG(ctx context.Context, pool *pgxpool.Pool, userID string) (int64, error) {
	if pool == nil || userID == "" {
		return 0, nil
	}
	var active int64
	err := pool.QueryRow(ctx, `SELECT active_bytes FROM nori_user_storage WHERE user_id = $1`, userID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return active, err
}

// SetUserStorageBytesPG overwrites active_bytes in nori_user_storage (used by reconciliation and reset).
func SetUserStorageBytesPG(ctx context.Context, pool *pgxpool.Pool, userID string, activeBytes int64) error {
	if pool == nil || userID == "" {
		return nil
	}
	if activeBytes < 0 {
		activeBytes = 0
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO nori_user_storage (user_id, active_bytes, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (user_id) DO UPDATE
		SET active_bytes = EXCLUDED.active_bytes,
		    updated_at = now()`, userID, activeBytes)
	return err
}

type memStorageDoc struct {
	ActiveBytes int64     `firestore:"active_bytes" json:"active_bytes"`
	UpdatedAt   time.Time `firestore:"updated_at" json:"updated_at"`
}

func storageDocRef(backend docstore.Backend, user users.User) *docstore.DocumentRef {
	return docstore.New(unclosedBackend{backend}).Collection("owners").Doc(user.DataOwner()).Collection("meta").Doc("user_storage")
}

// GetUserStorageBytes returns a user's active storage bytes (Postgres nori_user_storage or docstore fallback).
func GetUserStorageBytes(ctx context.Context, backend docstore.Backend, user users.User) (int64, error) {
	if pool := unwrapPool(backend); pool != nil {
		return GetUserStorageBytesPG(ctx, pool, user.ID)
	}
	if backend == nil || !users.ValidID(user.ID) {
		return 0, nil
	}
	doc, err := storageDocRef(backend, user).Get(ctx)
	if notFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var rec memStorageDoc
	if err := doc.DataTo(&rec); err != nil {
		return 0, err
	}
	if rec.ActiveBytes < 0 {
		return 0, nil
	}
	return rec.ActiveBytes, nil
}

// ReleaseUserStorageBytes decrements a user's active storage bytes (clamped at 0).
func ReleaseUserStorageBytes(ctx context.Context, backend docstore.Backend, user users.User, delta int64) error {
	if delta <= 0 {
		return nil
	}
	if pool := unwrapPool(backend); pool != nil {
		return ReleaseUserStorageBytesPG(ctx, pool, user.ID, delta)
	}
	if backend == nil || !users.ValidID(user.ID) {
		return nil
	}
	client := docstore.New(unclosedBackend{backend})
	ref := client.Collection("owners").Doc(user.DataOwner()).Collection("meta").Doc("user_storage")
	return client.RunTransaction(ctx, func(_ context.Context, tx *docstore.Transaction) error {
		var rec memStorageDoc
		if snap, err := tx.Get(ref); err == nil {
			_ = snap.DataTo(&rec)
		} else if !notFound(err) {
			return err
		}
		rec.ActiveBytes -= delta
		if rec.ActiveBytes < 0 {
			rec.ActiveBytes = 0
		}
		rec.UpdatedAt = time.Now().UTC()
		return tx.Set(ref, rec)
	})
}

// SetUserStorageBytes sets a user's active storage bytes directly.
func SetUserStorageBytes(ctx context.Context, backend docstore.Backend, user users.User, activeBytes int64) error {
	if activeBytes < 0 {
		activeBytes = 0
	}
	if pool := unwrapPool(backend); pool != nil {
		return SetUserStorageBytesPG(ctx, pool, user.ID, activeBytes)
	}
	if backend == nil || !users.ValidID(user.ID) {
		return nil
	}
	_, err := storageDocRef(backend, user).Set(ctx, memStorageDoc{
		ActiveBytes: activeBytes,
		UpdatedAt:   time.Now().UTC(),
	})
	return err
}

func (d *Database) storageCapBytes(ctx context.Context) (int64, error) {
	bs := unwrapBillingStore(d.db.Backend)
	if bs == nil {
		return 0, nil
	}
	ent, err := bs.GetEntitlement(ctx, d.user.ID)
	if errors.Is(err, billing.ErrEntitlementMissing) {
		if d.EnforceBilling {
			return 0, billing.ErrEntitlementMissing
		}
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if err := ent.CheckAccess(d.now()); err != nil {
		return 0, err
	}
	return ent.EffectivePolicy().MaxStorageBytes, nil
}

func (d *Database) CreateUpload(ctx context.Context, upload state.Upload) (state.Upload, error) {
	capBytes, err := d.storageCapBytes(ctx)
	if err != nil {
		return state.Upload{}, err
	}

	pool := unwrapPool(d.db.Backend)
	saved := upload
	createdNew := false

	err = d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		createdNew = false
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
		if pool == nil && upload.Size > 0 {
			var rec memStorageDoc
			if snap, sErr := tx.Get(d.storageMetaDoc()); sErr == nil {
				_ = snap.DataTo(&rec)
			} else if !notFound(sErr) {
				return sErr
			}
			if capBytes > 0 && rec.ActiveBytes+upload.Size > capBytes {
				return ErrStorageQuotaExceeded
			}
			rec.ActiveBytes += upload.Size
			rec.UpdatedAt = d.now()
			if err := tx.Set(d.storageMetaDoc(), rec); err != nil {
				return err
			}
		}
		saved = upload
		createdNew = true
		return tx.Create(d.uploads().Doc(upload.ID), upload)
	})
	if err != nil {
		return state.Upload{}, err
	}

	if pool != nil && createdNew && upload.Size > 0 {
		if _, err := ReserveUserStorageBytesPG(ctx, pool, d.user.ID, upload.Size, capBytes); err != nil {
			// Roll back the upload metadata row if storage reservation was denied.
			_ = d.db.RunTransaction(context.WithoutCancel(ctx), func(_ context.Context, tx *docstore.Transaction) error {
				return tx.Delete(d.uploads().Doc(upload.ID))
			})
			return state.Upload{}, err
		}
	}
	return saved, nil
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
	pool := unwrapPool(d.db.Backend)
	var freedBytes int64
	err := d.db.RunTransaction(ctx, func(_ context.Context, tx *docstore.Transaction) error {
		freedBytes = 0
		ref := d.uploads().Doc(id)
		doc, err := tx.Get(ref)
		if notFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		var existing state.Upload
		if err := doc.DataTo(&existing); err != nil {
			return err
		}
		// Never overwrite Retained, legacy "", Ready, or already Expired uploads.
		if existing.Lifecycle != state.UploadExpiring && existing.Lifecycle != state.UploadPending {
			return nil
		}
		if existing.Size > 0 {
			freedBytes = existing.Size
		}
		if err := tx.Set(ref, map[string]any{"id": id, "lifecycle": state.UploadExpired}); err != nil {
			return err
		}
		if pool == nil && freedBytes > 0 {
			var rec memStorageDoc
			if snap, sErr := tx.Get(d.storageMetaDoc()); sErr == nil {
				_ = snap.DataTo(&rec)
			} else if !notFound(sErr) {
				return sErr
			}
			rec.ActiveBytes -= freedBytes
			if rec.ActiveBytes < 0 {
				rec.ActiveBytes = 0
			}
			rec.UpdatedAt = d.now()
			return tx.Set(d.storageMetaDoc(), rec)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if pool != nil && freedBytes > 0 {
		return ReleaseUserStorageBytesPG(ctx, pool, d.user.ID, freedBytes)
	}
	return nil
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
