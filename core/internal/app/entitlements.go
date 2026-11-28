package app

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"
	"nori/internal/billing"
	"nori/internal/scheduler"
	"nori/internal/state/docstore"
)

// DefaultEntitlementCacheTTL is the fallback TTL on the in-memory entitlement cache
// in case a Postgres LISTEN/NOTIFY 'entitlement:<uid>' event is dropped.
const DefaultEntitlementCacheTTL = 15 * time.Second

type cachedEntitlement struct {
	ent       billing.Entitlement
	missing   bool
	expiresAt time.Time
}

func (s *Service) entitlementCacheTTL() time.Duration {
	if s != nil && s.EntitlementCacheTTL > 0 {
		return s.EntitlementCacheTTL
	}
	return DefaultEntitlementCacheTTL
}

// Billing returns the service's billing.Service (nil when no billing-capable backend is attached).
func (s *Service) Billing() *billing.Service {
	if s == nil {
		return nil
	}
	return s.billing
}

// SetBilling overrides the service's billing.Service (primarily for tests).
func (s *Service) SetBilling(b *billing.Service) {
	if s == nil {
		return
	}
	s.billing = b
	s.EvictEntitlementCache("")
}

// SetDisconnectDesktop registers an optional hook invoked on entitlement suspension/changes
// to immediately close open Desktop VNC WebSocket sessions for userID.
func (s *Service) SetDisconnectDesktop(fn func(userID string)) {
	if s == nil {
		return
	}
	s.hooksMu.Lock()
	s.disconnectDesktop = fn
	s.hooksMu.Unlock()
}

// SetSleepUserComputer registers an optional hook invoked on account suspension
// to immediately sleep userID's Sprite computer.
func (s *Service) SetSleepUserComputer(fn func(ctx context.Context, userID string) error) {
	if s == nil {
		return
	}
	s.hooksMu.Lock()
	s.sleepUserComputer = fn
	s.hooksMu.Unlock()
}

// SetCancelUserWork registers a hook (wired automatically by worker.Pool) that cancels
// active in-flight conversation/task/scheduler work for userID on this instance.
func (s *Service) SetCancelUserWork(fn func(userID string)) {
	if s == nil {
		return
	}
	s.hooksMu.Lock()
	s.cancelUserWork = fn
	s.hooksMu.Unlock()
}

// SetOnEntitlementChanged registers an optional callback invoked after local caches/DEKs/sessions
// are evicted for userID.
func (s *Service) SetOnEntitlementChanged(fn func(ctx context.Context, userID string)) {
	if s == nil {
		return
	}
	s.hooksMu.Lock()
	s.onEntitlementChanged = fn
	s.hooksMu.Unlock()
}

// EvictEntitlementCache drops the cached entitlement entry for userID (or all entries if userID == "").
func (s *Service) EvictEntitlementCache(userID string) {
	if s == nil {
		return
	}
	s.entMu.Lock()
	if userID == "" {
		clear(s.entCache)
	} else {
		delete(s.entCache, userID)
	}
	s.entMu.Unlock()
}

// GetEntitlement returns the user's entitlement using the bounded 15s fallback cache.
// Missing entitlements fail closed with billing.ErrEntitlementMissing.
func (s *Service) GetEntitlement(ctx context.Context, userID string) (billing.Entitlement, error) {
	if s == nil || s.billing == nil {
		return billing.Entitlement{}, billing.ErrEntitlementMissing
	}
	now := s.now().UTC()
	s.entMu.RLock()
	entry, ok := s.entCache[userID]
	s.entMu.RUnlock()
	if ok && now.Before(entry.expiresAt) {
		if entry.missing {
			return billing.Entitlement{}, billing.ErrEntitlementMissing
		}
		return entry.ent.Clone(), nil
	}

	ent, err := s.billing.GetEntitlement(ctx, userID)
	if errors.Is(err, billing.ErrEntitlementMissing) {
		s.entMu.Lock()
		if s.entCache == nil {
			s.entCache = map[string]cachedEntitlement{}
		}
		s.entCache[userID] = cachedEntitlement{missing: true, expiresAt: now.Add(s.entitlementCacheTTL())}
		s.entMu.Unlock()
		return billing.Entitlement{}, billing.ErrEntitlementMissing
	}
	if err != nil {
		return billing.Entitlement{}, err
	}
	s.entMu.Lock()
	if s.entCache == nil {
		s.entCache = map[string]cachedEntitlement{}
	}
	s.entCache[userID] = cachedEntitlement{ent: ent.Clone(), expiresAt: now.Add(s.entitlementCacheTTL())}
	s.entMu.Unlock()
	return ent, nil
}

