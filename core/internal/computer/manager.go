package computer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"nori/internal/billing"
	"nori/internal/computer/vmscripts"
	"nori/internal/observability"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/users"
)

// Config controls computer provisioning, limits, metering, janitor cadence, and desktop session lifetimes.
type Config struct {
	DisplayWidth    int
	DisplayHeight   int
	MemoryLimitMB   int
	MaxDiskGB       int
	SessionTTL      time.Duration
	SessionIdleCap  time.Duration
	BootstrapVer    string
	MaxFleet        int           // 0 defaults to DefaultMaxFleet (10); < 0 disables cap
	RatePerHourUSD  float64       // 0 defaults to DefaultSpritesRatePerHourUSD ($0.15/hr) unless DisableMetering is true
	DisableMetering bool          // true for local Docker provider ($0 cloud cost)
	IdleAutoSleep   time.Duration // default DefaultIdleAutoSleep (15m)
	JanitorInterval time.Duration // default DefaultJanitorInterval (1m)
}

func (c Config) withDefaults() Config {
	if c.DisplayWidth <= 0 {
		c.DisplayWidth = DefaultDisplayWidth
	}
	if c.DisplayHeight <= 0 {
		c.DisplayHeight = DefaultDisplayHeight
	}
	if c.MemoryLimitMB <= 0 {
		c.MemoryLimitMB = DefaultMemoryLimitMB
	}
	if c.MaxDiskGB <= 0 {
		c.MaxDiskGB = DefaultMaxDiskGB
	}
	if c.SessionTTL <= 0 {
		c.SessionTTL = DefaultSessionTTL
	}
	if c.SessionIdleCap <= 0 {
		c.SessionIdleCap = DefaultSessionIdleCap
	}
	if c.BootstrapVer == "" {
		c.BootstrapVer = vmscripts.Version
	}
	if c.MaxFleet == 0 {
		c.MaxFleet = DefaultMaxFleet
	}
	if !c.DisableMetering && c.RatePerHourUSD <= 0 {
		c.RatePerHourUSD = DefaultSpritesRatePerHourUSD
	}
	if c.IdleAutoSleep <= 0 {
		c.IdleAutoSleep = DefaultIdleAutoSleep
	}
	if c.JanitorInterval <= 0 {
		c.JanitorInterval = DefaultJanitorInterval
	}
	return c
}

// StatusView is the user-facing summary returned by Manager.Status.
type StatusView struct {
	ComputerID           string           `json:"computer_id,omitempty"`
	Provider             string           `json:"provider"`
	Status               Status           `json:"status"`
	BaselineCheckpointID string           `json:"baseline_checkpoint_id,omitempty"`
	DiskUsedBytes        int64            `json:"disk_used_bytes,omitempty"`
	DiskMaxBytes         int64            `json:"disk_max_bytes,omitempty"`
	Checkpoints          []CheckpointInfo `json:"checkpoints,omitempty"`
	ActiveSessions       []SessionRecord  `json:"active_sessions,omitempty"`
	LastActiveAt         *time.Time       `json:"last_active_at,omitempty"`
}

// CreatedSession is returned when minting a short-lived desktop watch/takeover session.
// RawToken is returned once to the caller and never stored in plaintext.
type CreatedSession struct {
	Session  SessionRecord
	RawToken string
}

// SessionEvent notifies active desktop WebSocket proxies synchronously of mode changes or revocation.
type SessionEvent struct {
	Mode    SessionMode
	Revoked bool
}

const (
	ensureCacheTTL      = 2 * time.Minute
	statusCacheTTL      = 5 * time.Second
	eventualStatusGrace = 30 * time.Second
	janitorLeaderUserID = "__computer_janitor__"
	janitorLeaderLane   = "computer_janitor"
)

type ensuredComputer struct {
	providerID   string
	bootstrapVer string
	at           time.Time
}

type cachedInstanceStatus struct {
	providerID string
	live       InstanceStatus
	at         time.Time
}

type inflightStatus struct {
	done chan struct{}
	live InstanceStatus
	err  error
}

// DesktopCloseReason identifies why the server initiated closing an active Desktop VNC WebSocket.
type DesktopCloseReason int

const (
	DesktopCloseSessionRevoked DesktopCloseReason = iota // WS close 4004
	DesktopCloseMaxLifetime                              // WS close 4001
	DesktopCloseIdleSleeping                             // WS close 4002
	DesktopCloseEntitlement                              // WS close 4003
)

// Manager coordinates lifecycle, policy, cost attribution, sessions, file links,
// awake-time metering, idle auto-sleep, and account-deletion teardown across any computer.Provider.
type Manager struct {
	provider Provider
	store    Store
	cfg      Config
	Now      func() time.Time

	instanceID string

	mu             sync.Mutex
	billingSvc     *billing.Service
	usageBackend   docstore.Backend
	leases         docstore.LeaseStore
	janitorStarted bool
	locks          map[string]*sync.Mutex
	ensured        map[string]ensuredComputer
	statusCache    map[string]cachedInstanceStatus
	statusInflight map[string]*inflightStatus
	statusGen      map[string]uint64
	listeners      map[string]map[uint64]func(SessionEvent)
	desktopCancels map[string]map[uint64]func(DesktopCloseReason)
	nextSubID      uint64
}

// NewManager creates a Manager backed by provider and store.
func NewManager(provider Provider, store Store, cfg Config) *Manager {
	host, _ := os.Hostname()
	if host == "" {
		host = "nori"
	}
	return &Manager{
		provider:       provider,
		store:          store,
		cfg:            cfg.withDefaults(),
		instanceID:     host + "-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8],
		locks:          map[string]*sync.Mutex{},
		ensured:        map[string]ensuredComputer{},
		statusCache:    map[string]cachedInstanceStatus{},
		statusInflight: map[string]*inflightStatus{},
		statusGen:      map[string]uint64{},
		listeners:      map[string]map[uint64]func(SessionEvent){},
		desktopCancels: map[string]map[uint64]func(DesktopCloseReason){},
	}
}

// SetBilling attaches the billing.Service used for fail-closed access/credit checks,
// per-tier checkpoint limits, and exhausted/suspended Sprite reaping.
func (m *Manager) SetBilling(svc *billing.Service) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.billingSvc = svc
	m.mu.Unlock()
	m.syncMemoryMeterHook()
}

// Billing returns the currently wired billing.Service (may be nil).
func (m *Manager) Billing() *billing.Service {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.billingSvc
}

// SetUsageBackend attaches a docstore.Backend used by in-memory stores/DSTs to persist
// finalized Sprite awake-time usage records alongside global budget accounting.
// PostgresStore writes usage records and nori_global_budget_daily directly inside its SQL transaction.
func (m *Manager) SetUsageBackend(backend docstore.Backend) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.usageBackend = backend
	m.mu.Unlock()
	m.syncMemoryMeterHook()
}

// SetLeases attaches a docstore.LeaseStore for multi-instance janitor leader election.
func (m *Manager) SetLeases(leases docstore.LeaseStore) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.leases = leases
	m.mu.Unlock()
}

// SetClock sets the deterministic clock used by Manager (and syncs MemoryStore if backed by one).
func (m *Manager) SetClock(now func() time.Time) {
	if m == nil {
		return
	}
	m.Now = now
	if ms, ok := m.store.(*MemoryStore); ok {
		ms.Now = now
	}
}

func (m *Manager) syncMemoryMeterHook() {
	ms, ok := m.store.(*MemoryStore)
	if !ok {
		return
	}
	m.mu.Lock()
	backend := m.usageBackend
	bsvc := m.billingSvc
	m.mu.Unlock()
	if backend == nil && (bsvc == nil || bsvc.Store == nil) {
		return
	}
	ms.OnMeterUsage = func(ctx context.Context, userID string, u state.Usage, costMicros int64) error {
		reconciled := false
		if backend != nil {
			fields, err := docstore.Encode(u)
			if err != nil {
				return err
			}
			col := "owners/" + userID + "/model_usage"
			client := docstore.New(backend)
			var errRec error
			reconciled, errRec = client.RecordAndReconcileUsage(ctx, docstore.UsageReconcileRequest{
				Collection:           col,
				UsageID:              u.ID,
				UsageFields:          fields,
				Billing:              u.Billing,
				Category:             u.Category,
				CostStatus:           u.CostStatus,
				EstimatedExposureUSD: u.EstimatedExposureUSD,
				CostUSD:              u.CostUSD,
				RequestStatus:        u.RequestStatus,
				CreatedAt:            u.CreatedAt,
			})
			if errRec != nil {
				return errRec
			}
			if !reconciled {
				if _, err := client.Collection(col).Doc(u.ID).Set(ctx, u); err != nil {
					return err
				}
			}
		}
		if !reconciled && bsvc != nil && bsvc.Store != nil && costMicros > 0 {
			if err := bsvc.Store.CheckAndIncrementGlobalBudget(ctx, u.CreatedAt, billing.ScopeHostedAll, costMicros, 0, 1, 0); err != nil {
				return err
			}
			if err := bsvc.Store.CheckAndIncrementGlobalBudget(ctx, u.CreatedAt, billing.ScopeHostedSprites, costMicros, 0, 1, 0); err != nil {
				return err
			}
		}
		return nil
	}
}

