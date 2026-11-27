package computer

import (
	"context"
	"time"
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
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
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
	UpsertComputer(ctx context.Context, rec ComputerRecord) error
	UpdateComputerStatus(ctx context.Context, userID string, status Status, lastActive *time.Time) error
	MarkComputerDeleting(ctx context.Context, userID string) error
	MarkComputerDeleted(ctx context.Context, userID string) error
	ListDeletingComputers(ctx context.Context, limit int) ([]ComputerRecord, error)

	CreateSession(ctx context.Context, sess SessionRecord) error
	GetSession(ctx context.Context, userID, sessionID string) (SessionRecord, error)
	GetSessionByTokenHash(ctx context.Context, tokenHash []byte) (SessionRecord, error)
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
