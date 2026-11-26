package records

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"nori/internal/state"
	"nori/internal/state/docstore"
)

var _ state.ResourceStore = (*Database)(nil)

type resourceDoc struct {
	ID        string            `firestore:"id"`
	Revision  int64             `firestore:"revision"`
	Body      string            `firestore:"body"`
	Index     map[string]string `firestore:"index,omitempty"`
	CreatedAt time.Time         `firestore:"created_at"`
	UpdatedAt time.Time         `firestore:"updated_at"`
}

type receiptDoc struct {
	RequestID  string    `firestore:"request_id"`
	Binding    string    `firestore:"binding"`
	Status     string    `firestore:"status"`
	Resource   string    `firestore:"resource"`
	TaskID     string    `firestore:"task_id,omitempty"`
	Operation  string    `firestore:"operation_id,omitempty"`
	Auth       string    `firestore:"auth,omitempty"`
	HTTPStatus int       `firestore:"http_status"`
	CreatedAt  time.Time `firestore:"created_at"`
}

func (d *Database) resources(collection string) *docstore.CollectionRef {
	return d.root().Collection("r_" + collection)
}
func (d *Database) receipts() *docstore.CollectionRef {
	return d.root().Collection("mutation_receipts")
}

func toResource(collection string, doc *docstore.DocumentSnapshot) (state.Resource, error) {
	var stored resourceDoc
	if err := doc.DataTo(&stored); err != nil {
		return state.Resource{}, err
	}
	return state.Resource{
		Collection: collection, ID: stored.ID, Revision: stored.Revision, Body: json.RawMessage(stored.Body),
		Index: stored.Index, CreatedAt: stored.CreatedAt, UpdatedAt: stored.UpdatedAt,
	}, nil
}

