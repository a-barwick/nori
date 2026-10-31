package state

import (
	"context"
	"time"
)

// ConversationStore is the storage used by conversation processing and its HTTP API.
// Task activities need only TaskStore.
type ConversationStore interface {
	TaskStore
	AcceptConversationInput(ctx context.Context, id, text string) error
	AcquireConversation(ctx context.Context, owner, turnID string) (ConversationControl, *ConversationTurn, error)
	CheckConversation(ctx context.Context, f ConversationFence) (bool, error)
	Close() error
	ConversationCommand(ctx context.Context, id string) (*CommandReceipt, error)
	ConversationHistory(ctx context.Context) ([]ConversationTurn, bool, error)
	ConversationMessages(ctx context.Context, before string) ([]Message, string, error)
	ConversationProjection(ctx context.Context) (Session, error)
	ConversationStatus(ctx context.Context) (ConversationControl, *ConversationTurn, error)
	ConversationTraces(ctx context.Context) ([]ConversationAttempt, error)
	EnsureConversationIntroduction(ctx context.Context, prompt string) error
	PublishConversation(ctx context.Context, f ConversationFence, t ConversationTurn, text string) error
	RecordUsage(ctx context.Context, usage Usage) error
	ReleaseConversation(ctx context.Context, f ConversationFence) error
	RenewConversation(ctx context.Context, f ConversationFence) (bool, error)
	RestartConversation(ctx context.Context, f ConversationFence, t ConversationTurn) (ConversationTurn, time.Duration, error)
	SaveConversation(ctx context.Context, f ConversationFence, t ConversationTurn) error
	UserRoutingPending(ctx context.Context) (bool, error)
}

var _ ConversationStore = (*Google)(nil)
