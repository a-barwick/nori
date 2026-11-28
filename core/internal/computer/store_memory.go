package computer

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"nori/internal/billing"
	"nori/internal/state"
)

// MemoryStore is an in-memory Store implementation for unit tests and DSTs.
// It mirrors PostgresStore's single-transaction atomic semantics under s.mu.
type MemoryStore struct {
	mu           sync.Mutex
	computers    map[string]ComputerRecord
	sessions     map[string]SessionRecord
	fileLinks    map[string]map[string]FileLink // userID -> workspacePath -> FileLink
	usageLog     map[string][]state.Usage       // userID -> finalized computer usage rows
	usageIDs     map[string]bool                // idempotency set for usage IDs
	OnMeterUsage func(ctx context.Context, userID string, u state.Usage, costMicros int64) error
	Now          func() time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		computers: map[string]ComputerRecord{},
		sessions:  map[string]SessionRecord{},
		fileLinks: map[string]map[string]FileLink{},
		usageLog:  map[string][]state.Usage{},
		usageIDs:  map[string]bool{},
	}
}

func (s *MemoryStore) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// MeteredUsage returns a copy of all finalized compute usage rows recorded for userID.
func (s *MemoryStore) MeteredUsage(userID string) []state.Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.usageLog[userID])
}

// UsageSamplesForBilling converts recorded computer usage for userID into billing.UsageSample
// slices so billing.MemoryStore.UsageReader can consume Sprite charges in unit tests and DSTs.
func (s *MemoryStore) UsageSamplesForBilling(userID string) []billing.UsageSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.usageLog[userID]
	out := make([]billing.UsageSample, 0, len(rows))
	for _, u := range rows {
		var costCopy *float64
		if u.CostUSD != nil {
			v := *u.CostUSD
			costCopy = &v
		}
		out = append(out, billing.UsageSample{
			CreatedAt:            u.CreatedAt,
			Billing:              u.Billing,
			CostUSD:              costCopy,
			EstimatedExposureUSD: u.EstimatedExposureUSD,
			CostStatus:           u.CostStatus,
			RequestStatus:        u.RequestStatus,
		})
	}
	return out
}

func cloneTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC()
	return &v
}

func cloneComputerRecord(rec ComputerRecord) ComputerRecord {
	rec.LastActiveAt = cloneTimePtr(rec.LastActiveAt)
	rec.LastActivityAt = cloneTimePtr(rec.LastActivityAt)
	rec.AwakeSince = cloneTimePtr(rec.AwakeSince)
	rec.LastMeteredAt = cloneTimePtr(rec.LastMeteredAt)
	rec.SleepingSince = cloneTimePtr(rec.SleepingSince)
	if rec.ProviderOwner == "" {
		rec.ProviderOwner = DefaultProviderOwner
	}
	return rec
}

func (s *MemoryStore) countAwakeLocked(excludeUserID string) int {
	count := 0
	for uid, rec := range s.computers {
		if excludeUserID != "" && uid == excludeUserID {
			continue
		}
		if isAwakeStatus(rec.Status) {
			count++
		}
	}
	return count
}

func (s *MemoryStore) GetComputer(_ context.Context, userID string) (ComputerRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.computers[userID]
	if !ok {
		return ComputerRecord{}, ErrNotFound
	}
	return cloneComputerRecord(rec), nil
}

func (s *MemoryStore) ReserveComputer(ctx context.Context, rec ComputerRecord) (ComputerRecord, bool, error) {
	return s.ReserveComputerWithCap(ctx, rec, 0)
}

func (s *MemoryStore) ReserveComputerWithCap(_ context.Context, rec ComputerRecord, maxFleet int) (ComputerRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.computers[rec.UserID]; ok {
		if existing.Status == StatusDeleting {
			return cloneComputerRecord(existing), false, ErrDeleting
		}
		if existing.Status != StatusDeleted {
			if maxFleet > 0 && !isAwakeStatus(existing.Status) && s.countAwakeLocked(rec.UserID) >= maxFleet {
				return ComputerRecord{}, false, ErrFleetCapReached
			}
			return cloneComputerRecord(existing), false, nil
		}
	}
	if maxFleet > 0 && s.countAwakeLocked(rec.UserID) >= maxFleet {
		return ComputerRecord{}, false, ErrFleetCapReached
	}
	now := s.now()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	if rec.LastActivityAt == nil {
		if rec.LastActiveAt != nil {
			rec.LastActivityAt = cloneTimePtr(rec.LastActiveAt)
		} else {
			t := now
			rec.LastActivityAt = &t
		}
	}
	if rec.ProviderOwner == "" {
		rec.ProviderOwner = DefaultProviderOwner
	}
	rec.SleepingSince = nil
	rec.UpdatedAt = now
	s.computers[rec.UserID] = cloneComputerRecord(rec)
	return cloneComputerRecord(rec), true, nil
}

