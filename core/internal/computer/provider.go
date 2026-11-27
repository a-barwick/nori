package computer

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"nori/internal/computer/vmscripts"
)

const (
	DefaultDisplayWidth    = 1280
	DefaultDisplayHeight   = 800
	DefaultMemoryLimitMB   = 3072
	DefaultMaxDiskGB       = 10
	DefaultExecTimeout     = 60 * time.Second
	MaxExecTimeout         = 90 * time.Second
	MaxExecOutputBytes     = 32 << 10 // 32 KiB
	DefaultMaxReadBytes    = 2 << 20  // 2 MiB
	MaxPublishReadBytes    = 10 << 20 // 10 MiB
	MaxWriteFileBytes      = 10 << 20 // 10 MiB
	PurgedLinkResultID     = "__purged__"
	DefaultSessionTTL      = 15 * time.Minute
	DefaultSessionIdleCap  = 10 * time.Minute
	HomeDir                = "/home/nori"
	DefaultWorkDir         = "/home/nori/workspace"
	DefaultUploadsDir      = "/home/nori/workspace/uploads"
	DefaultBootstrapVer    = vmscripts.Version
	DefaultDesktopPort     = 8080
	DefaultChromiumCDPPort = 9222
)

var (
	ErrDisabled         = errors.New("personal computer is not enabled")
	ErrNotFound         = errors.New("computer not found")
	ErrNotProvisioned   = errors.New("computer is not provisioned")
	ErrDeleting         = errors.New("computer is being deleted")
	ErrBusy             = errors.New("computer is busy")
	ErrPathTraversal    = errors.New("path must stay inside /home/nori")
	ErrFileTooLarge     = errors.New("file exceeds maximum allowed size")
	ErrTimeout          = errors.New("command timed out")
	ErrPolicyDenied     = errors.New("blocked by computer policy")
	ErrSessionExpired   = errors.New("desktop session has expired")
	ErrSessionRevoked   = errors.New("desktop session has ended")
	ErrInvalidMode      = errors.New("session mode must be view or control")
	ErrUserInControl    = errors.New("user currently has control of the desktop; wait until control is handed back")
	ErrProviderMismatch = errors.New("computer is managed by a different provider")
)

type Status string

const (
	StatusNotProvisioned Status = "not_provisioned"
	StatusProvisioning   Status = "provisioning"
	StatusReady          Status = "ready"
	StatusSleeping       Status = "sleeping"
	StatusResetting      Status = "resetting"
	StatusDeleting       Status = "deleting"
	StatusDeleted        Status = "deleted"
	StatusError          Status = "error"
)

type SessionMode string

const (
	SessionModeView    SessionMode = "view"
	SessionModeControl SessionMode = "control"
)

// Spec defines the desired configuration for a user's persistent computer.
type Spec struct {
	UserID                  string
	ComputerID              string
	DisplayWidth            int    // default 1280
	DisplayHeight           int    // default 800
	MemoryLimitMB           int    // soft watchdog limit, default 3072
	MaxDiskGB               int    // default 10
	CurrentBootstrapVersion string // last recorded bootstrap version for fast-path ensure
}

// Instance represents a provisioned VM/container managed by a Provider.
type Instance struct {
	ProviderID           string
	Status               Status
	BaselineCheckpointID string
	BootstrapVersion     string
}

// InstanceStatus reports live provider state and storage metadata.
type InstanceStatus struct {
	Status               Status           `json:"status"`
	Standby              bool             `json:"-"` // warm/standby state (e.g., Fly Sprites "warm")
	EventuallyConsistent bool             `json:"-"` // provider metadata may lag brief transitions (e.g., Fly Sprites)
	DiskUsedBytes        int64            `json:"disk_used_bytes,omitempty"`
	DiskMaxBytes         int64            `json:"disk_max_bytes,omitempty"`
	Checkpoints          []CheckpointInfo `json:"checkpoints,omitempty"`
	LastActiveAt         *time.Time       `json:"last_active_at,omitempty"`
}

// CheckpointInfo describes a point-in-time CoW filesystem checkpoint.
type CheckpointInfo struct {
	ID        string    `json:"id"`
	Label     string    `json:"label,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ExecRequest specifies a bash command to execute inside the user's computer.
type ExecRequest struct {
	Command    string            // bash -lc <command>
	WorkDir    string            // default /home/nori/workspace
	Env        map[string]string // non-secret env only
	Timeout    time.Duration     // hard cap <= 90s (below 300s Cloud Run timeout)
	Background bool              // returns JobID immediately for long installs/builds
}

// ExecResult contains command output, timing, and compute cost attribution.
type ExecResult struct {
	ExitCode        int     `json:"exit_code"`
	Stdout          string  `json:"stdout"`
	Stderr          string  `json:"stderr"`
	StdoutTruncated bool    `json:"stdout_truncated,omitempty"`
	StderrTruncated bool    `json:"stderr_truncated,omitempty"`
	StdoutBytes     int     `json:"stdout_bytes,omitempty"`
	StderrBytes     int     `json:"stderr_bytes,omitempty"`
	DurationMS      int64   `json:"duration_ms"`
	JobID           string  `json:"job_id,omitempty"`
	CostUSD         float64 `json:"cost_usd,omitempty"`
}

// FileEntry describes a file or directory inside /home/nori.
type FileEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode,omitempty"`
	ModTime time.Time `json:"mod_time,omitempty"`
}