func fromResource(r state.Resource) resourceDoc {
	return resourceDoc{ID: r.ID, Revision: r.Revision, Body: string(r.Body), Index: r.Index, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
}

func toReceipt(doc *docstore.DocumentSnapshot) (state.MutationReceipt, error) {
	var stored receiptDoc
	if err := doc.DataTo(&stored); err != nil {
		return state.MutationReceipt{}, err
	}
	receipt := state.MutationReceipt{
		RequestID: stored.RequestID, Binding: stored.Binding, Accepted: true, Status: stored.Status,
		Resource: json.RawMessage(stored.Resource), HTTPStatus: stored.HTTPStatus, CreatedAt: stored.CreatedAt,
	}
	if stored.Auth != "" {
		receipt.Auth = json.RawMessage(stored.Auth)
	}
	if stored.TaskID != "" {
		receipt.Work = &state.WorkRef{TaskID: stored.TaskID, OperationID: stored.Operation}
	}
	return receipt, nil
}

func (d *Database) Resource(ctx context.Context, collection, id string) (state.Resource, error) {
	if !state.ValidResourceID(id) {
		return state.Resource{}, state.ErrNotFound
	}
	doc, err := d.resources(collection).Doc(id).Get(ctx)
	if notFound(err) {
		return state.Resource{}, state.ErrNotFound
	}
	if err != nil {
		return state.Resource{}, err
	}
	return toResource(collection, doc)
}

func (d *Database) Resources(ctx context.Context, collection string, filter map[string]string, after string, limit int) ([]state.Resource, string, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	query := d.resources(collection).Query
	fields := make([]string, 0, len(filter))
	for field, value := range filter {
		if value != "" {
			fields = append(fields, field)
		}
	}
	slices.Sort(fields)
	for _, field := range fields {
		query = query.Where("index."+field, "==", filter[field])
	}
	query = query.OrderBy(docstore.DocumentID, docstore.Asc)
	if after != "" {
		query = query.Where(docstore.DocumentID, ">", after)
	}
	docs, err := query.Limit(limit + 1).Documents(ctx).GetAll()
	if err != nil {
		return nil, "", err
	}
	out := []state.Resource{}
	for i, doc := range docs {
		if i == limit {
			return out, out[len(out)-1].ID, nil
		}
		r, err := toResource(collection, doc)
		if err != nil {
			return nil, "", err
		}
		out = append(out, r)
	}
	return out, "", nil
}

func (d *Database) Receipt(ctx context.Context, requestID string) (state.MutationReceipt, error) {
	if !state.ValidResourceID(requestID) {
		return state.MutationReceipt{}, state.ErrNotFound
	}
	doc, err := d.receipts().Doc(requestID).Get(ctx)
	if notFound(err) {
		return state.MutationReceipt{}, state.ErrNotFound
	}
	if err != nil {
		return state.MutationReceipt{}, err
	}
	return toReceipt(doc)
}

func (d *Database) Mutate(ctx context.Context, m state.Mutation) (state.MutationReceipt, error) {
	if !state.ValidResourceID(m.RequestID) || !state.ValidResourceID(m.ID) || m.Apply == nil {
		return state.MutationReceipt{}, state.ErrInvalidResource
	}
	binding, err := state.MutationBinding(m)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	var receipt state.MutationReceipt
	err = d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		receiptRef := d.receipts().Doc(m.RequestID)
		saved, err := tx.Get(receiptRef)
		if err == nil {
			prior, err := toReceipt(saved)
			if err != nil {
				return err
			}
			if prior.Binding != binding {
				return state.ErrIdempotencyConflict
			}
			receipt = prior
			return nil
		}
		if !notFound(err) {
			return err
		}
		read := func(collection, id string) (*state.Resource, error) {
			if !state.ValidResourceID(id) {
				return nil, nil
			}
			doc, err := tx.Get(d.resources(collection).Doc(id))
			if notFound(err) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			r, err := toResource(collection, doc)
			return &r, err
		}
		current, err := read(m.Collection, m.ID)
		if err != nil {
			return err
		}
		revision := int64(0)
		if current != nil {
			revision = current.Revision
		} else if m.ExpectedRevision > 0 {
			return state.ErrNotFound
		}
		if !m.SkipRevisionCheck && revision != m.ExpectedRevision {
			return state.ErrRevisionConflict
		}
		result, err := m.Apply(current, read)
		if err != nil {
			return err
		}
		now := d.now()
		previousAll := make([]*state.Resource, len(result.Writes))
		for i, write := range result.Writes {
			if !state.ValidResourceID(write.ID) {
				return state.ErrInvalidResource
			}
			if write.Collection == m.Collection && write.ID == m.ID {
				previousAll[i] = current
				continue
			}
			if previousAll[i], err = read(write.Collection, write.ID); err != nil {
				return err
			}
		}
		for i, write := range result.Writes {
			previous := previousAll[i]
			write.UpdatedAt = now
			write.CreatedAt = now
			if previous != nil {
				write.CreatedAt = previous.CreatedAt
				if write.Revision != previous.Revision+1 {
					return state.ErrRevisionConflict
				}
			} else if write.Revision != 1 {
				return state.ErrRevisionConflict
			}
			if err := tx.Set(d.resources(write.Collection).Doc(write.ID), fromResource(write)); err != nil {
				return err
			}
		}
		for _, key := range result.Deletes {
			if err := tx.Delete(d.resources(key.Collection).Doc(key.ID)); err != nil {
				return err
			}
		}
		receipt = result.Receipt
		receipt.RequestID, receipt.Binding, receipt.Accepted, receipt.CreatedAt = m.RequestID, binding, true, now
		stored := receiptDoc{
			RequestID: m.RequestID, Binding: binding, Status: receipt.Status, Resource: string(receipt.Resource),
			Auth: string(receipt.Auth), HTTPStatus: receipt.HTTPStatus, CreatedAt: now,
		}
		if receipt.Work != nil {
			stored.TaskID, stored.Operation = receipt.Work.TaskID, receipt.Work.OperationID
		}
		return tx.Create(receiptRef, stored)
	})
	if errors.Is(err, docstore.ErrExists) {
		saved, readErr := d.Receipt(ctx, m.RequestID)
		if readErr != nil {
			return state.MutationReceipt{}, err
		}
		if saved.Binding != binding {
			return state.MutationReceipt{}, state.ErrIdempotencyConflict
		}
		return saved, nil
	}
	return receipt, err
}

func (d *Database) PutResource(ctx context.Context, r state.Resource, expected int64) (state.Resource, error) {
	if !state.ValidResourceID(r.ID) {
		return r, state.ErrInvalidResource
	}
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		ref := d.resources(r.Collection).Doc(r.ID)
		doc, err := tx.Get(ref)
		now := d.now()
		r.UpdatedAt, r.CreatedAt = now, now
		switch {
		case notFound(err):
			if expected != 0 {
				return state.ErrRevisionConflict
			}
			r.Revision = 1
			return tx.Create(ref, fromResource(r))
		case err != nil:
			return err
		}
		current, err := toResource(r.Collection, doc)
		if err != nil {
			return err
		}
		if current.Revision != expected {
			return state.ErrRevisionConflict
		}
		r.Revision, r.CreatedAt = current.Revision+1, current.CreatedAt
		return tx.Set(ref, fromResource(r))
	})
	return r, err
}
