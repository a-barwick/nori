package records

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/state/docstore"
	"nori/internal/users"
)

var (
	_ users.AutoProvisioner = (*Users)(nil)
	_ users.InviteStore     = (*Users)(nil)
)

func (u *Users) invites() *docstore.CollectionRef { return u.db.Collection("invites") }

func asAutoProvisioner(b docstore.Backend) (users.AutoProvisioner, bool) {
	for b != nil {
		if p, ok := b.(users.AutoProvisioner); ok {
			return p, true
		}
		uw, ok := b.(docstore.BackendUnwrapper)
		if !ok {
			return nil, false
		}
		b = uw.Unwrap()
	}
	return nil, false
}

func asInviteStore(b docstore.Backend) (users.InviteStore, bool) {
	for b != nil {
		if s, ok := b.(users.InviteStore); ok {
			return s, true
		}
		uw, ok := b.(docstore.BackendUnwrapper)
		if !ok {
			return nil, false
		}
		b = uw.Unwrap()
	}
	return nil, false
}

func (u *Users) FindOrCreateByEmail(ctx context.Context, email, displayName string) (users.User, bool, error) {
	if native, ok := asAutoProvisioner(u.db.Backend); ok {
		return native.FindOrCreateByEmail(ctx, email, displayName)
	}
	normalized := users.Email(email)
	candidate := users.User{
		ID:          uuid.NewString(),
		Email:       normalized,
		DisplayName: strings.TrimSpace(displayName),
		TimeZone:    "UTC",
	}
	if err := candidate.Validate(); err != nil {
		return users.User{}, false, err
	}
	var out users.User
	var created bool
	err := u.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		matches := tx.Documents(u.rows().Where("email", "==", normalized).Limit(1))
		defer matches.Stop()
		doc, err := matches.Next()
		if err == nil {
			existing, decErr := decodeUser(doc)
			if decErr != nil {
				return decErr
			}
			out = existing
			created = false
			return nil
		}
		if !isDone(err) {
			return err
		}
		if err := tx.Create(u.rows().Doc(candidate.ID), candidate); err != nil {
			return err
		}
		out = candidate
		created = true
		return nil
	})
	return out, created, err
}

func (u *Users) CreateInvite(ctx context.Context, opts users.CreateInviteOptions) (users.Invite, error) {
	if native, ok := asInviteStore(u.db.Backend); ok {
		return native.CreateInvite(ctx, opts)
	}
	inv, err := users.PrepareInvite(opts, time.Now().UTC())
	if err != nil {
		return users.Invite{}, err
	}
	if _, err := u.invites().Doc(inv.Code).Create(ctx, inv); err != nil {
		if errors.Is(err, docstore.ErrExists) {
			return users.Invite{}, fmt.Errorf("%w: invite code already exists", users.ErrInviteInvalid)
		}
		return users.Invite{}, err
	}
	return inv, nil
}

func (u *Users) ListInvites(ctx context.Context) ([]users.Invite, error) {
	if native, ok := asInviteStore(u.db.Backend); ok {
		return native.ListInvites(ctx)
	}
	iter := u.invites().Documents(ctx)
	defer iter.Stop()
	out := []users.Invite{}
	for {
		doc, err := iter.Next()
		if isDone(err) {
			break
		}
		if err != nil {
			return nil, err
		}
		var inv users.Invite
		if err := doc.DataTo(&inv); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].Code < out[j].Code
	})
	return out, nil
}

func (u *Users) RevokeInvite(ctx context.Context, code string) (users.Invite, error) {
	if native, ok := asInviteStore(u.db.Backend); ok {
		return native.RevokeInvite(ctx, code)
	}
	normalized := users.NormalizeInviteCode(code)
	if normalized == "" {
		return users.Invite{}, users.ErrInviteNotFound
	}
	var out users.Invite
	err := u.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		doc, err := tx.Get(u.invites().Doc(normalized))
		if notFound(err) {
			return users.ErrInviteNotFound
		}
		if err != nil {
			return err
		}
		var inv users.Invite
		if err := doc.DataTo(&inv); err != nil {
			return err
		}
		if inv.RevokedAt == nil {
			now := time.Now().UTC()
			inv.RevokedAt = &now
			if err := tx.Set(u.invites().Doc(normalized), inv); err != nil {
				return err
			}
		}
		out = inv
		return nil
	})
	return out, err
}

func (u *Users) RedeemInviteAndCreateUser(ctx context.Context, code, email, displayName string) (users.User, users.Invite, error) {
	if native, ok := asInviteStore(u.db.Backend); ok {
		return native.RedeemInviteAndCreateUser(ctx, code, email, displayName)
	}
	normalizedCode := users.NormalizeInviteCode(code)
	normalizedEmail := users.Email(email)
	candidate := users.User{
		ID:          uuid.NewString(),
		Email:       normalizedEmail,
		DisplayName: strings.TrimSpace(displayName),
		TimeZone:    "UTC",
	}
	if err := candidate.Validate(); err != nil {
		return users.User{}, users.Invite{}, err
	}
	if normalizedCode == "" {
		return users.User{}, users.Invite{}, users.ErrInviteNotFound
	}
	var outUser users.User
	var outInvite users.Invite
	err := u.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		matches := tx.Documents(u.rows().Where("email", "==", normalizedEmail).Limit(1))
		defer matches.Stop()
		if doc, err := matches.Next(); err == nil {
			existing, decErr := decodeUser(doc)
			if decErr != nil {
				return decErr
			}
			outUser = existing
			outInvite = users.Invite{}
			return nil
		} else if !isDone(err) {
			return err
		}

		invDoc, err := tx.Get(u.invites().Doc(normalizedCode))
		if notFound(err) {
			return users.ErrInviteNotFound
		}
		if err != nil {
			return err
		}
		var inv users.Invite
		if err := invDoc.DataTo(&inv); err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := inv.ValidateForRedemption(normalizedEmail, now); err != nil {
			return err
		}
		inv.UseCount++
		inv.LastUsedAt = &now
		if err := tx.Set(u.invites().Doc(normalizedCode), inv); err != nil {
			return err
		}
		if err := tx.Create(u.rows().Doc(candidate.ID), candidate); err != nil {
			return err
		}
		outUser = candidate
		outInvite = inv
		return nil
	})
	return outUser, outInvite, err
}