func (m *Manager) effectiveMaxFleet() int {
	if m == nil || m.cfg.MaxFleet < 0 {
		return 0
	}
	return m.cfg.MaxFleet
}

func (m *Manager) effectiveRatePerHour() float64 {
	if m == nil || m.cfg.DisableMetering || m.cfg.RatePerHourUSD <= 0 {
		return 0
	}
	return m.cfg.RatePerHourUSD
}

// MeteringDisabled reports whether compute metering / credit balance gating is disabled on this manager.
func (m *Manager) MeteringDisabled() bool {
	return m != nil && m.cfg.DisableMetering
}

// StoredComputerStatus returns the persisted computer lifecycle status for userID without contacting the provider.
func (m *Manager) StoredComputerStatus(ctx context.Context, userID string) (Status, error) {
	if !m.Enabled() {
		return "", ErrDisabled
	}
	rec, err := m.store.GetComputer(ctx, userID)
	if err != nil {
		return "", err
	}
	return rec.Status, nil
}

func (m *Manager) markError(ctx context.Context, userID string) {
	if !m.Enabled() {
		return
	}
	res, err := m.store.MeterAwakeTime(ctx, MeterAwakeRequest{
		UserID:           userID,
		Now:              m.now(),
		RatePerHourUSD:   m.effectiveRatePerHour(),
		TransitionStatus: StatusError,
	})
	if err == nil {
		emitMeteredUsage(ctx, userID, res)
		return
	}
	_ = m.store.UpdateComputerStatus(ctx, userID, StatusError, nil)
}

func (m *Manager) SessionIdleCap() time.Duration {
	if m == nil {
		return DefaultSessionIdleCap
	}
	return m.cfg.SessionIdleCap
}

// RegisterDesktopSession tracks an active Desktop VNC WebSocket cancel function for userID
// so CloseDesktopSessions(userID) can immediately terminate live sockets on suspension/sleep.
func (m *Manager) RegisterDesktopSession(userID string, cancel func()) func() {
	if m == nil || cancel == nil {
		return func() {}
	}
	return m.RegisterDesktopSessionWithReason(userID, func(DesktopCloseReason) { cancel() })
}

// RegisterDesktopSessionWithReason tracks an active Desktop VNC WebSocket close callback for userID
// that receives the server-initiated DesktopCloseReason so the WS proxy can send the matching 400x close code.
func (m *Manager) RegisterDesktopSessionWithReason(userID string, closeFn func(DesktopCloseReason)) func() {
	if m == nil || closeFn == nil {
		return func() {}
	}
	m.mu.Lock()
	m.nextSubID++
	id := m.nextSubID
	if m.desktopCancels == nil {
		m.desktopCancels = map[string]map[uint64]func(DesktopCloseReason){}
	}
	if m.desktopCancels[userID] == nil {
		m.desktopCancels[userID] = map[uint64]func(DesktopCloseReason){}
	}
	m.desktopCancels[userID][id] = closeFn
	m.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if subs := m.desktopCancels[userID]; subs != nil {
				delete(subs, id)
				if len(subs) == 0 {
					delete(m.desktopCancels, userID)
				}
			}
			m.mu.Unlock()
		})
	}
}

// CloseDesktopSessions immediately closes all active Desktop VNC WebSocket connections for userID
// and returns the number of connections signalled. Safe to call from entitlement:<uid> notify handlers.
func (m *Manager) CloseDesktopSessions(userID string) int {
	return m.CloseDesktopSessionsWithReason(userID, DesktopCloseSessionRevoked)
}

// CloseDesktopSessionsWithReason immediately closes all active Desktop VNC WebSocket connections
// for userID with an explicit server-initiated close reason.
func (m *Manager) CloseDesktopSessionsWithReason(userID string, reason DesktopCloseReason) int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	subs := m.desktopCancels[userID]
	delete(m.desktopCancels, userID)
	cancels := make([]func(DesktopCloseReason), 0, len(subs))
	for _, fn := range subs {
		cancels = append(cancels, fn)
	}
	m.mu.Unlock()
	for _, fn := range cancels {
		fn(reason)
	}
	return len(cancels)
}

// SubscribeSession registers a synchronous callback notified BEFORE SetSessionMode or EndSession returns.
func (m *Manager) SubscribeSession(sessionID string, fn func(SessionEvent)) func() {
	m.mu.Lock()
	m.nextSubID++
	id := m.nextSubID
	if m.listeners[sessionID] == nil {
		m.listeners[sessionID] = map[uint64]func(SessionEvent){}
	}
	m.listeners[sessionID][id] = fn
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		if subs := m.listeners[sessionID]; subs != nil {
			delete(subs, id)
			if len(subs) == 0 {
				delete(m.listeners, sessionID)
			}
		}
		m.mu.Unlock()
	}
}

func (m *Manager) notifySession(sessionID string, ev SessionEvent) {
	m.mu.Lock()
	subs := make([]func(SessionEvent), 0, len(m.listeners[sessionID]))
	for _, fn := range m.listeners[sessionID] {
		subs = append(subs, fn)
	}
	m.mu.Unlock()
	for _, fn := range subs {
		fn(ev)
	}
}

func (m *Manager) notifyUserSessionsRevoked(ctx context.Context, userID string) {
	if sessions, err := m.store.ListActiveSessions(ctx, userID); err == nil {
		for _, s := range sessions {
			m.notifySession(s.ID, SessionEvent{Mode: SessionModeView, Revoked: true})
		}
	}
}

// hasActiveControlSession reports whether the user currently holds an active interactive control lease.
func (m *Manager) hasActiveControlSession(ctx context.Context, userID string) bool {
	sessions, err := m.store.ListActiveSessions(ctx, userID)
	if err != nil {
		return false
	}
	now := m.now()
	for _, s := range sessions {
		if s.EndedAt == nil && s.Mode == SessionModeControl && s.ExpiresAt.After(now) && now.Sub(s.LastActivityAt) <= m.cfg.SessionIdleCap {
			return true
		}
	}
	return false
}

func expectedProviderID(providerName, computerID string) string {
	clean := strings.ToLower(strings.TrimSpace(computerID))
	clean = strings.ReplaceAll(clean, "_", "-")
	switch providerName {
	case "sprites":
		if strings.HasPrefix(clean, "nori-") {
			return clean
		}
		return "nori-" + clean
	case "docker":
		if strings.HasPrefix(clean, "nori-computer-") {
			return clean
		}
		return "nori-computer-" + clean
	default:
		return computerID
	}
}

func (m *Manager) Enabled() bool {
	return m != nil && m.provider != nil && m.store != nil
}

func (m *Manager) ProviderName() string {
	if !m.Enabled() {
		return "disabled"
	}
	return m.provider.Name()
}

func (m *Manager) now() time.Time {
	if m != nil && m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m *Manager) userLock(userID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.locks[userID]
	if l == nil {
		l = &sync.Mutex{}
		m.locks[userID] = l
	}
	return l
}

func (m *Manager) isRecentlyEnsured(userID, providerID, bootstrapVer string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.ensured[userID]
	if !ok || entry.providerID != providerID || entry.bootstrapVer != bootstrapVer {
		return false
	}
	return m.now().Sub(entry.at) < ensureCacheTTL
}

func (m *Manager) markEnsured(userID, providerID, bootstrapVer string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ensured == nil {
		m.ensured = map[string]ensuredComputer{}
	}
	m.ensured[userID] = ensuredComputer{
		providerID:   providerID,
		bootstrapVer: bootstrapVer,
		at:           m.now(),
	}
}

func (m *Manager) invalidateEnsure(userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.ensured, userID)
}

func (m *Manager) invalidateStatus(userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.statusCache, userID)
	if m.statusGen == nil {
		m.statusGen = map[string]uint64{}
	}
	m.statusGen[userID]++
}

