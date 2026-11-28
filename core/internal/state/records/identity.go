package records

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/state/docstore"
	"nori/internal/users"
)

var _ users.IdentityDirectory = (*Users)(nil)

func (u *Users) identityInTransaction(tx *docstore.Transaction, identity users.Identity) (users.User, bool, error) {
	iter := tx.Documents(u.rows().Where("identity_issuer", "==", identity.Issuer).
		Where("identity_subject", "==", identity.Subject).Limit(2))
	defer iter.Stop()
	doc, err := iter.Next()
	if isDone(err) {
		return users.User{}, false, nil
	}
	if err != nil {
		return users.User{}, false, err
	}
	row, err := decodeUser(doc)
	if err != nil {
		return users.User{}, false, err
	}
	if _, err := iter.Next(); !isDone(err) {
		if err != nil {
			return users.User{}, false, err
		}
		return users.User{}, false, users.ErrIdentityConflict
	}
	return row, true, nil
}

func (u *Users) bindInTransaction(tx *docstore.Transaction, row users.User, identity users.Identity) (users.User, error) {
	if row.IdentityIssuer != "" || row.IdentitySubject != "" {
		if row.HasIdentity(identity) {
			return row, nil
		}
		return users.User{}, users.ErrIdentityConflict
	}
	row.IdentityIssuer, row.IdentitySubject = identity.Issuer, identity.Subject
	if err := tx.Update(u.rows().Doc(row.ID), []docstore.Update{
		{Path: "identity_issuer", Value: identity.Issuer},
		{Path: "identity_subject", Value: identity.Subject},
	}); err != nil {
		return users.User{}, err
	}
	return row, nil
}

func (u *Users) BindIdentity(ctx context.Context, userID string, identity users.Identity) (users.User, error) {
	if err := identity.Validate(); err != nil {
		return users.User{}, err
	}
	if !users.ValidID(userID) {
		return users.User{}, users.ErrIdentityNotFound
	}
	var out users.User
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = u.db.RunTransaction(ctx, func(_ context.Context, tx *docstore.Transaction) error {
			existing, found, err := u.identityInTransaction(tx, identity)
			if err != nil {
				return err
			}
			if found && existing.ID != userID {
				return users.ErrIdentityConflict
			}
			doc, err := tx.Get(u.rows().Doc(userID))
			if notFound(err) {
				return users.ErrIdentityNotFound
			}
			if err != nil {
				return err
			}
			row, err := decodeUser(doc)
			if err != nil {
				return err
			}
			out, err = u.bindInTransaction(tx, row, identity)
			return err
		})
		if !errors.Is(err, docstore.ErrExists) {
			break
		}
	}
	return out, err
}

func (u *Users) ResolveIdentity(ctx context.Context, identity users.Identity, email, displayName, mode, inviteCode string) (users.User, error) {
	if err := identity.Validate(); err != nil {
		return users.User{}, err
	}
	candidate := users.User{
		ID: uuid.NewString(), Email: users.Email(email), DisplayName: strings.TrimSpace(displayName), TimeZone: "UTC",
		IdentityIssuer: identity.Issuer, IdentitySubject: identity.Subject,
	}
	if err := candidate.Validate(); err != nil {
		return users.User{}, err
	}
	var out users.User
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = u.db.RunTransaction(ctx, func(_ context.Context, tx *docstore.Transaction) error {
			row, found, err := u.identityInTransaction(tx, identity)
			if err != nil {
				return err
			}
			if found {
				out = row
				return nil
			}
			matches := tx.Documents(u.rows().Where("email", "==", candidate.Email).Limit(2))
			defer matches.Stop()
			doc, err := matches.Next()
			if err == nil {
				row, err = decodeUser(doc)
				if err != nil {
					return err
				}
				if row.IdentityIssuer != "" || row.IdentitySubject != "" {
					return users.ErrIdentityConflict
				}
				if !users.MayBindLegacyAccount(identity, row.Email) {
					return users.ErrIdentityUnbound
				}
				out, err = u.bindInTransaction(tx, row, identity)
				return err
			}
			if !isDone(err) {
				return err
			}
			switch mode {
			case "open":
			case "invite":
				code := users.NormalizeInviteCode(inviteCode)
				if code == "" {
					return users.ErrInviteRequired
				}
				inviteDoc, err := tx.Get(u.invites().Doc(code))
				if notFound(err) {
					return users.ErrInviteNotFound
				}
				if err != nil {
					return err
				}
				var invite users.Invite
				if err := inviteDoc.DataTo(&invite); err != nil {
					return err
				}
				now := time.Now().UTC()
				if err := invite.ValidateForRedemption(candidate.Email, now); err != nil {
					return err
				}
				if (invite.Email != "" || invite.Domain != "") && !identity.EmailAuthoritative {
					return users.ErrIdentityUnbound
				}
				invite.UseCount++
				invite.LastUsedAt = &now
				if err := tx.Set(u.invites().Doc(code), invite); err != nil {
					return err
				}
			default:
				return users.ErrIdentityNotFound
			}
			if err := tx.Create(u.rows().Doc(candidate.ID), candidate); err != nil {
				return err
			}
			if err := tx.ProvisionTrial(candidate.ID, u.now(), u.TrialMaxSignupsPerDay); err != nil {
				return err
			}
			out = candidate
			return nil
		})
		if !errors.Is(err, docstore.ErrExists) {
			break
		}
	}
	return out, err
}
