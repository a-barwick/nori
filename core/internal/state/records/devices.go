package records

import (
	"context"
	"crypto/subtle"
	"time"

	"nori/internal/clientauth"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

type Devices struct{ db *docstore.Client }

func NewDevices(backend docstore.Backend) *Devices {
	return &Devices{db: docstore.New(backend)}
}

func (d *Devices) ref(id string) *docstore.DocumentRef {
	return d.db.Collection("client_authorizations").Doc(id)
}

func (d *Devices) CreateDevice(ctx context.Context, id string, record clientauth.DeviceRecord) error {
	_, err := d.ref(id).Create(ctx, record)
	return err
}

func (d *Devices) Device(ctx context.Context, id string) (clientauth.DeviceRecord, error) {
	return deviceRecord(d.ref(id).Get(ctx))
}

func deviceRecord(doc *docstore.DocumentSnapshot, err error) (clientauth.DeviceRecord, error) {
	var record clientauth.DeviceRecord
	if notFound(err) {
		return record, state.ErrNotFound
	}
	if err == nil {
		err = doc.DataTo(&record)
	}
	if err == nil && !time.Now().Before(record.ExpiresAt) {
		err = state.ErrNotFound
	}
	return record, err
}

func (d *Devices) ApproveDevice(ctx context.Context, id, owner string, approval clientauth.DeviceApproval) error {
	return d.db.RunTransaction(ctx, func(_ context.Context, tx *docstore.Transaction) error {
		ref := d.ref(id)
		record, err := deviceRecord(tx.Get(ref))
		if err != nil {
			return err
		}
		if record.OwnerID != "" || subtle.ConstantTimeCompare([]byte(approval.Code), []byte(record.Code)) != 1 {
			return state.ErrNotFound
		}
		record.OwnerID, record.Scopes = owner, approval.Scopes
		return tx.Set(ref, record)
	})
}