func (m *Manager) cachedInstanceStatus(ctx context.Context, userID, providerID string) (InstanceStatus, error) {
	now := m.now()
	m.mu.Lock()
	if entry, ok := m.statusCache[userID]; ok && entry.providerID == providerID && now.Sub(entry.at) < statusCacheTTL {
		m.mu.Unlock()
		return entry.live, nil
	}
	if in, ok := m.statusInflight[userID]; ok {
		m.mu.Unlock()
		select {
		case <-in.done:
			return in.live, in.err
		case <-ctx.Done():
			return InstanceStatus{}, ctx.Err()
		}
	}
	if m.statusInflight == nil {
		m.statusInflight = map[string]*inflightStatus{}
	}
	in := &inflightStatus{done: make(chan struct{})}
	gen := m.statusGen[userID]
	m.statusInflight[userID] = in
	m.mu.Unlock()

	live, err := m.provider.Status(ctx, providerID)

	m.mu.Lock()
	in.live, in.err = live, err
	delete(m.statusInflight, userID)
	if err == nil && m.statusGen[userID] == gen {
		if m.statusCache == nil {
			m.statusCache = map[string]cachedInstanceStatus{}
		}
		m.statusCache[userID] = cachedInstanceStatus{
			providerID: providerID,
			live:       live,
			at:         m.now(),
		}
	}
	close(in.done)
	m.mu.Unlock()
	return live, err
}

func shouldInvalidateEnsureOnErr(err error) bool {
	if err == nil {
		return false
	}
	return !errors.Is(err, ErrNotFound) &&
		!errors.Is(err, ErrFileTooLarge) &&
		!errors.Is(err, ErrPathTraversal) &&
		!errors.Is(err, ErrTimeout)
}

// checkHostedAccess enforces fail-closed billing access (suspension, subscription status,
// hosted credit balance > 0, and fleet-wide daily hosted budget) whenever billingSvc is wired.
func (m *Manager) checkHostedAccess(ctx context.Context, userID string, now time.Time) (billing.Entitlement, error) {
	bsvc := m.Billing()
	if bsvc == nil {
		return billing.Entitlement{}, nil
	}
	ent, err := bsvc.GetEntitlement(ctx, userID)
	if err != nil {
		return billing.Entitlement{}, err
	}
	if err := ent.CheckAccess(now); err != nil {
		return ent, err
	}
	if m.cfg.DisableMetering {
		return ent, nil
	}
	bal, err := bsvc.Store.CreditBalance(ctx, userID)
	if err != nil {
		return ent, err
	}
	if bal.AvailableMicros <= 0 {
		return ent, billing.ErrCreditExhausted
	}
	if bsvc.GlobalBudget.HostedAllMaxMicrosPerDay > 0 {
		used, err := bsvc.Store.GetGlobalBudget(ctx, now, billing.ScopeHostedAll)
		if err == nil && used.SpentMicros >= bsvc.GlobalBudget.HostedAllMaxMicrosPerDay {
			return ent, billing.ErrGlobalBudgetExceeded
		}
	}
	return ent, nil
}

// CheckDesktopAccess verifies whether userID is permitted to open/maintain a Desktop VNC session.
func (m *Manager) CheckDesktopAccess(ctx context.Context, userID string) (billing.Entitlement, error) {
	return m.checkHostedAccess(ctx, userID, m.now())
}

// TouchActivity records active computer usage for idle auto-sleep tracking.
func (m *Manager) TouchActivity(ctx context.Context, userID string) error {
	if !m.Enabled() {
		return ErrDisabled
	}
	return m.store.TouchComputerActivity(ctx, userID, m.now())
}

func emitMeteredUsage(ctx context.Context, userID string, res MeterAwakeResult) {
	if res.Usage == nil {
		return
	}
	var costUSD float64
	if res.Usage.CostUSD != nil && *res.Usage.CostUSD >= 0 {
		costUSD = *res.Usage.CostUSD
	}
	observability.RecordUsage(
		users.WithID(ctx, userID),
		res.Usage.Provider,
		res.Usage.Model,
		res.Usage.Billing,
		res.Usage.Category,
		res.Usage.CostStatus,
		res.Usage.RequestStatus,
		costUSD,
	)
}

// Status returns the current state of the user's computer without provisioning one.
func (m *Manager) Status(ctx context.Context, userID string) (StatusView, error) {
	if !m.Enabled() {
		return StatusView{Provider: "disabled", Status: StatusNotProvisioned}, ErrDisabled
	}
	rec, err := m.store.GetComputer(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return StatusView{
			Provider:     m.provider.Name(),
			Status:       StatusNotProvisioned,
			DiskMaxBytes: int64(m.cfg.MaxDiskGB) << 30,
		}, nil
	}
	if err != nil {
		return StatusView{}, err
	}
	view := StatusView{
		ComputerID:           rec.ComputerID,
		Provider:             rec.Provider,
		Status:               rec.Status,
		BaselineCheckpointID: rec.BaselineCheckpointID,
		DiskMaxBytes:         int64(m.cfg.MaxDiskGB) << 30,
		LastActiveAt:         rec.LastActiveAt,
	}
	if rec.Status == StatusDeleted {
		view.Status = StatusNotProvisioned
		return view, nil
	}
	if rec.Status != StatusDeleting && rec.ProviderID != "" {
		if live, lErr := m.cachedInstanceStatus(ctx, userID, rec.ProviderID); lErr == nil {
			if live.Status != "" {
				recentTransition := !rec.UpdatedAt.IsZero() && m.now().Sub(rec.UpdatedAt) < eventualStatusGrace
				switch {
				case live.Standby && rec.Status == StatusSleeping:
					view.Status = StatusSleeping
				case live.EventuallyConsistent && recentTransition && rec.Status == StatusReady && live.Status == StatusSleeping:
					view.Status = StatusReady
				case live.EventuallyConsistent && recentTransition && rec.Status == StatusSleeping && live.Status == StatusReady:
					view.Status = StatusSleeping
				default:
					view.Status = live.Status
				}
			}
			view.DiskUsedBytes = live.DiskUsedBytes
			if live.DiskMaxBytes > 0 {
				view.DiskMaxBytes = live.DiskMaxBytes
			}
			view.Checkpoints = live.Checkpoints
			if live.LastActiveAt != nil {
				view.LastActiveAt = live.LastActiveAt
			}
		}
	}
	if sessions, sErr := m.store.ListActiveSessions(ctx, userID); sErr == nil {
		view.ActiveSessions = sessions
	}
	return view, nil
}

// Ensure provisions (if needed) and wakes the user's persistent computer,
// enforcing fail-closed billing/credit admission, awake-only global fleet cap,
// Nori's SSRF network policy, and lifecycle/metering timestamps.
func (m *Manager) Ensure(ctx context.Context, userID string) (ComputerRecord, error) {
	if !m.Enabled() {
		return ComputerRecord{}, ErrDisabled
	}
	lock := m.userLock(userID)
	lock.Lock()
	defer lock.Unlock()

	now := m.now()
	if _, err := m.checkHostedAccess(ctx, userID, now); err != nil {
		m.invalidateEnsure(userID)
		return ComputerRecord{}, err
	}

	rec, err := m.store.GetComputer(ctx, userID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return ComputerRecord{}, err
	}
	if err == nil && rec.Status == StatusDeleting {
		return ComputerRecord{}, ErrDeleting
	}

	if err == nil && rec.Status == StatusReady && rec.Provider == m.provider.Name() && rec.ProviderID != "" &&
		rec.BootstrapVersion == m.cfg.BootstrapVer && m.isRecentlyEnsured(userID, rec.ProviderID, rec.BootstrapVersion) {
		_ = m.store.TouchComputerActivity(ctx, userID, now)
		m.applyPurgedTombstones(ctx, userID, rec.ProviderID)
		return rec, nil
	}

	maxFleet := m.effectiveMaxFleet()
	if errors.Is(err, ErrNotFound) || rec.Status == StatusDeleted {
		candidateID := "cmp_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
		placeholder := ComputerRecord{
			UserID:           userID,
			ComputerID:       candidateID,
			Provider:         m.provider.Name(),
			ProviderID:       expectedProviderID(m.provider.Name(), candidateID),
			Status:           StatusProvisioning,
			BootstrapVersion: m.cfg.BootstrapVer,
			LastActiveAt:     &now,
			LastActivityAt:   &now,
			ProviderOwner:    DefaultProviderOwner,
		}
		reserved, _, rErr := m.store.ReserveComputerWithCap(ctx, placeholder, maxFleet)
		if rErr != nil {
			return ComputerRecord{}, rErr
		}
		rec = reserved
	} else if !isAwakeStatus(rec.Status) {
		if capErr := m.store.CheckFleetCapAndMarkWaking(ctx, userID, maxFleet, now); capErr != nil {
			return ComputerRecord{}, capErr
		}
	}
	computerID := rec.ComputerID

	spec := Spec{
		UserID:                  userID,
		ComputerID:              computerID,
		DisplayWidth:            m.cfg.DisplayWidth,
		DisplayHeight:           m.cfg.DisplayHeight,
		MemoryLimitMB:           m.cfg.MemoryLimitMB,
		MaxDiskGB:               m.cfg.MaxDiskGB,
		CurrentBootstrapVersion: rec.BootstrapVersion,
	}

	inst, err := m.provider.Ensure(ctx, spec)
	if err != nil {
		m.invalidateEnsure(userID)
		m.markError(ctx, userID)
		return ComputerRecord{}, err
	}

	// Enforce baseline SSRF egress block on every ensure/provision.
	if err := m.provider.SetNetworkPolicy(ctx, inst.ProviderID, DefaultNetworkPolicy()); err != nil {
		m.invalidateEnsure(userID)
		m.markError(ctx, userID)
		return ComputerRecord{}, fmt.Errorf("apply computer network policy: %w", err)
	}

	// Start the RSS memory watchdog even on shell-only workloads (best effort).
	_, _ = m.provider.Exec(ctx, inst.ProviderID, ExecRequest{
		Command: `if [ -x /home/nori/.nori/bin/nori-watchdog ] && ! pgrep -f "[n]ori-watchdog" >/dev/null 2>&1; then mkdir -p /home/nori/.nori/logs; nohup /home/nori/.nori/bin/nori-watchdog >/home/nori/.nori/logs/watchdog.log 2>&1 </dev/null & fi`,
		WorkDir: DefaultWorkDir,
		Timeout: 10 * time.Second,
	})

	// Re-apply any pending purged-file tombstones so forgotten/deleted files stay deleted across wake.
	m.applyPurgedTombstones(ctx, userID, inst.ProviderID)

	baseline := inst.BaselineCheckpointID
	if baseline == "" {
		baseline = rec.BaselineCheckpointID
	}
	bootstrapVer := inst.BootstrapVersion
	if bootstrapVer == "" {
		bootstrapVer = m.cfg.BootstrapVer
	}
	status := inst.Status
	if status == "" {
		status = StatusReady
	}
	activeAt := m.now()
	awakeAt := activeAt
	meteredAt := activeAt
	owner := rec.ProviderOwner
	if owner == "" {
		owner = DefaultProviderOwner
	}
	updated := ComputerRecord{
		UserID:               userID,
		ComputerID:           computerID,
		Provider:             m.provider.Name(),
		ProviderID:           inst.ProviderID,
		Status:               status,
		BaselineCheckpointID: baseline,
		BootstrapVersion:     bootstrapVer,
		LastActiveAt:         &activeAt,
		LastActivityAt:       &activeAt,
		AwakeSince:           &awakeAt,
		LastMeteredAt:        &meteredAt,
		SleepingSince:        nil,
		ProviderOwner:        owner,
	}
	if err := m.store.UpsertComputer(ctx, updated); err != nil {
		return ComputerRecord{}, err
	}
	if stored, gErr := m.store.GetComputer(ctx, userID); gErr == nil {
		updated = stored
	}
	m.markEnsured(userID, updated.ProviderID, updated.BootstrapVersion)
	m.invalidateStatus(userID)
	return updated, nil
}

