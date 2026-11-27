package push

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemoryStore is an in-memory Store implementation for unit tests and DST.
type MemoryStore struct {
	mu              sync.Mutex
	Now             func() time.Time
	devicesByID     map[string]Device
	deviceIDByToken map[string]string
	outboxByID      map[string]OutboxItem
}

// NewMemoryStore creates an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		devicesByID:     make(map[string]Device),
		deviceIDByToken: make(map[string]string),
		outboxByID:      make(map[string]OutboxItem),
	}
}

func (s *MemoryStore) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *MemoryStore) RegisterDevice(_ context.Context, userID, token, deviceID, env string) (Device, error) {
	var err error
	userID, token, deviceID, env, err = normalizeRegisterArgs(userID, token, deviceID, env)
	if err != nil {
		return Device{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if existingID, ok := s.deviceIDByToken[token]; ok {
		existing := s.devicesByID[existingID]
		if existing.UserID == userID {
			existing.DeviceID = deviceID
			existing.Environment = env
			existing.UpdatedAt = now
			s.devicesByID[existingID] = existing
			return existing, nil
		}
		// Reassign token to the new user with a fresh ID so the prior user cannot
		// delete the new user's registration with the old ID.
		delete(s.devicesByID, existingID)
	}

	dev := Device{
		ID:          uuid.NewString(),
		UserID:      userID,
		DeviceID:    deviceID,
		Token:       token,
		Environment: env,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	s.devicesByID[dev.ID] = dev
	s.deviceIDByToken[token] = dev.ID
	return dev, nil
}

func (s *MemoryStore) DeleteDevice(_ context.Context, userID, id string) error {
	userID = strings.TrimSpace(userID)
	id = strings.TrimSpace(id)
	if userID == "" || id == "" {
		return ErrNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	dev, ok := s.devicesByID[id]
	if !ok || dev.UserID != userID {
		return ErrNotFound
	}
	delete(s.devicesByID, id)
	if curID, ok := s.deviceIDByToken[dev.Token]; ok && curID == id {
		delete(s.deviceIDByToken, dev.Token)
	}
	return nil
}

func (s *MemoryStore) DeleteDeviceByToken(_ context.Context, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	id, ok := s.deviceIDByToken[token]
	if !ok {
		return nil
	}
	delete(s.deviceIDByToken, token)
	delete(s.devicesByID, id)
	return nil
}

func (s *MemoryStore) DeleteUserDevices(_ context.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for id, dev := range s.devicesByID {
		if dev.UserID == userID {
			delete(s.devicesByID, id)
			if curID, ok := s.deviceIDByToken[dev.Token]; ok && curID == id {
				delete(s.deviceIDByToken, dev.Token)
			}
		}
	}
	return nil
}

func (s *MemoryStore) ListDevices(_ context.Context, userID string) ([]Device, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Device, 0)
	for _, dev := range s.devicesByID {
		if dev.UserID == userID {
			out = append(out, dev)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *MemoryStore) EnqueueOutbox(_ context.Context, item OutboxItem) (OutboxItem, bool, error) {
	item.DeliveryID = strings.TrimSpace(item.DeliveryID)
	item.UserID = strings.TrimSpace(item.UserID)
	if item.DeliveryID == "" {
		return OutboxItem{}, false, ErrInvalidDeliveryID
	}
	if item.UserID == "" {
		return OutboxItem{}, false, ErrInvalidUserID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.outboxByID[item.DeliveryID]; ok {
		return cloneOutboxItem(existing), false, nil
	}

	now := s.now()
	if item.Status == "" {
		item.Status = StatusPending
	}
	if item.CollapseID == "" {
		item.CollapseID = item.DeliveryID
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = now
	}
	s.outboxByID[item.DeliveryID] = cloneOutboxItem(item)
	return cloneOutboxItem(item), true, nil
}

func (s *MemoryStore) GetOutbox(_ context.Context, deliveryID string) (OutboxItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.outboxByID[strings.TrimSpace(deliveryID)]
	if !ok {
		return OutboxItem{}, ErrNotFound
	}
	return cloneOutboxItem(item), nil
}

func (s *MemoryStore) ListPendingOutbox(_ context.Context, limit int) ([]OutboxItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]OutboxItem, 0)
	for _, item := range s.outboxByID {
		if item.Status == StatusPending {
			out = append(out, cloneOutboxItem(item))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].DeliveryID < out[j].DeliveryID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) MarkOutboxSent(_ context.Context, deliveryID string, sentAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.outboxByID[deliveryID]
	if !ok {
		return ErrNotFound
	}
	ts := sentAt.UTC()
	if ts.IsZero() {
		ts = s.now()
	}
	item.Status = StatusSent
	item.Attempts++
	item.LastError = ""
	item.SentAt = &ts
	item.UpdatedAt = ts
	s.outboxByID[deliveryID] = item
	return nil
}

func (s *MemoryStore) MarkOutboxFailed(_ context.Context, deliveryID string, lastErr string, permanent bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.outboxByID[deliveryID]
	if !ok {
		return ErrNotFound
	}
	item.Attempts++
	item.LastError = lastErr
	if permanent {
		item.Status = StatusFailed
	}
	item.UpdatedAt = s.now()
	s.outboxByID[deliveryID] = item
	return nil
}

func (s *MemoryStore) MarkOutboxSuppressed(_ context.Context, deliveryID string, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.outboxByID[deliveryID]
	if !ok {
		return ErrNotFound
	}
	item.Status = StatusSuppressed
	item.LastError = reason
	item.UpdatedAt = s.now()
	s.outboxByID[deliveryID] = item
	return nil
}

func (s *MemoryStore) CountSentSince(_ context.Context, userID string, since time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0
	for _, item := range s.outboxByID {
		if item.UserID != userID || item.Status != StatusSent || item.SentAt == nil {
			continue
		}
		if !item.SentAt.Before(since) {
			count++
		}
	}
	return count, nil
}

func cloneOutboxItem(item OutboxItem) OutboxItem {
	if item.SentAt != nil {
		ts := *item.SentAt
		item.SentAt = &ts
	}
	return item
}
