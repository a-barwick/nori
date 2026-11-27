package computer

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// MemoryStore is an in-memory Store implementation for unit tests and DSTs.
type MemoryStore struct {
	mu        sync.Mutex
	computers map[string]ComputerRecord
	sessions  map[string]SessionRecord
	fileLinks map[string]map[string]FileLink // userID -> workspacePath -> FileLink
	Now       func() time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		computers: map[string]ComputerRecord{},
		sessions:  map[string]SessionRecord{},
		fileLinks: map[string]map[string]FileLink{},
	}
}

func (s *MemoryStore) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *MemoryStore) GetComputer(_ context.Context, userID string) (ComputerRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.computers[userID]
	if !ok {
		return ComputerRecord{}, ErrNotFound
	}
	return rec, nil
}

func (s *MemoryStore) ReserveComputer(_ context.Context, rec ComputerRecord) (ComputerRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.computers[rec.UserID]; ok {
		if existing.Status == StatusDeleting {
			return existing, false, ErrDeleting
		}
		if existing.Status != StatusDeleted {
			return existing, false, nil
		}
	}
	now := s.now()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now
	s.computers[rec.UserID] = rec
	return rec, true, nil
}

func (s *MemoryStore) UpsertComputer(_ context.Context, rec ComputerRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if existing, ok := s.computers[rec.UserID]; ok {
		if existing.Status == StatusDeleting && rec.Status != StatusDeleting && rec.Status != StatusDeleted {
			return ErrDeleting
		}
		if rec.CreatedAt.IsZero() {
			rec.CreatedAt = existing.CreatedAt
		}
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now
	s.computers[rec.UserID] = rec
	return nil
}

func (s *MemoryStore) UpdateComputerStatus(_ context.Context, userID string, status Status, lastActive *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.computers[userID]
	if !ok {
		return ErrNotFound
	}
	if rec.Status == StatusDeleting && status != StatusDeleting && status != StatusDeleted {
		return ErrDeleting
	}
	rec.Status = status
	if lastActive != nil {
		t := lastActive.UTC()
		rec.LastActiveAt = &t
	}
	rec.UpdatedAt = s.now()
	s.computers[userID] = rec
	return nil
}

func (s *MemoryStore) MarkComputerDeleting(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if rec, ok := s.computers[userID]; ok && rec.Status != StatusDeleted {
		rec.Status = StatusDeleting
		rec.UpdatedAt = now
		s.computers[userID] = rec
	}
	for id, sess := range s.sessions {
		if sess.UserID == userID && sess.EndedAt == nil {
			t := now
			sess.EndedAt = &t
			s.sessions[id] = sess
		}
	}
	return nil
}

func (s *MemoryStore) MarkComputerDeleted(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if rec, ok := s.computers[userID]; ok {
		rec.Status = StatusDeleted
		rec.UpdatedAt = now
		s.computers[userID] = rec
	}
	for id, sess := range s.sessions {
		if sess.UserID == userID {
			delete(s.sessions, id)
		}
	}
	delete(s.fileLinks, userID)
	return nil
}

func (s *MemoryStore) ListDeletingComputers(_ context.Context, limit int) ([]ComputerRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ComputerRecord
	for _, rec := range s.computers {
		if rec.Status == StatusDeleting {
			out = append(out, rec)
		}
	}
	slices.SortFunc(out, func(a, b ComputerRecord) int {
		if a.UpdatedAt.Before(b.UpdatedAt) {
			return -1
		}
		if a.UpdatedAt.After(b.UpdatedAt) {
			return 1
		}
		return strings.Compare(a.UserID, b.UserID)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) CreateSession(_ context.Context, sess SessionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now
	}
	if sess.LastActivityAt.IsZero() {
		sess.LastActivityAt = now
	}
	sess.TokenHash = append([]byte(nil), sess.TokenHash...)
	s.sessions[sess.ID] = sess
	return nil
}

func (s *MemoryStore) GetSession(_ context.Context, userID, sessionID string) (SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok || sess.UserID != userID {
		return SessionRecord{}, ErrNotFound
	}
	sess.TokenHash = append([]byte(nil), sess.TokenHash...)
	return sess, nil
}

func (s *MemoryStore) GetSessionByTokenHash(_ context.Context, tokenHash []byte) (SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if bytes.Equal(sess.TokenHash, tokenHash) {
			sess.TokenHash = append([]byte(nil), sess.TokenHash...)
			return sess, nil
		}
	}
	return SessionRecord{}, ErrNotFound
}

func (s *MemoryStore) ListActiveSessions(_ context.Context, userID string) ([]SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var out []SessionRecord
	for _, sess := range s.sessions {
		if sess.UserID == userID && sess.EndedAt == nil && sess.ExpiresAt.After(now) {
			sess.TokenHash = append([]byte(nil), sess.TokenHash...)
			out = append(out, sess)
		}
	}
	slices.SortFunc(out, func(a, b SessionRecord) int {
		if a.CreatedAt.Before(b.CreatedAt) {
			return -1
		}
		if a.CreatedAt.After(b.CreatedAt) {
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

func (s *MemoryStore) UpdateSessionMode(_ context.Context, userID, sessionID string, mode SessionMode) (SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok || sess.UserID != userID {
		return SessionRecord{}, ErrNotFound
	}
	if sess.EndedAt != nil {
		return SessionRecord{}, ErrSessionRevoked
	}
	sess.Mode = mode
	sess.LastActivityAt = s.now()
	s.sessions[sessionID] = sess
	return sess, nil
}

func (s *MemoryStore) TouchSession(_ context.Context, sessionID string, activityAt, newExpiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return ErrNotFound
	}
	if sess.EndedAt != nil {
		return ErrSessionRevoked
	}
	sess.LastActivityAt = activityAt.UTC()
	if newExpiresAt.After(sess.ExpiresAt) {
		sess.ExpiresAt = newExpiresAt.UTC()
	}
	s.sessions[sessionID] = sess
	return nil
}

func (s *MemoryStore) EndSession(_ context.Context, userID, sessionID string, endedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok || sess.UserID != userID {
		return ErrNotFound
	}
	if sess.EndedAt == nil {
		t := endedAt.UTC()
		sess.EndedAt = &t
		s.sessions[sessionID] = sess
	}
	return nil
}

func (s *MemoryStore) RevokeUserSessions(_ context.Context, userID string, endedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := endedAt.UTC()
	for id, sess := range s.sessions {
		if sess.UserID == userID && sess.EndedAt == nil {
			sess.EndedAt = &t
			s.sessions[id] = sess
		}
	}
	return nil
}

func (s *MemoryStore) ExpireIdleSessions(_ context.Context, now time.Time, idleCutoff time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	t := now.UTC()
	for id, sess := range s.sessions {
		if sess.EndedAt != nil {
			continue
		}
		if !sess.ExpiresAt.After(t) || sess.LastActivityAt.Before(idleCutoff) {
			ended := t
			sess.EndedAt = &ended
			s.sessions[id] = sess
			count++
		}
	}
	return count, nil
}

func (s *MemoryStore) UpsertFileLink(_ context.Context, link FileLink) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.fileLinks[link.UserID] == nil {
		s.fileLinks[link.UserID] = map[string]FileLink{}
	}
	if existing, ok := s.fileLinks[link.UserID][link.WorkspacePath]; ok && link.CreatedAt.IsZero() {
		link.CreatedAt = existing.CreatedAt
	}
	if link.CreatedAt.IsZero() {
		link.CreatedAt = now
	}
	link.UpdatedAt = now
	link.EntityTags = slices.Clone(link.EntityTags)
	s.fileLinks[link.UserID][link.WorkspacePath] = link
	return nil
}

func (s *MemoryStore) ListFileLinksByResult(_ context.Context, userID, resultID string) ([]FileLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []FileLink
	for _, link := range s.fileLinks[userID] {
		if link.ResultID == resultID {
			link.EntityTags = slices.Clone(link.EntityTags)
			out = append(out, link)
		}
	}
	return out, nil
}

func (s *MemoryStore) ListFileLinksByEntityTag(_ context.Context, userID, tag string) ([]FileLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	needle := strings.ToLower(strings.TrimSpace(tag))
	if needle == "" {
		return nil, nil
	}
	var out []FileLink
	for _, link := range s.fileLinks[userID] {
		if link.ResultID == PurgedLinkResultID {
			continue
		}
		matched := false
		for _, candidate := range link.EntityTags {
			if strings.ToLower(strings.TrimSpace(candidate)) == needle {
				matched = true
				break
			}
		}
		if matched {
			link.EntityTags = slices.Clone(link.EntityTags)
			out = append(out, link)
		}
	}
	return out, nil
}

func (s *MemoryStore) DeleteFileLink(_ context.Context, userID, workspacePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.fileLinks[userID]; m != nil {
		delete(m, workspacePath)
	}
	return nil
}

func (s *MemoryStore) DeleteFileLinksByUser(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.fileLinks, userID)
	return nil
}