// Exec runs an agent command inside the user's computer with hardened env filtering,
// user-control lease enforcement, timeout capping (<= 90s), and output truncation.
func (m *Manager) Exec(ctx context.Context, userID string, req ExecRequest) (ExecResult, error) {
	if m.Enabled() && m.hasActiveControlSession(ctx, userID) {
		return ExecResult{}, ErrUserInControl
	}
	return m.execInternal(ctx, userID, req)
}

// ExecSystem runs an internal system maintenance command (such as nori-desktop-start)
// without being blocked by an active user control session.
func (m *Manager) ExecSystem(ctx context.Context, userID string, req ExecRequest) (ExecResult, error) {
	return m.execInternal(ctx, userID, req)
}

func (m *Manager) execInternal(ctx context.Context, userID string, req ExecRequest) (ExecResult, error) {
	rec, err := m.Ensure(ctx, userID)
	if err != nil {
		return ExecResult{}, err
	}
	if strings.TrimSpace(req.Command) == "" {
		return ExecResult{}, errors.New("command is required")
	}
	workDir, err := NormalizePath(req.WorkDir)
	if err != nil {
		return ExecResult{}, err
	}
	req.WorkDir = workDir
	if req.Timeout <= 0 {
		req.Timeout = DefaultExecTimeout
	}
	if req.Timeout > MaxExecTimeout {
		req.Timeout = MaxExecTimeout
	}
	req.Env = sanitizeEnv(req.Env)

	res, err := m.provider.Exec(ctx, rec.ProviderID, req)
	if shouldInvalidateEnsureOnErr(err) {
		m.invalidateEnsure(userID)
	}
	now := m.now()
	_ = m.store.UpdateComputerStatus(ctx, userID, StatusReady, &now)
	res.Stdout, res.StdoutTruncated, res.StdoutBytes = TruncateOutput(res.Stdout)
	res.Stderr, res.StderrTruncated, res.StderrBytes = TruncateOutput(res.Stderr)
	return res, err
}

// ReadFile reads a text/general file strictly under /home/nori (clamped to DefaultMaxReadBytes = 2 MiB).
func (m *Manager) ReadFile(ctx context.Context, userID, filePath string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 || maxBytes > DefaultMaxReadBytes {
		maxBytes = DefaultMaxReadBytes
	}
	return m.readFileWithCap(ctx, userID, filePath, maxBytes)
}

// ReadBinaryFile reads a binary artifact (such as a screenshot or Library publish file)
// strictly under /home/nori up to MaxPublishReadBytes (10 MiB).
func (m *Manager) ReadBinaryFile(ctx context.Context, userID, filePath string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 || maxBytes > MaxPublishReadBytes {
		maxBytes = MaxPublishReadBytes
	}
	return m.readFileWithCap(ctx, userID, filePath, maxBytes)
}

func (m *Manager) readFileWithCap(ctx context.Context, userID, filePath string, maxBytes int64) ([]byte, error) {
	rec, err := m.Ensure(ctx, userID)
	if err != nil {
		return nil, err
	}
	clean, err := NormalizePath(filePath)
	if err != nil {
		return nil, err
	}
	data, err := m.provider.ReadFile(ctx, rec.ProviderID, clean, maxBytes)
	if shouldInvalidateEnsureOnErr(err) {
		m.invalidateEnsure(userID)
	}
	now := m.now()
	_ = m.store.UpdateComputerStatus(ctx, userID, StatusReady, &now)
	return data, err
}

// WriteFile writes a file strictly under /home/nori.
func (m *Manager) WriteFile(ctx context.Context, userID, filePath string, data []byte, mode uint32) error {
	if m.Enabled() && m.hasActiveControlSession(ctx, userID) {
		return ErrUserInControl
	}
	if len(data) > MaxWriteFileBytes {
		return ErrFileTooLarge
	}
	rec, err := m.Ensure(ctx, userID)
	if err != nil {
		return err
	}
	clean, err := NormalizePath(filePath)
	if err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	if err := m.provider.WriteFile(ctx, rec.ProviderID, clean, data, mode); err != nil {
		if shouldInvalidateEnsureOnErr(err) {
			m.invalidateEnsure(userID)
		}
		return err
	}
	now := m.now()
	_ = m.store.UpdateComputerStatus(ctx, userID, StatusReady, &now)
	return nil
}

// ListFiles lists directory contents strictly under /home/nori.
func (m *Manager) ListFiles(ctx context.Context, userID, dir string) ([]FileEntry, error) {
	rec, err := m.Ensure(ctx, userID)
	if err != nil {
		return nil, err
	}
	clean, err := NormalizePath(dir)
	if err != nil {
		return nil, err
	}
	entries, err := m.provider.ListFiles(ctx, rec.ProviderID, clean)
	if shouldInvalidateEnsureOnErr(err) {
		m.invalidateEnsure(userID)
	}
	now := m.now()
	_ = m.store.UpdateComputerStatus(ctx, userID, StatusReady, &now)
	return entries, err
}

// DeletePath removes a file or directory strictly inside /home/nori (never /home/nori itself).
func (m *Manager) DeletePath(ctx context.Context, userID, targetPath string) error {
	if m.Enabled() && m.hasActiveControlSession(ctx, userID) {
		return ErrUserInControl
	}
	clean, err := NormalizePath(targetPath)
	if err != nil {
		return err
	}
	if clean == HomeDir || clean == DefaultWorkDir {
		return ErrPathTraversal
	}
	rec, err := m.Ensure(ctx, userID)
	if err != nil {
		return err
	}
	if err := m.provider.DeletePath(ctx, rec.ProviderID, clean); err != nil {
		if shouldInvalidateEnsureOnErr(err) {
			m.invalidateEnsure(userID)
		}
		return err
	}
	_ = m.store.DeleteFileLink(ctx, userID, clean)
	now := m.now()
	_ = m.store.UpdateComputerStatus(ctx, userID, StatusReady, &now)
	return nil
}

