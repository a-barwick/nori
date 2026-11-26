package users

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"nori/internal/state"
)

// MemoryDirectory is a concurrency-safe in-memory Directory, AutoProvisioner,
// and InviteStore for tests and local tooling.
type MemoryDirectory struct {
	mu      sync.Mutex
	byID    map[string]User
	byEmail map[string]string
	invites map[string]Invite
	paused  bool
	now     func() time.Time
}

func NewMemoryDirectory() *MemoryDirectory {
	return &MemoryDirectory{
		byID:    make(map[string]User),
		byEmail: make(map[string]string),
		invites: make(map[string]Invite),
		now:     func() time.Time { return time.Now().UTC() },
	}
}

func (m *MemoryDirectory) WithClock(now func() time.Time) *MemoryDirectory {
	m.mu.Lock()
	defer m.mu.Unlock()
	if now != nil {
		m.now = now
	}
	return m
}

func (m *MemoryDirectory) Get(_ context.Context, id string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byID[id]
	if !ok {
		return User{}, state.ErrNotFound
	}
	return u, nil
}

func (m *MemoryDirectory) FindEmail(_ context.Context, email string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byEmail[Email(email)]
	if !ok {
		return User{}, state.ErrNotFound
	}
	return m.byID[id], nil
}

func (m *MemoryDirectory) List(_ context.Context) ([]User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]User, 0, len(m.byID))
	for _, u := range m.byID {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *MemoryDirectory) Paused(_ context.Context) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.paused, nil
}

func (m *MemoryDirectory) Create(_ context.Context, row User) error {
	row.Email = Email(row.Email)
	if err := row.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.byEmail[row.Email]; exists {
		return fmt.Errorf("a user with that email already exists")
	}
	if _, exists := m.byID[row.ID]; exists {
		return fmt.Errorf("a user with that ID already exists")
	}
	m.byID[row.ID] = row
	m.byEmail[row.Email] = row.ID
	return nil
}

func (m *MemoryDirectory) Close() error { return nil }

func (m *MemoryDirectory) FindOrCreateByEmail(_ context.Context, email, displayName string) (User, bool, error) {
	normalized := Email(email)
	candidate := User{
		ID:          uuid.NewString(),
		Email:       normalized,
		DisplayName: strings.TrimSpace(displayName),
		TimeZone:    "UTC",
	}
	if err := candidate.Validate(); err != nil {
		return User{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.byEmail[normalized]; ok {
		return m.byID[id], false, nil
	}
	m.byID[candidate.ID] = candidate
	m.byEmail[normalized] = candidate.ID
	return candidate, true, nil
}

func (m *MemoryDirectory) CreateInvite(_ context.Context, opts CreateInviteOptions) (Invite, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, err := PrepareInvite(opts, m.now())
	if err != nil {
		return Invite{}, err
	}
	if _, exists := m.invites[inv.Code]; exists {
		return Invite{}, fmt.Errorf("%w: invite code already exists", ErrInviteInvalid)
	}
	m.invites[inv.Code] = inv
	return inv, nil
}

func (m *MemoryDirectory) ListInvites(_ context.Context) ([]Invite, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Invite, 0, len(m.invites))
	for _, inv := range m.invites {
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

func (m *MemoryDirectory) RevokeInvite(_ context.Context, code string) (Invite, error) {
	normalized := NormalizeInviteCode(code)
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.invites[normalized]
	if !ok {
		return Invite{}, ErrInviteNotFound
	}
	if inv.RevokedAt == nil {
		now := m.now().UTC()
		inv.RevokedAt = &now
		m.invites[normalized] = inv
	}
	return inv, nil
}

func (m *MemoryDirectory) RedeemInviteAndCreateUser(_ context.Context, code, email, displayName string) (User, Invite, error) {
	normalizedCode := NormalizeInviteCode(code)
	normalizedEmail := Email(email)
	candidate := User{
		ID:          uuid.NewString(),
		Email:       normalizedEmail,
		DisplayName: strings.TrimSpace(displayName),
		TimeZone:    "UTC",
	}
	if err := candidate.Validate(); err != nil {
		return User{}, Invite{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.byEmail[normalizedEmail]; ok {
		return m.byID[id], Invite{}, nil
	}
	inv, ok := m.invites[normalizedCode]
	if !ok {
		return User{}, Invite{}, ErrInviteNotFound
	}
	now := m.now().UTC()
	if err := inv.ValidateForRedemption(normalizedEmail, now); err != nil {
		return User{}, Invite{}, err
	}
	inv.UseCount++
	inv.LastUsedAt = &now
	m.invites[normalizedCode] = inv
	m.byID[candidate.ID] = candidate
	m.byEmail[normalizedEmail] = candidate.ID
	return candidate, inv, nil
}