func (s *MemoryStore) CheckFleetCapAndMarkWaking(_ context.Context, userID string, maxFleet int, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.computers[userID]
	if !ok {
		return ErrNotFound
	}
	if rec.Status == StatusDeleting {
		return ErrDeleting
	}
	if isAwakeStatus(rec.Status) {
		return nil
	}
	if maxFleet > 0 && s.countAwakeLocked(userID) >= maxFleet {
		return ErrFleetCapReached
	}
	if now.IsZero() {
		now = s.now()
	}
	t := now.UTC()
	rec.Status = StatusProvisioning
	rec.LastActiveAt = &t
	rec.LastActivityAt = &t
	rec.SleepingSince = nil
	rec.UpdatedAt = s.now()
	s.computers[userID] = rec
	return nil
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
		if rec.LastActiveAt == nil {
			rec.LastActiveAt = cloneTimePtr(existing.LastActiveAt)
		}
		if rec.LastActivityAt == nil {
			if rec.LastActiveAt != nil {
				rec.LastActivityAt = cloneTimePtr(rec.LastActiveAt)
			} else {
				rec.LastActivityAt = cloneTimePtr(existing.LastActivityAt)
			}
		}
		// Match PostgresStore: preserve awake_since / last_metered_at when already awake,
		// reset them cleanly on sleep->wake transition.
		switch {
		case rec.Status == StatusSleeping || rec.Status == StatusDeleted || rec.Status == StatusError:
			rec.AwakeSince = nil
			if rec.LastMeteredAt == nil {
				rec.LastMeteredAt = cloneTimePtr(existing.LastMeteredAt)
			}
			if rec.Status == StatusSleeping && rec.SleepingSince == nil {
				if existing.SleepingSince != nil {
					rec.SleepingSince = cloneTimePtr(existing.SleepingSince)
				} else {
					t := now
					rec.SleepingSince = &t
				}
			}
		case existing.AwakeSince != nil:
			rec.AwakeSince = cloneTimePtr(existing.AwakeSince)
			if existing.LastMeteredAt != nil {
				rec.LastMeteredAt = cloneTimePtr(existing.LastMeteredAt)
			}
			rec.SleepingSince = nil
		default:
			if isAwakeStatus(rec.Status) {
				rec.SleepingSince = nil
			}
		}
		if rec.ProviderOwner == "" {
			rec.ProviderOwner = existing.ProviderOwner
		}
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	if rec.LastActivityAt == nil {
		if rec.LastActiveAt != nil {
			rec.LastActivityAt = cloneTimePtr(rec.LastActiveAt)
		} else {
			t := now
			rec.LastActivityAt = &t
		}
	}
	if rec.ProviderOwner == "" {
		rec.ProviderOwner = DefaultProviderOwner
	}
	rec.UpdatedAt = now
	s.computers[rec.UserID] = cloneComputerRecord(rec)
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
	now := s.now()
	rec.Status = status
	if lastActive != nil {
		t := lastActive.UTC()
		rec.LastActiveAt = &t
		t2 := t
		rec.LastActivityAt = &t2
	}
	switch status {
	case StatusSleeping:
		rec.AwakeSince = nil
		if rec.SleepingSince == nil {
			t := now
			rec.SleepingSince = &t
		}
	case StatusDeleted:
		rec.AwakeSince = nil
	case StatusError:
		// Preserve awake_since so unmetered awake time is not silently discarded;
		// use MeterAwakeTime(TransitionStatus: StatusError) to meter and clear atomically.
	case StatusReady:
		rec.SleepingSince = nil
		if rec.AwakeSince == nil {
			t := now
			if lastActive != nil {
				t = lastActive.UTC()
			}
			rec.AwakeSince = &t
			t2 := t
			rec.LastMeteredAt = &t2
		}
	case StatusProvisioning, StatusResetting:
		rec.SleepingSince = nil
	}
	rec.UpdatedAt = now
	s.computers[userID] = rec
	return nil
}

