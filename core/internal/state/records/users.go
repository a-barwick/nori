package records

import (
	"context"
	"fmt"

	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/users"
)

// Users reads the user directory in the same database as scoped records. It is a trusted
// server/CLI surface; no route exposes whole rows.
type Users struct{ db *docstore.Client }

func NewUsers(backend docstore.Backend) *Users { return &Users{db: docstore.New(backend)} }
func NewSharedUsers(backend docstore.Backend) *Users {
	return &Users{db: docstore.New(unclosedBackend{backend})}
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
	// Email uniqueness is transactional, including concurrent local provisioning.
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
		return tx.Create(u.rows().Doc(row.ID), row)
	})
}
func (u *Users) Paused(ctx context.Context) (bool, error) {
	c, err := readControl(ctx, controlDoc(u.db))
	return c.Paused, err
}
