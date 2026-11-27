package push

import (
	"context"
	"errors"
	"strings"
	"time"
)

const (
	EnvSandbox    = "sandbox"
	EnvProduction = "production"
)

type OutboxStatus string

const (
	StatusPending    OutboxStatus = "pending"
	StatusSent       OutboxStatus = "sent"
	StatusFailed     OutboxStatus = "failed"
	StatusSuppressed OutboxStatus = "suppressed"
)

var (
	ErrNotFound           = errors.New("push device not found")
	ErrInvalidUserID      = errors.New("user_id is required")
	ErrInvalidToken       = errors.New("push token is required")
	ErrInvalidEnvironment = errors.New("push environment must be sandbox or production")
	ErrInvalidDeliveryID  = errors.New("delivery_id is required")
	ErrDailyCapReached    = errors.New("daily push notification cap reached")
)

// Device represents a registered native iOS APNs device token.
// Token and UserID are omitted from JSON serialization so API responses
// match the PushDevice OpenAPI schema and never echo APNs tokens back.
type Device struct {
	ID          string    `json:"id"`
	UserID      string    `json:"-"`
	DeviceID    string    `json:"device_id,omitempty"`
	Token       string    `json:"-"`
	Environment string    `json:"environment"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// OutboxItem represents a durable, idempotent push notification delivery record
// keyed by DeliveryID in nori_push_outbox.
type OutboxItem struct {
	DeliveryID    string       `json:"delivery_id"`
	UserID        string       `json:"user_id"`
	InteractionID string       `json:"interaction_id,omitempty"`
	CollapseID    string       `json:"collapse_id,omitempty"`
	Title         string       `json:"title"`
	Body          string       `json:"body"`
	Status        OutboxStatus `json:"status"`
	Attempts      int          `json:"attempts"`
	LastError     string       `json:"last_error,omitempty"`
	SentAt        *time.Time   `json:"sent_at,omitempty"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

// DeviceDeleter removes an invalid APNs token from storage.
type DeviceDeleter interface {
	DeleteDeviceByToken(ctx context.Context, token string) error
}

// Store persists push device registrations and the idempotent push outbox.
type Store interface {
	DeviceDeleter

	RegisterDevice(ctx context.Context, userID, token, deviceID, env string) (Device, error)
	DeleteDevice(ctx context.Context, userID, id string) error
	DeleteUserDevices(ctx context.Context, userID string) error
	ListDevices(ctx context.Context, userID string) ([]Device, error)

	EnqueueOutbox(ctx context.Context, item OutboxItem) (OutboxItem, bool, error)
	GetOutbox(ctx context.Context, deliveryID string) (OutboxItem, error)
	ListPendingOutbox(ctx context.Context, limit int) ([]OutboxItem, error)
	MarkOutboxSent(ctx context.Context, deliveryID string, sentAt time.Time) error
	MarkOutboxFailed(ctx context.Context, deliveryID string, lastErr string, permanent bool) error
	MarkOutboxSuppressed(ctx context.Context, deliveryID string, reason string) error
	CountSentSince(ctx context.Context, userID string, since time.Time) (int, error)
}

// ValidEnvironment reports whether env is a supported APNs environment.
func ValidEnvironment(env string) bool {
	return env == EnvSandbox || env == EnvProduction
}

func normalizeRegisterArgs(userID, token, deviceID, env string) (string, string, string, string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", "", "", "", ErrInvalidUserID
	}
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 512 {
		return "", "", "", "", ErrInvalidToken
	}
	deviceID = strings.TrimSpace(deviceID)
	if len(deviceID) > 256 {
		deviceID = deviceID[:256]
	}
	env = strings.TrimSpace(env)
	if !ValidEnvironment(env) {
		return "", "", "", "", ErrInvalidEnvironment
	}
	return userID, token, deviceID, env, nil
}