// StageUpload writes a task upload into /home/nori/workspace/uploads/ (or a clean subpath)
// and returns the normalized workspace path.
func (m *Manager) StageUpload(ctx context.Context, userID, destPath, fileName string, data []byte) (string, error) {
	target := strings.TrimSpace(destPath)
	if target == "" {
		base := path.Base(strings.ReplaceAll(fileName, "\\", "/"))
		if base == "" || base == "." || base == "/" {
			base = "upload.bin"
		}
		target = path.Join(DefaultUploadsDir, base)
	}
	clean, err := NormalizePath(target)
	if err != nil {
		return "", err
	}
	if err := m.WriteFile(ctx, userID, clean, data, 0o644); err != nil {
		return "", err
	}
	return clean, nil
}

// LinkFile records a link between a workspace file and a published Library result and/or entity tags.
func (m *Manager) LinkFile(ctx context.Context, userID, workspacePath, resultID string, entityTags []string) error {
	if !m.Enabled() {
		return ErrDisabled
	}
	clean, err := NormalizePath(workspacePath)
	if err != nil {
		return err
	}
	return m.store.UpsertFileLink(ctx, FileLink{
		UserID:        userID,
		WorkspacePath: clean,
		ResultID:      resultID,
		EntityTags:    entityTags,
	})
}

// PurgeLinkedResultFiles removes workspace files linked to a deleted Library result (all_versions).
func (m *Manager) PurgeLinkedResultFiles(ctx context.Context, userID, resultID string) error {
	if !m.Enabled() || strings.TrimSpace(resultID) == "" {
		return nil
	}
	links, err := m.store.ListFileLinksByResult(ctx, userID, resultID)
	if err != nil || len(links) == 0 {
		return err
	}
	return m.purgeLinks(ctx, userID, links)
}

// PurgeLinkedEntityFiles removes workspace files linked to an entity tag during `forget`.
func (m *Manager) PurgeLinkedEntityFiles(ctx context.Context, userID, entityTag string) error {
	if !m.Enabled() || strings.TrimSpace(entityTag) == "" {
		return nil
	}
	links, err := m.store.ListFileLinksByEntityTag(ctx, userID, entityTag)
	if err != nil || len(links) == 0 {
		return err
	}
	return m.purgeLinks(ctx, userID, links)
}