func (s *MemoryStore) TouchComputerActivity(_ context.Context, userID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.computers[userID]
	if !ok || rec.Status == StatusDeleting || rec.Status == StatusDeleted {
		return nil
	}
	if at.IsZero() {
		at = s.now()
	}
	t := at.UTC()
	if rec.LastActiveAt == nil || t.After(*rec.LastActiveAt) {
		tCopy := t
		rec.LastActiveAt = &tCopy
	}
	if rec.LastActivityAt == nil || t.After(*rec.LastActivityAt) {
		tCopy := t
		rec.LastActivityAt = &tCopy
	}
	s.computers[userID] = rec
	return nil
}

func (s *MemoryStore) CountAwakeComputers(_ context.Context, excludeUserID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.countAwakeLocked(excludeUserID), nil
}

func (s *MemoryStore) ListAwakeComputers(_ context.Context, limit int) ([]ComputerRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ComputerRecord
	for _, rec := range s.computers {
		if isAwakeStatus(rec.Status) || (rec.AwakeSince != nil && rec.Status != StatusSleeping && rec.Status != StatusDeleted) {
			out = append(out, cloneComputerRecord(rec))
		}
	}
	slices.SortFunc(out, func(a, b ComputerRecord) int {
		at, bt := a.UpdatedAt, b.UpdatedAt
		if a.LastActivityAt != nil {
			at = *a.LastActivityAt
		}
		if b.LastActivityAt != nil {
			bt = *b.LastActivityAt
		}
		if at.Before(bt) {
			return -1
		}
		if at.After(bt) {
			return 1
		}
		return strings.Compare(a.UserID, b.UserID)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// MeterAwakeTime mirrors PostgresStore.MeterAwakeTime atomically under s.mu so concurrent
// janitor passes and Sleep calls never double-bill in unit tests or DSTs.
func (s *MemoryStore) MeterAwakeTime(ctx context.Context, req MeterAwakeRequest) (MeterAwakeResult, error) {
	now := req.Now.UTC()
	if now.IsZero() {
		now = s.now()
	}

	s.mu.Lock()
	rec, ok := s.computers[req.UserID]
	if !ok {
		s.mu.Unlock()
		return MeterAwakeResult{}, ErrNotFound
	}
	origRec := cloneComputerRecord(rec)

	isAwake := rec.AwakeSince != nil && rec.Status != StatusSleeping && rec.Status != StatusDeleted
	var start, end time.Time
	var elapsed time.Duration
	var costMicros int64
	var costUSD float64
	if isAwake {
		start = rec.AwakeSince.UTC()
		if rec.LastMeteredAt != nil && rec.LastMeteredAt.UTC().After(start) {
			start = rec.LastMeteredAt.UTC()
		}
		end = now
		if req.MeterEndAt != nil && !req.MeterEndAt.IsZero() && req.MeterEndAt.UTC().Before(end) {
			end = req.MeterEndAt.UTC()
		}
		if end.After(start) {
			elapsed = end.Sub(start)
			isPlatform := rec.ProviderOwner == "" || rec.ProviderOwner == DefaultProviderOwner
			if isPlatform && req.RatePerHourUSD > 0 {
				costMicros = billing.USDToMicros((elapsed.Seconds() / 3600.0) * req.RatePerHourUSD)
				costUSD = billing.MicrosToUSD(costMicros)
			}
		}
	}

	switch req.TransitionStatus {
	case StatusSleeping:
		if rec.Status != StatusDeleting && rec.Status != StatusDeleted {
			rec.Status = StatusSleeping
		}
		rec.AwakeSince = nil
		sleepAt := now
		rec.SleepingSince = &sleepAt
		if isAwake && end.After(start) {
			mAt := end
			rec.LastMeteredAt = &mAt
		} else if rec.LastMeteredAt == nil {
			mAt := now
			rec.LastMeteredAt = &mAt
		}
	case StatusDeleting, StatusDeleted:
		rec.Status = req.TransitionStatus
		rec.AwakeSince = nil
		if isAwake && end.After(start) {
			mAt := end
			rec.LastMeteredAt = &mAt
		}
	case StatusError:
		if rec.Status != StatusDeleting && rec.Status != StatusDeleted {
			rec.Status = StatusError
		}
		rec.AwakeSince = nil
		if isAwake && end.After(start) {
			mAt := end
			rec.LastMeteredAt = &mAt
		}
	default:
		if req.TransitionStatus != "" && rec.Status != StatusDeleting && rec.Status != StatusDeleted {
			rec.Status = req.TransitionStatus
		}
		if isAwake && costMicros > 0 {
			mAt := end
			rec.LastMeteredAt = &mAt
		}
	}
	rec.UpdatedAt = now

	var usageRow *state.Usage
	if costMicros > 0 {
		usageID := strings.TrimSpace(req.UsageID)
		if usageID == "" {
			usageID = "sprites_" + rec.ComputerID + "_" + uuid.NewString()
		}
		if !s.usageIDs[usageID] {
			costVal := costUSD
			u := state.Usage{
				ID:            usageID,
				InputID:       rec.ComputerID,
				Provider:      "sprites",
				Model:         "sprites-compute",
				Billing:       state.UsageBillingHosted,
				Category:      state.UsageCategoryComputer,
				CostUSD:       &costVal,
				CostStatus:    "calculated",
				RequestStatus: "completed",
				CreatedAt:     now,
			}
			if s.OnMeterUsage != nil {
				if err := s.OnMeterUsage(ctx, rec.UserID, u, costMicros); err != nil {
					// Roll back in-memory state cleanly on error, mirroring SQL tx rollback.
					s.computers[req.UserID] = origRec
					s.mu.Unlock()
					return MeterAwakeResult{}, err
				}
			}
			s.usageIDs[usageID] = true
			s.usageLog[rec.UserID] = append(s.usageLog[rec.UserID], u)
			usageRow = &u
		}
	}

	s.computers[req.UserID] = cloneComputerRecord(rec)
	outRec := cloneComputerRecord(rec)
	s.mu.Unlock()

	return MeterAwakeResult{
		Record:     outRec,
		Metered:    usageRow != nil,
		Elapsed:    elapsed,
		CostUSD:    costUSD,
		CostMicros: costMicros,
		Usage:      usageRow,
	}, nil
}

func (s *MemoryStore) MarkComputerDeleting(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if rec, ok := s.computers[userID]; ok && rec.Status != StatusDeleted {
		rec.Status = StatusDeleting
		rec.AwakeSince = nil
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
		rec.AwakeSince = nil
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
			out = append(out, cloneComputerRecord(rec))
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

func (s *MemoryStore) ConsumeSessionToken(_ context.Context, userID, sessionID string, tokenHash, consumedHash []byte, now time.Time) (SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.IsZero() {
		now = s.now()
	}
	now = now.UTC()
	sess, ok := s.sessions[sessionID]
	if !ok || sess.UserID != userID {
		return SessionRecord{}, ErrNotFound
	}
	if sess.EndedAt != nil {
		return SessionRecord{}, ErrSessionRevoked
	}
	if !sess.ExpiresAt.After(now) {
		t := now
		sess.EndedAt = &t
		s.sessions[sessionID] = sess
		return SessionRecord{}, ErrSessionExpired
	}
	if !bytes.Equal(sess.TokenHash, tokenHash) {
		return SessionRecord{}, ErrSessionTokenUsed
	}
	sess.TokenHash = append([]byte(nil), consumedHash...)
	sess.LastActivityAt = now
	s.sessions[sessionID] = sess
	sess.TokenHash = append([]byte(nil), sess.TokenHash...)
	return sess, nil
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
	act := activityAt.UTC()
	sess.LastActivityAt = act
	maxExp := sess.CreatedAt.UTC().Add(DefaultDesktopWSMaxLifetime)
	targetExp := newExpiresAt.UTC()
	if targetExp.After(maxExp) {
		targetExp = maxExp
	}
	if targetExp.After(sess.ExpiresAt) {
		sess.ExpiresAt = targetExp
	}
	s.sessions[sessionID] = sess
	if rec, ok := s.computers[sess.UserID]; ok && rec.Status != StatusDeleting && rec.Status != StatusDeleted {
		if rec.LastActiveAt == nil || act.After(*rec.LastActiveAt) {
			t := act
			rec.LastActiveAt = &t
		}
		if rec.LastActivityAt == nil || act.After(*rec.LastActivityAt) {
			t := act
			rec.LastActivityAt = &t
		}
		s.computers[sess.UserID] = rec
	}
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