// CheckNotSuspended verifies the user has an entitlement row and is not suspended.
// Returns billing.ErrEntitlementMissing or billing.ErrAccountSuspended (both map to 403).
// When billing is not configured on this Service (e.g. legacy storage-less unit tests), returns nil error.
func (s *Service) CheckNotSuspended(ctx context.Context, userID string) (billing.Entitlement, error) {
	if s == nil || s.billing == nil {
		return billing.Entitlement{}, nil
	}
	ent, err := s.GetEntitlement(ctx, userID)
	if err != nil {
		return billing.Entitlement{}, err
	}
	if ent.IsSuspended() {
		return ent, billing.ErrAccountSuspended
	}
	return ent, nil
}

// IsUserSuspended reports whether userID is suspended (or missing an entitlement when billing is active).
func (s *Service) IsUserSuspended(ctx context.Context, userID string) bool {
	if s == nil || s.billing == nil {
		return false
	}
	ent, err := s.GetEntitlement(ctx, userID)
	if err != nil {
		return errors.Is(err, billing.ErrAccountSuspended) || errors.Is(err, billing.ErrEntitlementMissing)
	}
	return ent.IsSuspended()
}

// CheckHomeAIAccess verifies userID is neither suspended nor disallowed from Home AI (e.g. trial tier).
func (s *Service) CheckHomeAIAccess(ctx context.Context, userID string) error {
	if s == nil || s.billing == nil {
		return nil
	}
	ent, err := s.GetEntitlement(ctx, userID)
	if err != nil {
		return err
	}
	if err := ent.CheckAccess(s.now().UTC()); err != nil {
		return err
	}
	if !ent.EffectivePolicy().AllowHomeAI {
		return billing.ErrBYOKNotAllowed
	}
	return nil
}

// HandleEntitlementChanged processes a cross-instance 'entitlement:<uid>' notification:
//  1. Evicts entitlement cache + cached userApp entry.
//  2. Evicts rowstore & tenantkey DEK cache entries for userID.
//  3. Disconnects open Home AI sockets, invokes optional Desktop VNC disconnect & Sprite sleep hooks,
//     and cancels active in-flight worker tasks/conversations for suspended/disallowed users.
func (s *Service) HandleEntitlementChanged(ctx context.Context, userID string) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// 1. Evict entitlement cache and cached userApp.
	s.EvictEntitlementCache(userID)
	if userID != "" {
		s.evictUser(userID)
	}

	// 2. Evict rowstore & tenantkey DEKs immediately.
	if s.storage != nil {
		s.storage.EvictUserDEKs(userID)
	}

	s.hooksMu.RLock()
	disconnectDesktop := s.disconnectDesktop
	sleepComputer := s.sleepUserComputer
	cancelWork := s.cancelUserWork
	onChanged := s.onEntitlementChanged
	s.hooksMu.RUnlock()

	// 3. Re-read fresh entitlement to decide whether active sessions/work must be terminated.
	if userID != "" && s.billing != nil {
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ent, err := s.GetEntitlement(checkCtx, userID)
		cancel()

		now := s.now().UTC()
		suspendedOrMissing := errors.Is(err, billing.ErrEntitlementMissing) || (err == nil && ent.IsSuspended())
		inactive := err == nil && !ent.IsActiveAt(now)
		homeDisallowed := err == nil && !ent.EffectivePolicy().AllowHomeAI

		if suspendedOrMissing || inactive || homeDisallowed {
			if s.home != nil {
				s.home.DisconnectUser(userID)
			}
		}
		if suspendedOrMissing || inactive {
			if disconnectDesktop != nil {
				disconnectDesktop(userID)
			}
			if cancelWork != nil {
				cancelWork(userID)
			}
			if sleepComputer != nil {
				sleepCtx, sleepCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				if sleepErr := sleepComputer(sleepCtx, userID); sleepErr != nil {
					slog.WarnContext(sleepCtx, "sleep user computer after entitlement change failed", "user_id", userID, "err", sleepErr)
				}
				sleepCancel()
			}
		}
	}

	if onChanged != nil {
		onChanged(ctx, userID)
	}
}