func (m *Manager) purgeLinks(ctx context.Context, userID string, links []FileLink) error {
	// Record durable purge tombstones first so even if the VM is unreachable right now or an
	// older checkpoint is later restored, purged files will be re-deleted on next wake/restore.
	for _, link := range links {
		_ = m.store.UpsertFileLink(ctx, FileLink{
			UserID:        userID,
			WorkspacePath: link.WorkspacePath,
			ResultID:      PurgedLinkResultID,
			EntityTags:    nil,
		})
	}
	rec, err := m.store.GetComputer(ctx, userID)
	if errors.Is(err, ErrNotFound) || rec.Status == StatusDeleted {
		for _, link := range links {
			_ = m.store.DeleteFileLink(ctx, userID, link.WorkspacePath)
		}
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, link := range links {
		if delErr := m.provider.DeletePath(ctx, rec.ProviderID, link.WorkspacePath); delErr != nil && !errors.Is(delErr, ErrNotFound) {
			errs = append(errs, delErr)
			continue
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) applyPurgedTombstones(ctx context.Context, userID, providerID string) {
	if providerID == "" {
		return
	}
	tombstones, err := m.store.ListFileLinksByResult(ctx, userID, PurgedLinkResultID)
	if err != nil || len(tombstones) == 0 {
		return
	}
	for _, link := range tombstones {
		_ = m.provider.DeletePath(ctx, providerID, link.WorkspacePath)
	}
}

// Wake ensures the user's computer is running and ready.
func (m *Manager) Wake(ctx context.Context, userID string) (StatusView, error) {
	m.invalidateEnsure(userID)
	m.invalidateStatus(userID)
	if _, err := m.Ensure(ctx, userID); err != nil {
		return StatusView{}, err
	}
	m.invalidateStatus(userID)
	return m.Status(ctx, userID)
}

// Sleep ends active sessions, puts the computer to sleep, and atomically meters
// elapsed awake time + transitions status to sleeping in a single transaction.
func (m *Manager) Sleep(ctx context.Context, userID string) error {
	if !m.Enabled() {
		return ErrDisabled
	}
	lock := m.userLock(userID)
	lock.Lock()
	defer lock.Unlock()
	return m.sleepLocked(ctx, userID, false, DesktopCloseIdleSleeping)
}

// ForceSleepUser immediately terminates open Desktop VNC sessions, revokes session tokens,
// puts the user's Sprite to sleep, and atomically meters any unbilled awake time.
// Safe and idempotent to call from cross-instance entitlement:<uid> notify handlers even if
// the user has no computer or is already sleeping.
func (m *Manager) ForceSleepUser(ctx context.Context, userID string) error {
	if !m.Enabled() {
		return nil
	}
	m.CloseDesktopSessionsWithReason(userID, DesktopCloseEntitlement)
	lock := m.userLock(userID)
	lock.Lock()
	defer lock.Unlock()
	err := m.sleepLocked(ctx, userID, true, DesktopCloseEntitlement)
	if errors.Is(err, ErrNotProvisioned) || errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (m *Manager) sleepLocked(ctx context.Context, userID string, force bool, closeReason DesktopCloseReason) error {
	m.invalidateEnsure(userID)
	m.invalidateStatus(userID)
	m.CloseDesktopSessionsWithReason(userID, closeReason)
	m.notifyUserSessionsRevoked(ctx, userID)
	_ = m.store.RevokeUserSessions(ctx, userID, m.now())

	rec, err := m.store.GetComputer(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		if force {
			return nil
		}
		return ErrNotProvisioned
	}
	if err != nil {
		return err
	}
	if rec.Status == StatusDeleting {
		if force {
			return nil
		}
		return ErrDeleting
	}
	if rec.Status == StatusDeleted {
		if force {
			return nil
		}
		return ErrNotProvisioned
	}
	if rec.Status == StatusSleeping && rec.AwakeSince == nil {
		return nil
	}
	if rec.ProviderID != "" && rec.Provider == m.provider.Name() {
		if sErr := m.provider.Sleep(ctx, rec.ProviderID); sErr != nil {
			if errors.Is(sErr, ErrNotFound) {
				// Provider instance already vanished: finalize any elapsed time up to last activity and mark deleted.
				now := m.now()
				meterEnd := chooseProviderStopTime(rec, nil, now)
				res, mErr := m.store.MeterAwakeTime(ctx, MeterAwakeRequest{
					UserID:           userID,
					Now:              now,
					RatePerHourUSD:   m.effectiveRatePerHour(),
					TransitionStatus: StatusDeleted,
					MeterEndAt:       &meterEnd,
				})
				if mErr == nil {
					emitMeteredUsage(ctx, userID, res)
				}
				m.invalidateStatus(userID)
				if force {
					return mErr
				}
				return sErr
			}
			return sErr
		}
	}
	res, err := m.store.MeterAwakeTime(ctx, MeterAwakeRequest{
		UserID:           userID,
		Now:              m.now(),
		RatePerHourUSD:   m.effectiveRatePerHour(),
		TransitionStatus: StatusSleeping,
	})
	if err != nil {
		return err
	}
	emitMeteredUsage(ctx, userID, res)
	m.invalidateStatus(userID)
	return nil
}

// StartJanitor launches the background leader-elected Sprite janitor goroutine until ctx is cancelled.
// Calling StartJanitor multiple times on the same Manager is a safe no-op.
func (m *Manager) StartJanitor(ctx context.Context) {
	if !m.Enabled() {
		return
	}
	m.mu.Lock()
	if m.janitorStarted {
		m.mu.Unlock()
		return
	}
	m.janitorStarted = true
	interval := m.cfg.JanitorInterval
	m.mu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer func() {
			m.releaseJanitorLeadership(context.Background())
			m.mu.Lock()
			m.janitorStarted = false
			m.mu.Unlock()
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				passCtx, cancel := context.WithTimeout(ctx, max(interval-5*time.Second, 30*time.Second))
				if err := m.RunJanitorPass(passCtx); err != nil && !errors.Is(err, context.Canceled) {
					slog.WarnContext(passCtx, "computer janitor pass failed", "err", err)
				}
				cancel()
			}
		}
	}()
}

// RunJanitorPass executes one leader-elected janitor cycle:
// 1. Reconciles actual provider status (stops billing phantom awake time if Fly already slept/destroyed a Sprite).
// 2. Reaps awake Sprites whose owner is suspended, subscription_inactive, or credit_exhausted.
// 3. Auto-sleeps Sprites idle for >= IdleAutoSleep (default 15m).
// 4. Atomically meters periodic awake time for remaining active Sprites (single-tx usage + global budget).
func (m *Manager) RunJanitorPass(ctx context.Context) error {
	if !m.Enabled() {
		return nil
	}
	leader, err := m.acquireJanitorLeadership(ctx)
	if err != nil {
		return err
	}
	if !leader {
		return nil
	}

	var errs []error
	if err := m.ReapDeletingComputers(ctx); err != nil {
		errs = append(errs, err)
	}
	if _, err := m.SweepIdleSessions(ctx); err != nil {
		errs = append(errs, err)
	}

	awake, err := m.store.ListAwakeComputers(ctx, 200)
	if err != nil {
		errs = append(errs, err)
		return errors.Join(errs...)
	}
	for _, rec := range awake {
		if ctx.Err() != nil {
			break
		}
		if err := m.reconcileAndMeterComputer(ctx, rec); err != nil {
			errs = append(errs, fmt.Errorf("janitor computer %s (user %s): %w", rec.ComputerID, rec.UserID, err))
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) acquireJanitorLeadership(ctx context.Context) (bool, error) {
	m.mu.Lock()
	leases := m.leases
	leaseTTL := m.cfg.JanitorInterval * 2
	m.mu.Unlock()
	if leases == nil {
		return true, nil
	}
	if leaseTTL < 90*time.Second {
		leaseTTL = 90 * time.Second
	}
	if renewed, err := leases.RenewUserWake(ctx, janitorLeaderUserID, janitorLeaderLane, m.instanceID, leaseTTL); err == nil && renewed {
		return true, nil
	}
	wakes, err := leases.ListUserWakes(ctx, janitorLeaderUserID)
	if err != nil {
		return false, err
	}
	needsWake := true
	for _, w := range wakes {
		if w.Lane == janitorLeaderLane {
			needsWake = !w.Runnable
			break
		}
	}
	if needsWake {
		if err := leases.WakeUser(ctx, janitorLeaderUserID, janitorLeaderLane, time.Time{}); err != nil {
			return false, err
		}
	}
	_, ok, err := leases.ClaimUserWake(ctx, janitorLeaderLane, m.instanceID, leaseTTL)
	return ok, err
}

func (m *Manager) releaseJanitorLeadership(ctx context.Context) {
	m.mu.Lock()
	leases := m.leases
	m.mu.Unlock()
	if leases == nil {
		return
	}
	relCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = leases.ReleaseUserWake(relCtx, janitorLeaderUserID, janitorLeaderLane, m.instanceID, 0, time.Time{})
}

func (m *Manager) shouldReapUser(ctx context.Context, userID string, now time.Time) (bool, string) {
	bsvc := m.Billing()
	if bsvc == nil {
		return false, ""
	}
	ent, err := bsvc.GetEntitlement(ctx, userID)
	if err != nil {
		if errors.Is(err, billing.ErrEntitlementMissing) {
			return true, billing.CodeEntitlementMissing
		}
		return false, ""
	}
	if ent.IsSuspended() {
		return true, billing.CodeAccountSuspended
	}
	if !ent.IsActiveAt(now) {
		return true, billing.CodeSubscriptionInactive
	}
	if !m.cfg.DisableMetering {
		bal, bErr := bsvc.Store.CreditBalance(ctx, userID)
		if bErr == nil && bal.AvailableMicros <= 0 {
			return true, billing.CodeCreditExhausted
		}
	}
	return false, ""
}

func chooseProviderStopTime(rec ComputerRecord, providerLastActive *time.Time, now time.Time) time.Time {
	end := now.UTC()
	if providerLastActive != nil && !providerLastActive.IsZero() {
		cand := providerLastActive.UTC()
		if cand.Before(end) {
			return cand
		}
		return end
	}
	if rec.LastActivityAt != nil && !rec.LastActivityAt.IsZero() && rec.LastActivityAt.UTC().Before(end) {
		return rec.LastActivityAt.UTC()
	}
	if rec.LastActiveAt != nil && !rec.LastActiveAt.IsZero() && rec.LastActiveAt.UTC().Before(end) {
		return rec.LastActiveAt.UTC()
	}
	return end
}

func lastActivityTime(rec ComputerRecord) time.Time {
	if rec.LastActivityAt != nil && !rec.LastActivityAt.IsZero() {
		return rec.LastActivityAt.UTC()
	}
	if rec.LastActiveAt != nil && !rec.LastActiveAt.IsZero() {
		return rec.LastActiveAt.UTC()
	}
	if rec.AwakeSince != nil && !rec.AwakeSince.IsZero() {
		return rec.AwakeSince.UTC()
	}
	return rec.UpdatedAt.UTC()
}

func (m *Manager) reconcileAndMeterComputer(ctx context.Context, listed ComputerRecord) error {
	lock := m.userLock(listed.UserID)
	lock.Lock()
	defer lock.Unlock()

	cur, err := m.store.GetComputer(ctx, listed.UserID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !isAwakeStatus(cur.Status) && cur.AwakeSince == nil {
		return nil
	}
	if cur.Provider != "" && cur.Provider != m.provider.Name() {
		return nil
	}

	now := m.now()

	// 1. Reconcile actual Fly/Sprites provider status first so we never bill phantom awake time.
	if cur.ProviderID != "" {
		live, sErr := m.provider.Status(ctx, cur.ProviderID)
		switch {
		case errors.Is(sErr, ErrNotFound) || (sErr == nil && live.Status == StatusDeleted):
			meterEnd := chooseProviderStopTime(cur, live.LastActiveAt, now)
			m.invalidateEnsure(cur.UserID)
			m.invalidateStatus(cur.UserID)
			m.CloseDesktopSessionsWithReason(cur.UserID, DesktopCloseIdleSleeping)
			m.notifyUserSessionsRevoked(ctx, cur.UserID)
			_ = m.store.RevokeUserSessions(ctx, cur.UserID, now)
			res, mErr := m.store.MeterAwakeTime(ctx, MeterAwakeRequest{
				UserID:           cur.UserID,
				Now:              now,
				RatePerHourUSD:   m.effectiveRatePerHour(),
				TransitionStatus: StatusDeleted,
				MeterEndAt:       &meterEnd,
			})
			if mErr == nil {
				emitMeteredUsage(ctx, cur.UserID, res)
			}
			return mErr

		case sErr == nil && live.Status == StatusSleeping && !live.Standby:
			recentTransition := !cur.UpdatedAt.IsZero() && now.Sub(cur.UpdatedAt) < eventualStatusGrace
			if !live.EventuallyConsistent || !recentTransition {
				meterEnd := chooseProviderStopTime(cur, live.LastActiveAt, now)
				m.invalidateEnsure(cur.UserID)
				m.invalidateStatus(cur.UserID)
				m.CloseDesktopSessionsWithReason(cur.UserID, DesktopCloseIdleSleeping)
				m.notifyUserSessionsRevoked(ctx, cur.UserID)
				_ = m.store.RevokeUserSessions(ctx, cur.UserID, now)
				res, mErr := m.store.MeterAwakeTime(ctx, MeterAwakeRequest{
					UserID:           cur.UserID,
					Now:              now,
					RatePerHourUSD:   m.effectiveRatePerHour(),
					TransitionStatus: StatusSleeping,
					MeterEndAt:       &meterEnd,
				})
				if mErr == nil {
					emitMeteredUsage(ctx, cur.UserID, res)
				}
				return mErr
			}
		}
	}

	// 2. Reap awake Sprites for suspended, subscription_inactive, or credit_exhausted users.
	if reap, reason := m.shouldReapUser(ctx, cur.UserID, now); reap {
		slog.InfoContext(ctx, "reaping awake computer due to entitlement state", "user_id", cur.UserID, "computer_id", cur.ComputerID, "reason", reason)
		return m.sleepLocked(ctx, cur.UserID, true, DesktopCloseEntitlement)
	}

	// 3. Auto-sleep Sprites idle for >= IdleAutoSleep (default 15m).
	lastAct := lastActivityTime(cur)
	if !lastAct.IsZero() && now.Sub(lastAct) >= m.cfg.IdleAutoSleep {
		slog.InfoContext(ctx, "auto-sleeping idle computer", "user_id", cur.UserID, "computer_id", cur.ComputerID, "idle", now.Sub(lastAct).Round(time.Second))
		return m.sleepLocked(ctx, cur.UserID, true, DesktopCloseIdleSleeping)
	}

	// 4. Periodic single-tx awake-time metering for active Sprites.
	res, err := m.store.MeterAwakeTime(ctx, MeterAwakeRequest{
		UserID:         cur.UserID,
		Now:            now,
		RatePerHourUSD: m.effectiveRatePerHour(),
	})
	if err != nil {
		return err
	}
	emitMeteredUsage(ctx, cur.UserID, res)

	// If this metering tick just exhausted the user's hosted credit balance, sleep immediately.
	if res.Metered {
		if reap, reason := m.shouldReapUser(ctx, cur.UserID, now); reap {
			slog.InfoContext(ctx, "sleeping computer after metering exhausted balance", "user_id", cur.UserID, "computer_id", cur.ComputerID, "reason", reason)
			return m.sleepLocked(ctx, cur.UserID, true, DesktopCloseEntitlement)
		}
	}
	return nil
}

// ListCheckpoints reads saved checkpoints independently of the polled status.
func (m *Manager) ListCheckpoints(ctx context.Context, userID string) ([]CheckpointInfo, error) {
	if !m.Enabled() {
		return nil, ErrDisabled
	}
	lister, ok := m.provider.(interface {
		ListCheckpoints(context.Context, string) ([]CheckpointInfo, error)
	})
	if !ok {
		view, err := m.Status(ctx, userID)
		return view.Checkpoints, err
	}
	rec, err := m.store.GetComputer(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if rec.ProviderID == "" || rec.Status == StatusDeleted || rec.Status == StatusDeleting {
		return nil, nil
	}
	if rec.Provider != m.provider.Name() {
		return nil, ErrProviderMismatch
	}
	return lister.ListCheckpoints(ctx, rec.ProviderID)
}

// Checkpoint creates a point-in-time CoW snapshot of the user's computer,
// enforcing per-tier MaxSpriteCheckpoints from billing.PolicyFor(tier).
func (m *Manager) Checkpoint(ctx context.Context, userID, label string) (string, error) {
	rec, err := m.Ensure(ctx, userID)
	if err != nil {
		return "", err
	}
	if bsvc := m.Billing(); bsvc != nil {
		ent, err := bsvc.CheckAccess(ctx, userID)
		if err != nil {
			return "", err
		}
		policy := ent.EffectivePolicy()
		if policy.MaxSpriteCheckpoints > 0 {
			existing, lErr := m.ListCheckpoints(ctx, userID)
			if lErr != nil {
				return "", lErr
			}
			userCheckpoints := 0
			for _, ck := range existing {
				if rec.BaselineCheckpointID != "" && ck.ID == rec.BaselineCheckpointID {
					continue
				}
				userCheckpoints++
			}
			if userCheckpoints >= policy.MaxSpriteCheckpoints {
				return "", ErrCheckpointLimitReached
			}
		}
	}
	id, err := m.provider.Checkpoint(ctx, rec.ProviderID, label)
	if err == nil {
		_ = m.store.TouchComputerActivity(ctx, userID, m.now())
		m.invalidateStatus(userID)
	}
	return id, err
}

// Restore rolls the user's computer back to a checkpoint ID (or baseline if empty).
func (m *Manager) Restore(ctx context.Context, userID, checkpointID string) error {
	if !m.Enabled() {
		return ErrDisabled
	}
	lock := m.userLock(userID)
	lock.Lock()
	defer lock.Unlock()

	if _, err := m.checkHostedAccess(ctx, userID, m.now()); err != nil {
		return err
	}

	rec, err := m.store.GetComputer(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return ErrNotProvisioned
	}
	if err != nil {
		return err
	}
	if rec.Status == StatusDeleting {
		return ErrDeleting
	}
	target := strings.TrimSpace(checkpointID)
	if target == "" {
		target = rec.BaselineCheckpointID
	}
	if target == "" {
		return errors.New("no checkpoint available to restore")
	}
	if !isAwakeStatus(rec.Status) {
		if capErr := m.store.CheckFleetCapAndMarkWaking(ctx, userID, m.effectiveMaxFleet(), m.now()); capErr != nil {
			return capErr
		}
	}
	m.invalidateEnsure(userID)
	m.invalidateStatus(userID)
	m.CloseDesktopSessionsWithReason(userID, DesktopCloseSessionRevoked)
	m.notifyUserSessionsRevoked(ctx, userID)
	_ = m.store.RevokeUserSessions(ctx, userID, m.now())
	_ = m.store.UpdateComputerStatus(ctx, userID, StatusResetting, nil)
	if err := m.provider.Restore(ctx, rec.ProviderID, target); err != nil {
		m.markError(ctx, userID)
		return err
	}
	if err := m.provider.SetNetworkPolicy(ctx, rec.ProviderID, DefaultNetworkPolicy()); err != nil {
		m.markError(ctx, userID)
		return err
	}
	// Re-apply purged-file tombstones so restoring an older checkpoint cannot resurrect forgotten/deleted files.
	m.applyPurgedTombstones(ctx, userID, rec.ProviderID)
	m.invalidateStatus(userID)
	now := m.now()
	return m.store.UpdateComputerStatus(ctx, userID, StatusReady, &now)
}

// Reset restores the baseline checkpoint (or destroys and reprovisions if no baseline exists).
func (m *Manager) Reset(ctx context.Context, userID string) (StatusView, error) {
	if !m.Enabled() {
		return StatusView{}, ErrDisabled
	}
	rec, err := m.store.GetComputer(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		if _, err := m.Ensure(ctx, userID); err != nil {
			return StatusView{}, err
		}
		return m.Status(ctx, userID)
	}
	if err != nil {
		return StatusView{}, err
	}
	if rec.BaselineCheckpointID != "" {
		if err := m.Restore(ctx, userID, rec.BaselineCheckpointID); err != nil {
			return StatusView{}, err
		}
		_ = m.store.DeleteFileLinksByUser(ctx, userID)
		return m.Status(ctx, userID)
	}
	// Fallback when provider has no baseline checkpoint: destroy and re-provision cleanly.
	lock := m.userLock(userID)
	lock.Lock()
	m.invalidateEnsure(userID)
	m.invalidateStatus(userID)
	m.CloseDesktopSessionsWithReason(userID, DesktopCloseSessionRevoked)
	_ = m.store.RevokeUserSessions(ctx, userID, m.now())
	_ = m.store.UpdateComputerStatus(ctx, userID, StatusResetting, nil)
	if err := m.provider.Destroy(ctx, rec.ProviderID); err != nil && !errors.Is(err, ErrNotFound) {
		m.markError(ctx, userID)
		lock.Unlock()
		return StatusView{}, err
	}
	_ = m.store.DeleteFileLinksByUser(ctx, userID)
	_ = m.store.MarkComputerDeleted(ctx, userID)
	lock.Unlock()
	if _, err := m.Ensure(ctx, userID); err != nil {
		return StatusView{}, err
	}
	return m.Status(ctx, userID)
}

// CreateSession wakes the computer, starts the desktop stack, and mints a short-lived
// single-use user-bound session token for the live desktop WebSocket proxy.
func (m *Manager) CreateSession(ctx context.Context, userID string, mode SessionMode, taskID, interactionID string) (CreatedSession, error) {
	if mode == "" {
		mode = SessionModeView
	}
	if mode != SessionModeView && mode != SessionModeControl {
		return CreatedSession{}, ErrInvalidMode
	}
	rec, err := m.Ensure(ctx, userID)
	if err != nil {
		return CreatedSession{}, err
	}
	// Ensure desktop stack is running and passes readiness check before minting a session.
	startRes, err := m.provider.Exec(ctx, rec.ProviderID, ExecRequest{
		Command: "/home/nori/.nori/bin/nori-desktop-start",
		WorkDir: DefaultWorkDir,
		Timeout: 30 * time.Second,
	})
	if err != nil {
		return CreatedSession{}, fmt.Errorf("start computer desktop: %w", err)
	}
	if startRes.ExitCode != 0 {
		return CreatedSession{}, fmt.Errorf("start computer desktop failed (exit %d): %s", startRes.ExitCode, strings.TrimSpace(startRes.Stderr))
	}
	if _, epErr := m.provider.DesktopEndpoint(ctx, rec.ProviderID); epErr != nil {
		return CreatedSession{}, fmt.Errorf("desktop endpoint unavailable: %w", epErr)
	}
	rawToken, err := generateToken(32)
	if err != nil {
		return CreatedSession{}, err
	}
	now := m.now()
	sess := SessionRecord{
		ID:             "csess_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16],
		UserID:         userID,
		ComputerID:     rec.ComputerID,
		Mode:           mode,
		TaskID:         taskID,
		InteractionID:  interactionID,
		TokenHash:      hashToken(rawToken),
		ExpiresAt:      now.Add(m.cfg.SessionTTL),
		CreatedAt:      now,
		LastActivityAt: now,
	}
	if err := m.store.CreateSession(ctx, sess); err != nil {
		return CreatedSession{}, err
	}
	return CreatedSession{Session: sess, RawToken: rawToken}, nil
}

// ValidateSession verifies a desktop session ID + raw token for userID without consuming it.
func (m *Manager) ValidateSession(ctx context.Context, userID, sessionID, rawToken string) (SessionRecord, error) {
	if !m.Enabled() {
		return SessionRecord{}, ErrDisabled
	}
	if strings.TrimSpace(rawToken) == "" {
		return SessionRecord{}, ErrSessionRevoked
	}
	sess, err := m.store.GetSessionByTokenHash(ctx, hashToken(rawToken))
	if err != nil {
		return SessionRecord{}, err
	}
	if sess.ID != sessionID || sess.UserID != userID {
		return SessionRecord{}, ErrNotFound
	}
	now := m.now()
	if sess.EndedAt != nil {
		return SessionRecord{}, ErrSessionRevoked
	}
	if !sess.ExpiresAt.After(now) {
		_ = m.store.EndSession(ctx, userID, sessionID, now)
		return SessionRecord{}, ErrSessionExpired
	}
	if now.Sub(sess.LastActivityAt) > m.cfg.SessionIdleCap {
		_ = m.store.EndSession(ctx, userID, sessionID, now)
		return SessionRecord{}, ErrSessionExpired
	}
	return sess, nil
}

// ConsumeSession atomically validates and consumes a single-use desktop session token so a
// stolen or replayed token cannot open multiple concurrent WebSocket connections.
func (m *Manager) ConsumeSession(ctx context.Context, userID, sessionID, rawToken string) (SessionRecord, error) {
	if !m.Enabled() {
		return SessionRecord{}, ErrDisabled
	}
	if strings.TrimSpace(rawToken) == "" {
		return SessionRecord{}, ErrSessionRevoked
	}
	now := m.now()
	existing, gErr := m.store.GetSession(ctx, userID, sessionID)
	if gErr != nil {
		return SessionRecord{}, gErr
	}
	if existing.EndedAt != nil {
		return SessionRecord{}, ErrSessionRevoked
	}
	if !existing.ExpiresAt.After(now) || now.Sub(existing.LastActivityAt) > m.cfg.SessionIdleCap {
		_ = m.store.EndSession(ctx, userID, sessionID, now)
		return SessionRecord{}, ErrSessionExpired
	}
	consumedHash := hashToken("consumed:" + sessionID + ":" + uuid.NewString())
	sess, err := m.store.ConsumeSessionToken(ctx, userID, sessionID, hashToken(rawToken), consumedHash, now)
	if err != nil {
		return SessionRecord{}, err
	}
	_ = m.store.TouchComputerActivity(ctx, userID, now)
	return sess, nil
}

// GetSession returns the current session record (used by the WS proxy to check live mode changes).
func (m *Manager) GetSession(ctx context.Context, userID, sessionID string) (SessionRecord, error) {
	if !m.Enabled() {
		return SessionRecord{}, ErrDisabled
	}
	return m.store.GetSession(ctx, userID, sessionID)
}

// SetSessionMode switches a live desktop session between "view" and "control".
// Synchronously notifies any open desktop WebSocket proxy BEFORE returning so handback takes effect immediately.
func (m *Manager) SetSessionMode(ctx context.Context, userID, sessionID string, mode SessionMode) (SessionRecord, error) {
	if !m.Enabled() {
		return SessionRecord{}, ErrDisabled
	}
	if mode != SessionModeView && mode != SessionModeControl {
		return SessionRecord{}, ErrInvalidMode
	}
	updated, err := m.store.UpdateSessionMode(ctx, userID, sessionID, mode)
	if err != nil {
		return SessionRecord{}, err
	}
	_ = m.store.TouchComputerActivity(ctx, userID, m.now())
	m.notifySession(sessionID, SessionEvent{Mode: mode})
	return updated, nil
}

// TouchSession records active user input, extends session TTL (clamped to 2h max lifetime),
// and touches computer last_activity_at.
func (m *Manager) TouchSession(ctx context.Context, sessionID string) error {
	if !m.Enabled() {
		return ErrDisabled
	}
	now := m.now()
	return m.store.TouchSession(ctx, sessionID, now, now.Add(m.cfg.SessionTTL))
}

// EndSession terminates a desktop session immediately.
func (m *Manager) EndSession(ctx context.Context, userID, sessionID string) error {
	if !m.Enabled() {
		return ErrDisabled
	}
	if err := m.store.EndSession(ctx, userID, sessionID, m.now()); err != nil {
		return err
	}
	m.notifySession(sessionID, SessionEvent{Mode: SessionModeView, Revoked: true})
	return nil
}

// DesktopTarget returns the upstream private noVNC WebSocket target for a validated session.
func (m *Manager) DesktopTarget(ctx context.Context, userID string) (DesktopTarget, error) {
	if !m.Enabled() {
		return DesktopTarget{}, ErrDisabled
	}
	rec, err := m.store.GetComputer(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return DesktopTarget{}, ErrNotProvisioned
	}
	if err != nil {
		return DesktopTarget{}, err
	}
	if rec.Status == StatusDeleting {
		return DesktopTarget{}, ErrDeleting
	}
	return m.provider.DesktopEndpoint(ctx, rec.ProviderID)
}

// DestroyForAccountDelete destroys the user's remote VM/container during account deletion.
// The database row is only marked 'deleted' after provider.Destroy confirms destruction.
func (m *Manager) DestroyForAccountDelete(ctx context.Context, userID string) error {
	if !m.Enabled() {
		return nil
	}
	lock := m.userLock(userID)
	lock.Lock()
	defer lock.Unlock()
	m.invalidateEnsure(userID)
	m.invalidateStatus(userID)
	m.CloseDesktopSessions(userID)

	// Meter any remaining awake time before marking deleting/destroying.
	if res, mErr := m.store.MeterAwakeTime(ctx, MeterAwakeRequest{
		UserID:           userID,
		Now:              m.now(),
		RatePerHourUSD:   m.effectiveRatePerHour(),
		TransitionStatus: StatusDeleting,
	}); mErr == nil {
		emitMeteredUsage(ctx, userID, res)
	}

	if err := m.store.MarkComputerDeleting(ctx, userID); err != nil {
		return err
	}
	rec, err := m.store.GetComputer(ctx, userID)
	if errors.Is(err, ErrNotFound) || rec.Status == StatusDeleted {
		return nil
	}
	if err != nil {
		return err
	}
	m.notifyUserSessionsRevoked(ctx, userID)
	if rec.Provider != "" && rec.Provider != m.provider.Name() {
		return fmt.Errorf("%w: computer %s belongs to provider %q but active provider is %q", ErrProviderMismatch, rec.ComputerID, rec.Provider, m.provider.Name())
	}
	if rec.ProviderID != "" {
		if err := m.provider.Destroy(ctx, rec.ProviderID); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	m.invalidateStatus(userID)
	return m.store.MarkComputerDeleted(ctx, userID)
}

// ReapDeletingComputers retries destruction of any computers stuck in 'deleting' state
// so remote Sprites/containers are never orphaned.
func (m *Manager) ReapDeletingComputers(ctx context.Context) error {
	if !m.Enabled() {
		return nil
	}
	pending, err := m.store.ListDeletingComputers(ctx, 50)
	if err != nil {
		return err
	}
	var errs []error
	for _, rec := range pending {
		if ctx.Err() != nil {
			break
		}
		if err := m.DestroyForAccountDelete(ctx, rec.UserID); err != nil {
			errs = append(errs, fmt.Errorf("reap computer %s for user %s: %w", rec.ComputerID, rec.UserID, err))
		}
	}
	return errors.Join(errs...)
}

// SweepIdleSessions expires desktop sessions that exceeded their TTL or idle cap.
func (m *Manager) SweepIdleSessions(ctx context.Context) (int64, error) {
	if !m.Enabled() {
		return 0, nil
	}
	now := m.now()
	return m.store.ExpireIdleSessions(ctx, now, now.Add(-m.cfg.SessionIdleCap))
}

func generateToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// sanitizeEnv strips any Nori host/DB/KMS/OAuth/LLM secrets so they never enter the VM.
func sanitizeEnv(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		upper := strings.ToUpper(strings.TrimSpace(k))
		if upper == "" || isForbiddenEnvKey(upper) {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func isForbiddenEnvKey(upper string) bool {
	if strings.HasPrefix(upper, "NORI_") ||
		strings.HasPrefix(upper, "AWS_") ||
		strings.HasPrefix(upper, "GCP_") ||
		strings.HasPrefix(upper, "GOOGLE_") ||
		strings.HasPrefix(upper, "OPENAI_") ||
		strings.HasPrefix(upper, "ANTHROPIC_") ||
		strings.HasPrefix(upper, "OPENROUTER_") ||
		strings.HasPrefix(upper, "EXA_") ||
		strings.HasPrefix(upper, "JEV_") ||
		strings.HasPrefix(upper, "SENTRY_") ||
		strings.HasPrefix(upper, "ARIZE_") ||
		strings.HasPrefix(upper, "SPRITES_") {
		return true
	}
	for _, needle := range []string{"SECRET", "TOKEN", "PASSWORD", "CREDENTIAL", "PRIVATE_KEY", "API_KEY", "DATABASE_URL", "POSTGRES"} {
		if strings.Contains(upper, needle) {
			return true
		}
	}
	return false
}
