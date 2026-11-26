package users

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

var _ IdentityDirectory = (*MemoryDirectory)(nil)

func (m *MemoryDirectory) identityUser(identity Identity) (User, bool) {
	for _, row := range m.byID {
		if row.HasIdentity(identity) {
			return row, true
		}
	}
	return User{}, false
}

func (m *MemoryDirectory) BindIdentity(_ context.Context, userID string, identity Identity) (User, error) {
	if err := identity.Validate(); err != nil {
		return User{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if row, found := m.identityUser(identity); found && row.ID != userID {
		return User{}, ErrIdentityConflict
	}
	row, found := m.byID[userID]
	if !found {
		return User{}, ErrIdentityNotFound
	}
	if (row.IdentityIssuer != "" || row.IdentitySubject != "") && !row.HasIdentity(identity) {
		return User{}, ErrIdentityConflict
	}
	row.IdentityIssuer, row.IdentitySubject = identity.Issuer, identity.Subject
	m.byID[row.ID] = row
	return row, nil
}

func (m *MemoryDirectory) ResolveIdentity(_ context.Context, identity Identity, email, displayName, mode, inviteCode string) (User, error) {
	if err := identity.Validate(); err != nil {
		return User{}, err
	}
	candidate := User{
		ID: uuid.NewString(), Email: Email(email), DisplayName: strings.TrimSpace(displayName), TimeZone: "UTC",
		IdentityIssuer: identity.Issuer, IdentitySubject: identity.Subject,
	}
	if err := candidate.Validate(); err != nil {
		return User{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if row, found := m.identityUser(identity); found {
		return row, nil
	}
	if id, found := m.byEmail[candidate.Email]; found {
		row := m.byID[id]
		if row.IdentityIssuer != "" || row.IdentitySubject != "" {
			return User{}, ErrIdentityConflict
		}
		if !MayBindLegacyAccount(identity, row.Email) {
			return User{}, ErrIdentityUnbound
		}
		row.IdentityIssuer, row.IdentitySubject = identity.Issuer, identity.Subject
		m.byID[row.ID] = row
		return row, nil
	}
	switch mode {
	case "open":
	case "invite":
		code := NormalizeInviteCode(inviteCode)
		if code == "" {
			return User{}, ErrInviteRequired
		}
		invite, found := m.invites[code]
		if !found {
			return User{}, ErrInviteNotFound
		}
		now := m.now().UTC()
		if err := invite.ValidateForRedemption(candidate.Email, now); err != nil {
			return User{}, err
		}
		if (invite.Email != "" || invite.Domain != "") && !identity.EmailAuthoritative {
			return User{}, ErrIdentityUnbound
		}
		invite.UseCount++
		invite.LastUsedAt = &now
		m.invites[code] = invite
	default:
		return User{}, ErrIdentityNotFound
	}
	m.byID[candidate.ID] = candidate
	m.byEmail[candidate.Email] = candidate.ID
	return candidate, nil
}