// DesktopTarget holds the upstream WebSocket endpoint and auth headers for noVNC.
// Headers are strictly internal to Nori Core and must never be sent to clients.
type DesktopTarget struct {
	WSURL   string      // internal/upstream ws:// or wss:// noVNC endpoint
	Headers http.Header // upstream auth headers (never sent to client!)
}

// NetworkPolicy controls outbound egress from the user's computer.
type NetworkPolicy struct {
	AllowDomains []string // empty = allow public internet except Deny/SSRF
	DenyDomains  []string
	BlockPrivate bool // always true: RFC1918, loopback, link-local, cloud metadata
}

// DefaultNetworkPolicy returns Nori's baseline SSRF-safe outbound policy.
func DefaultNetworkPolicy() NetworkPolicy {
	return NetworkPolicy{
		BlockPrivate: true,
		DenyDomains: []string{
			"metadata.google.internal",
			"metadata",
			"169.254.169.254",
			"*.internal",
			"*.local",
			"*.localhost",
			"localhost",
			"127.0.0.0/8",
			"10.0.0.0/8",
			"172.16.0.0/12",
			"192.168.0.0/16",
			"169.254.0.0/16",
			"fc00::/7",
			"fe80::/10",
			"::1/128",
		},
	}
}

// Provider is the swappable host adapter interface implemented by sprites, docker, and fake.
// Core packages (agent, task, api) depend only on Provider and Manager, never on host specifics.
type Provider interface {
	Name() string // "sprites", "docker", "fake"
	Ensure(ctx context.Context, spec Spec) (Instance, error)
	Status(ctx context.Context, providerID string) (InstanceStatus, error)
	Exec(ctx context.Context, providerID string, req ExecRequest) (ExecResult, error)
	ReadFile(ctx context.Context, providerID, path string, maxBytes int64) ([]byte, error)
	WriteFile(ctx context.Context, providerID, path string, data []byte, mode uint32) error
	ListFiles(ctx context.Context, providerID, dir string) ([]FileEntry, error)
	DeletePath(ctx context.Context, providerID, path string) error
	DesktopEndpoint(ctx context.Context, providerID string) (DesktopTarget, error)
	SetNetworkPolicy(ctx context.Context, providerID string, policy NetworkPolicy) error
	Checkpoint(ctx context.Context, providerID, label string) (string, error)
	Restore(ctx context.Context, providerID, checkpointID string) error
	Sleep(ctx context.Context, providerID string) error
	Destroy(ctx context.Context, providerID string) error
}

// NormalizePath validates and resolves a path so it stays strictly inside /home/nori.
// Relative paths are resolved relative to /home/nori/workspace.
func NormalizePath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return DefaultWorkDir, nil
	}
	if strings.ContainsRune(trimmed, 0) {
		return "", ErrPathTraversal
	}
	for _, seg := range strings.Split(strings.ReplaceAll(trimmed, "\\", "/"), "/") {
		if seg == ".." {
			return "", ErrPathTraversal
		}
	}
	var joined string
	if strings.HasPrefix(trimmed, "/") {
		joined = path.Clean(trimmed)
	} else {
		joined = path.Clean(path.Join(DefaultWorkDir, trimmed))
	}
	if joined != HomeDir && !strings.HasPrefix(joined, HomeDir+"/") {
		return "", ErrPathTraversal
	}
	return joined, nil
}

// LimitedBuffer is a bounded io.Writer that retains at most limit bytes while
// counting total bytes written so noisy guest processes cannot OOM Nori Core.
type LimitedBuffer struct {
	limit int
	total int
	buf   []byte
}

func NewLimitedBuffer(limit int) *LimitedBuffer {
	if limit <= 0 {
		limit = MaxExecOutputBytes
	}
	return &LimitedBuffer{limit: limit}
}

func (b *LimitedBuffer) Write(p []byte) (int, error) {
	b.total += len(p)
	remaining := b.limit - len(b.buf)
	if remaining > 0 {
		if len(p) <= remaining {
			b.buf = append(b.buf, p...)
		} else {
			b.buf = append(b.buf, p[:remaining]...)
		}
	}
	return len(p), nil
}

func (b *LimitedBuffer) String() string { return string(b.buf) }
func (b *LimitedBuffer) Total() int     { return b.total }
func (b *LimitedBuffer) Truncated() bool {
	return b.total > len(b.buf)
}

// TruncateOutput caps stdout/stderr to MaxExecOutputBytes while recording original size.
func TruncateOutput(s string) (text string, truncated bool, origBytes int) {
	origBytes = len(s)
	if origBytes <= MaxExecOutputBytes {
		return s, false, origBytes
	}
	// Preserve a valid UTF-8 prefix.
	cut := MaxExecOutputBytes
	for cut > 0 && (s[cut]&0xc0) == 0x80 {
		cut--
	}
	return s[:cut], true, origBytes
}
