package records

import (
	"context"
	"fmt"
	"time"

	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/users"
)

type Users struct {
	db                    *docstore.Client
	TrialMaxSignupsPerDay int
	Now                   func() time.Time
}

var _ users.TrialCapSetter = (*Users)(nil)

func NewUsers(backend docstore.Backend) *Users { return &Users{db: docstore.New(backend)} }
func NewSharedUsers(backend docstore.Backend) *Users {
	return &Users{db: docstore.New(unclosedBackend{backend})}
}

func (u *Users) SetTrialMaxSignupsPerDay(maxPerDay int) {
	u.TrialMaxSignupsPerDay = maxPerDay
	for b := u.db.Backend; b != nil; {
		if setter, ok := b.(users.TrialCapSetter); ok {
			setter.SetTrialMaxSignupsPerDay(maxPerDay)
			return
		}
		uw, ok := b.(docstore.BackendUnwrapper)
		if !ok {
			return
		}
		b = uw.Unwrap()
	}
}

func (u *Users) now() time.Time {
	if u != nil && u.Now != nil {
		return u.Now().UTC()
	}
	return time.Time{}
}
func (u *Users) Close() error                  { return u.db.Close() }
func (u *Users) rows() *docstore.CollectionRef { return u.db.Collection("users") }
func (u *Users) Get(ctx context.Context, id string) (users.User, error) {
	if !users.ValidID(id) {
		return users.User{}, state.ErrNotFound
	}
	doc, err := u.rows().Doc(id).Get(ctx)
	if notFound(err) {
		return users.User{}, state.ErrNotFound
	}
	if err != nil {
		return users.User{}, err
	}
	return decodeUser(doc)
}
func decodeUser(doc *docstore.DocumentSnapshot) (users.User, error) {
	var row users.User
	if err := doc.DataTo(&row); err != nil {
		return row, err
	}
	if row.ID != doc.Ref.ID {
		return row, fmt.Errorf("user ID does not match its row")
	}
	return row, row.Validate()
}
func userRows(iter *docstore.DocumentIterator) ([]users.User, error) {
	defer iter.Stop()
	rows := []users.User{}
	for {
		doc, err := iter.Next()
		if isDone(err) {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		row, err := decodeUser(doc)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
}
func (u *Users) FindEmail(ctx context.Context, email string) (users.User, error) {
	rows, err := userRows(u.rows().Where("email", "==", users.Email(email)).Limit(2).Documents(ctx))
	if err != nil {
		return users.User{}, err
	}
	if len(rows) != 1 {
		return users.User{}, state.ErrNotFound
	}
	return rows[0], nil
}
func (u *Users) List(ctx context.Context) ([]users.User, error) {
	return userRows(u.rows().Documents(ctx))
}
func (u *Users) Create(ctx context.Context, row users.User) error {
	row.Email = users.Email(row.Email)
	if err := row.Validate(); err != nil {
		return err
	}
	return u.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		matches := tx.Documents(u.rows().Where("email", "==", row.Email).Limit(1))
		defer matches.Stop()
		_, err := matches.Next()
		if err == nil {
			return fmt.Errorf("a user with that email already exists")
		}
		if !isDone(err) {
			return err
		}
		if err := tx.Create(u.rows().Doc(row.ID), row); err != nil {
			return err
		}
		return tx.ProvisionTrial(row.ID, u.now(), u.TrialMaxSignupsPerDay)
	})
}
func (u *Users) Paused(ctx context.Context) (bool, error) {
	c, err := readControl(ctx, controlDoc(u.db))
	return c.Paused, err
}

func (u *Users) SetPaused(ctx context.Context, paused bool) error {
	return u.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		if err := tx.Set(controlDoc(u.db), state.Control{Paused: paused}); err != nil {
			return err
		}
		if paused {
			return tx.NotifyPause()
		}
		return tx.WakeWaitingTasks("", docstore.WaitPaused)
	})
}
