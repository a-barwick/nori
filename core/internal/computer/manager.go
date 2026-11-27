package computer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"nori/internal/computer/vmscripts"
)

// Config controls computer provisioning, limits, and desktop session lifetimes.
type Config struct {
	DisplayWidth   int
	DisplayHeight  int
	MemoryLimitMB  int
	MaxDiskGB      int
	SessionTTL     time.Duration
	SessionIdleCap time.Duration
	BootstrapVer   string
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

// Manager coordinates lifecycle, policy, cost attribution, sessions, file links,
// and account-deletion teardown across any computer.Provider implementation.
type Manager struct {
	provider Provider
	store    Store
	cfg      Config
	Now      func() time.Time

	mu        sync.Mutex
	locks     map[string]*sync.Mutex
	listeners map[string]map[uint64]func(SessionEvent)
	nextSubID uint64
}

// NewManager creates a Manager backed by provider and store.
func NewManager(provider Provider, store Store, cfg Config) *Manager {
	return &Manager{
		provider:  provider,
		store:     store,
		cfg:       cfg.withDefaults(),
		locks:     map[string]*sync.Mutex{},
		listeners: map[string]map[uint64]func(SessionEvent){},
	}
}

func (m *Manager) SessionIdleCap() time.Duration {
	if m == nil {
		return DefaultSessionIdleCap
	}
	return m.cfg.SessionIdleCap
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
		if live, lErr := m.provider.Status(ctx, rec.ProviderID); lErr == nil {
			if live.Status != "" {
				view.Status = live.Status
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
// applying Nori's SSRF network policy and recording the baseline checkpoint.
func (m *Manager) Ensure(ctx context.Context, userID string) (ComputerRecord, error) {
	if !m.Enabled() {
		return ComputerRecord{}, ErrDisabled
	}
	lock := m.userLock(userID)
	lock.Lock()
	defer lock.Unlock()

	rec, err := m.store.GetComputer(ctx, userID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return ComputerRecord{}, err
	}
	if err == nil && rec.Status == StatusDeleting {
		return ComputerRecord{}, ErrDeleting
	}

	now := m.now()
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
		}
		reserved, _, rErr := m.store.ReserveComputer(ctx, placeholder)
		if rErr != nil {
			return ComputerRecord{}, rErr
		}
		rec = reserved
	}
	computerID := rec.ComputerID

	spec := Spec{
		UserID:        userID,
		ComputerID:    computerID,
		DisplayWidth:  m.cfg.DisplayWidth,
		DisplayHeight: m.cfg.DisplayHeight,
		MemoryLimitMB: m.cfg.MemoryLimitMB,
		MaxDiskGB:     m.cfg.MaxDiskGB,
	}

	inst, err := m.provider.Ensure(ctx, spec)
	if err != nil {
		_ = m.store.UpdateComputerStatus(ctx, userID, StatusError, nil)
		return ComputerRecord{}, err
	}

	// Enforce baseline SSRF egress block on every ensure/provision.
	if err := m.provider.SetNetworkPolicy(ctx, inst.ProviderID, DefaultNetworkPolicy()); err != nil {
		_ = m.store.UpdateComputerStatus(ctx, userID, StatusError, nil)
		return ComputerRecord{}, fmt.Errorf("apply computer network policy: %w", err)
	}

	// Start the RSS memory watchdog even on shell-only workloads (best effort).
	_, _ = m.provider.Exec(ctx, inst.ProviderID, ExecRequest{
		Command: `if [ -x /home/nori/.nori/bin/nori-watchdog ] && ! pgrep -f "nori-watchdog" >/dev/null 2>&1; then mkdir -p /home/nori/.nori/logs; nohup /home/nori/.nori/bin/nori-watchdog >/home/nori/.nori/logs/watchdog.log 2>&1 </dev/null & fi`,
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
	updated := ComputerRecord{
		UserID:               userID,
		ComputerID:           computerID,
		Provider:             m.provider.Name(),
		ProviderID:           inst.ProviderID,
		Status:               status,
		BaselineCheckpointID: baseline,
		BootstrapVersion:     bootstrapVer,
		LastActiveAt:         &activeAt,
	}
	if err := m.store.UpsertComputer(ctx, updated); err != nil {
		return ComputerRecord{}, err
	}
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
	if _, err := m.Ensure(ctx, userID); err != nil {
		return StatusView{}, err
	}
	return m.Status(ctx, userID)
}

// Sleep ends active sessions and puts the computer to sleep to stop compute billing.
func (m *Manager) Sleep(ctx context.Context, userID string) error {
	if !m.Enabled() {
		return ErrDisabled
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
	m.notifyUserSessionsRevoked(ctx, userID)
	_ = m.store.RevokeUserSessions(ctx, userID, m.now())
	if err := m.provider.Sleep(ctx, rec.ProviderID); err != nil {
		return err
	}
	return m.store.UpdateComputerStatus(ctx, userID, StatusSleeping, nil)
}

// Checkpoint creates a point-in-time CoW snapshot of the user's computer.
func (m *Manager) Checkpoint(ctx context.Context, userID, label string) (string, error) {
	rec, err := m.Ensure(ctx, userID)
	if err != nil {
		return "", err
	}
	return m.provider.Checkpoint(ctx, rec.ProviderID, label)
}

// Restore rolls the user's computer back to a checkpoint ID (or baseline if empty).
func (m *Manager) Restore(ctx context.Context, userID, checkpointID string) error {
	if !m.Enabled() {
		return ErrDisabled
	}
	lock := m.userLock(userID)
	lock.Lock()
	defer lock.Unlock()

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
	m.notifyUserSessionsRevoked(ctx, userID)
	_ = m.store.RevokeUserSessions(ctx, userID, m.now())
	_ = m.store.UpdateComputerStatus(ctx, userID, StatusResetting, nil)
	if err := m.provider.Restore(ctx, rec.ProviderID, target); err != nil {
		_ = m.store.UpdateComputerStatus(ctx, userID, StatusError, nil)
		return err
	}
	if err := m.provider.SetNetworkPolicy(ctx, rec.ProviderID, DefaultNetworkPolicy()); err != nil {
		_ = m.store.UpdateComputerStatus(ctx, userID, StatusError, nil)
		return err
	}
	// Re-apply purged-file tombstones so restoring an older checkpoint cannot resurrect forgotten/deleted files.
	m.applyPurgedTombstones(ctx, userID, rec.ProviderID)
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
	_ = m.store.RevokeUserSessions(ctx, userID, m.now())
	_ = m.store.UpdateComputerStatus(ctx, userID, StatusResetting, nil)
	if err := m.provider.Destroy(ctx, rec.ProviderID); err != nil && !errors.Is(err, ErrNotFound) {
		_ = m.store.UpdateComputerStatus(ctx, userID, StatusError, nil)
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
// user-bound session token for the live desktop WebSocket proxy.
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

// ValidateSession verifies a desktop session ID + raw token for userID, checking expiry and idle cap.
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
	m.notifySession(sessionID, SessionEvent{Mode: mode})
	return updated, nil
}

// TouchSession records active user input and extends session TTL.
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