// StartEntitlementListener runs a dedicated LISTEN nori_wake loop for instances that do
// not run worker.Pool (e.g. NORI_ROLE=api) so entitlement changes still propagate immediately.
func (s *Service) StartEntitlementListener(ctx context.Context) error {
	listener := s.WakeListener()
	if listener == nil {
		return nil
	}
	return listener.ListenWakes(ctx, func(ev docstore.WakeEvent) {
		parsed := docstore.ParseWakePayload(ev.Payload)
		if parsed.Kind == docstore.WakeKindEntitlement {
			s.HandleEntitlementChanged(ctx, parsed.UserID)
		}
	})
}

// PauseExcessRoutines auto-pauses enabled non-system routines above maxRoutines,
// pausing the newest routines first (CreatedAt DESC, then ID DESC).
func PauseExcessRoutines(ctx context.Context, sched *scheduler.Service, maxRoutines int) (int, error) {
	if sched == nil || maxRoutines < 0 {
		return 0, nil
	}
	items, err := sched.List(ctx, string(scheduler.AutomationEnabled))
	if err != nil {
		return 0, err
	}
	eligible := make([]scheduler.Automation, 0, len(items))
	for _, item := range items {
		if item.System {
			continue
		}
		eligible = append(eligible, item)
	}
	if len(eligible) <= maxRoutines {
		return 0, nil
	}
	// Sort newest first so excess newest routines are paused first.
	sort.SliceStable(eligible, func(i, j int) bool {
		var ti, tj time.Time
		if eligible[i].CreatedAt != nil {
			ti = eligible[i].CreatedAt.UTC()
		}
		if eligible[j].CreatedAt != nil {
			tj = eligible[j].CreatedAt.UTC()
		}
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return eligible[i].ID > eligible[j].ID
	})

	excess := eligible[:len(eligible)-maxRoutines]
	paused := 0
	var errs []error
	for _, auto := range excess {
		reqID := uuid.NewString()
		if _, err := sched.Act(ctx, auto.ID, reqID, auto.Revision, scheduler.Action{Action: "pause"}); err != nil {
			errs = append(errs, err)
			continue
		}
		paused++
	}
	return paused, errors.Join(errs...)
}

// EnforceRoutineCap enforces the user's current tier MaxRoutines cap by auto-pausing
// excess enabled non-system routines (newest disabled first).
func (s *Service) EnforceRoutineCap(ctx context.Context, userID string) (int, error) {
	if s == nil || s.billing == nil {
		return 0, nil
	}
	ent, err := s.billing.GetEntitlement(ctx, userID)
	if err != nil {
		return 0, err
	}
	userApp, release, err := s.AcquireByID(ctx, userID)
	if err != nil {
		return 0, err
	}
	defer release()
	return PauseExcessRoutines(ctx, userApp.Scheduler, ent.EffectivePolicy().MaxRoutines)
}
