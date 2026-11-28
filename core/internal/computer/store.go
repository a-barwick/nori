package computer

import (
	"context"
	"time"

	"nori/internal/state"
)

// ComputerRecord mirrors a row in nori_user_computers.
type ComputerRecord struct {
	UserID               string     `json:"user_id"`
	ComputerID           string     `json:"computer_id"`
	Provider             string     `json:"provider"`
	ProviderID           string     `json:"provider_id"`
	Status               Status     `json:"status"`
	BaselineCheckpointID string     `json:"baseline_checkpoint_id,omitempty"`
	BootstrapVersion     string     `json:"bootstrap_version"`
	LastActiveAt         *time.Time `json:"last_active_at,omitempty"`
	LastActivityAt       *time.Time `json:"last_activity_at,omitempty"`
	AwakeSince           *time.Time `json:"awake_since,omitempty"`
	LastMeteredAt        *time.Time `json:"last_metered_at,omitempty"`
	SleepingSince        *time.Time `json:"sleeping_since,omitempty"`
	ProviderOwner        string     `json:"provider_owner,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

// MeterAwakeRequest atomically meters elapsed Sprite awake time since last_metered_at
// (or awake_since), advances last_metered_at (and optional status transition such as sleeping),
// inserts the finalized state.Usage row, and updates nori_global_budget_daily in ONE transaction.
type MeterAwakeRequest struct {
	UserID           string
	Now              time.Time
	RatePerHourUSD   float64
	TransitionStatus Status     // optional: StatusSleeping / StatusDeleted / StatusDeleting; empty keeps current status
	UsageID          string     // optional idempotency/usage ID; auto-generated if empty
	DataOwner        string     // optional owner shard override; resolved if empty
	MeterEndAt       *time.Time // optional cap when provider already slept/stopped earlier
}

// MeterAwakeResult reports the outcome of an atomic MeterAwakeTime transaction.
type MeterAwakeResult struct {
	Record     ComputerRecord
	Metered    bool
	Elapsed    time.Duration
	CostUSD    float64
	CostMicros int64
	Usage      *state.Usage
}

// SessionRecord mirrors a row in nori_computer_sessions.
type SessionRecord struct {
	ID             string      `json:"id"`
	UserID         string      `json:"user_id"`
	ComputerID     string      `json:"computer_id"`
	Mode           SessionMode `json:"mode"`
	TaskID         string      `json:"task_id,omitempty"`
	InteractionID  string      `json:"interaction_id,omitempty"`
	TokenHash      []byte      `json:"-"`
	ExpiresAt      time.Time   `json:"expires_at"`
	EndedAt        *time.Time  `json:"ended_at,omitempty"`
	CreatedAt      time.Time   `json:"created_at"`
	LastActivityAt time.Time   `json:"last_activity_at"`
}

// FileLink mirrors a row in nori_computer_file_links.
type FileLink struct {
	UserID        string    `json:"user_id"`
	WorkspacePath string    `json:"workspace_path"`
	ResultID      string    `json:"result_id,omitempty"`
	EntityTags    []string  `json:"entity_tags,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Store persists user computer lifecycle, desktop sessions, and workspace file links.
type Store interface {
	GetComputer(ctx context.Context, userID string) (ComputerRecord, error)
	ReserveComputer(ctx context.Context, rec ComputerRecord) (ComputerRecord, bool, error)
	ReserveComputerWithCap(ctx context.Context, rec ComputerRecord, maxFleet int) (ComputerRecord, bool, error)
	CheckFleetCapAndMarkWaking(ctx context.Context, userID string, maxFleet int, now time.Time) error
	UpsertComputer(ctx context.Context, rec ComputerRecord) error
	UpdateComputerStatus(ctx context.Context, userID string, status Status, lastActive *time.Time) error
	TouchComputerActivity(ctx context.Context, userID string, at time.Time) error
	CountAwakeComputers(ctx context.Context, excludeUserID string) (int, error)
	ListAwakeComputers(ctx context.Context, limit int) ([]ComputerRecord, error)
	MeterAwakeTime(ctx context.Context, req MeterAwakeRequest) (MeterAwakeResult, error)
	MarkComputerDeleting(ctx context.Context, userID string) error
	MarkComputerDeleted(ctx context.Context, userID string) error
	ListDeletingComputers(ctx context.Context, limit int) ([]ComputerRecord, error)

	CreateSession(ctx context.Context, sess SessionRecord) error
	GetSession(ctx context.Context, userID, sessionID string) (SessionRecord, error)
	GetSessionByTokenHash(ctx context.Context, tokenHash []byte) (SessionRecord, error)
	ConsumeSessionToken(ctx context.Context, userID, sessionID string, tokenHash, consumedHash []byte, now time.Time) (SessionRecord, error)
	ListActiveSessions(ctx context.Context, userID string) ([]SessionRecord, error)
	UpdateSessionMode(ctx context.Context, userID, sessionID string, mode SessionMode) (SessionRecord, error)
	TouchSession(ctx context.Context, sessionID string, activityAt, newExpiresAt time.Time) error
	EndSession(ctx context.Context, userID, sessionID string, endedAt time.Time) error
	RevokeUserSessions(ctx context.Context, userID string, endedAt time.Time) error
	ExpireIdleSessions(ctx context.Context, now time.Time, idleCutoff time.Time) (int64, error)

	UpsertFileLink(ctx context.Context, link FileLink) error
	ListFileLinksByResult(ctx context.Context, userID, resultID string) ([]FileLink, error)
	ListFileLinksByEntityTag(ctx context.Context, userID, tag string) ([]FileLink, error)
	DeleteFileLink(ctx context.Context, userID, workspacePath string) error
	DeleteFileLinksByUser(ctx context.Context, userID string) error
}
