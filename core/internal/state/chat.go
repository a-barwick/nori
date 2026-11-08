package state

import (
	"context"
	"errors"
	"time"
)

// Chat is the additive foreground conversation. The assistant's existing records
// are deliberately absent from this representation and remain at their old paths.
type Chat struct {
	ID           string        `json:"id" firestore:"id"`
	Title        string        `json:"title" firestore:"title"`
	Mode         string        `json:"mode" firestore:"mode"`
	Messages     []ChatMessage `json:"messages" firestore:"messages,omitempty"`
	MessageCount int64         `json:"-" firestore:"message_count"`
	NextBefore   int64         `json:"nextBefore,omitempty" firestore:"-"`
	ActiveTurn   *string       `json:"activeTurn" firestore:"active_turn"`
	Position     int64         `json:"position" firestore:"position"`
	UpdatedAt    time.Time     `json:"updatedAt" firestore:"updated_at"`
	// Started and LeaseUntil fence the process advancing the active turn.
	Started    string              `json:"-" firestore:"started"`
	LeaseUntil time.Time           `json:"-" firestore:"lease_until"`
	Checkpoint *ChatTurnCheckpoint `json:"-" firestore:"checkpoint,omitempty"`
	// Attention is true while a turn is open or a write's outcome is unknown, so
	// recovery reads only these chats instead of every chat.
	Attention bool `json:"-" firestore:"attention"`
}

// ChatTurnCheckpoint belongs to ActiveTurn. A pending model request may have
// been billed. A pending attachment read can be retried against its saved input.
type ChatTurnCheckpoint struct {
	Models       int                 `firestore:"models"`
	ModelPending bool                `firestore:"model_pending"`
	Read         *ChatReadCheckpoint `firestore:"read,omitempty"`
}

type ChatReadCheckpoint struct {
	OperationID string `firestore:"operation_id"`
	UploadID    string `firestore:"upload_id"`
}

type ChatMessage struct {
	ID          string          `json:"id" firestore:"id"`
	Sequence    int64           `json:"sequence" firestore:"sequence"`
	Role        string          `json:"role" firestore:"role"`
	Text        string          `json:"text" firestore:"text"`
	Status      string          `json:"status" firestore:"status"`
	Attachments []Attachment    `json:"attachments,omitempty" firestore:"attachments,omitempty"`
	Operations  []ChatOperation `json:"operations,omitempty" firestore:"operations,omitempty"`
}

type ChatOperation struct {
	OperationRecord
	LeaseUntil time.Time      `json:"-" firestore:"lease_until"`
	Owner      string         `json:"-" firestore:"owner"`
	Result     map[string]any `json:"result,omitempty" firestore:"result,omitempty"`
}

type ChatSummary struct {
	ID         string    `json:"id" firestore:"id"`
	Title      string    `json:"title" firestore:"title"`
	Mode       string    `json:"mode" firestore:"mode"`
	ActiveTurn *string   `json:"activeTurn" firestore:"active_turn"`
	UpdatedAt  time.Time `json:"updatedAt" firestore:"updated_at"`
}

type ChatHeader struct {
	ID         string    `json:"id" firestore:"id"`
	Title      string    `json:"title" firestore:"title"`
	Mode       string    `json:"mode" firestore:"mode"`
	ActiveTurn *string   `json:"activeTurn" firestore:"active_turn"`
	Position   int64     `json:"position" firestore:"position"`
	UpdatedAt  time.Time `json:"updatedAt" firestore:"updated_at"`
}

// ChatEvent carries only the messages one update added or changed. Replaying
// events in order onto the snapshot they follow rebuilds the conversation.
type ChatEvent struct {
	Seq          int64         `json:"seq" firestore:"seq"`
	Kind         string        `json:"kind" firestore:"kind"`
	Conversation ChatHeader    `json:"conversation" firestore:"conversation"`
	Messages     []ChatMessage `json:"messages" firestore:"messages"`
}

// ChatEventReplayLimit is how far behind a client may resume before it reloads a snapshot.
const ChatEventReplayLimit = 500

var (
	ErrChatMessageTooLarge = errors.New("This message exceeds the saved message size limit.")
	ErrMessageConflict     = errors.New("That message ID was already used for different content.")
	ErrChatBusy            = errors.New("A reply or action is still open. Stop it or wait for it to finish.")
)

func (c Chat) Header() ChatHeader {
	return ChatHeader{ID: c.ID, Title: c.Title, Mode: c.Mode, ActiveTurn: c.ActiveTurn, Position: c.Position, UpdatedAt: c.UpdatedAt}
}

func (c Chat) NeedsAttention() bool {
	if c.ActiveTurn != nil {
		return true
	}
	for _, m := range c.Messages {
		if m.NeedsAttention() {
			return true
		}
	}
	return false
}

// ChatDatabase commits a foreground update and its replay event atomically.
// Returning an empty event kind makes an idempotent read without a write.
type ChatDatabase interface {
	CreateChat(context.Context, Chat) error
	ChatSummaries(context.Context) ([]ChatSummary, error)
	// ActiveChats returns chats with an open turn or an unresolved write.
	ActiveChats(context.Context) ([]Chat, error)
	Chat(context.Context, string) (Chat, error)
	ChatMessage(ctx context.Context, id, messageID string) (ChatMessage, error)
	// UpdateChat loads the latest page plus unresolved writes and explicitly requested message IDs.
	UpdateChat(context.Context, string, func(*Chat) (string, error), ...string) (Chat, error)
	ChatMessages(context.Context, string, int64) (ChatMessagePage, error)
	// WatchChatEvents delivers events after a position as they are committed, until ctx ends or deliver fails.
	WatchChatEvents(context.Context, string, int64, func([]ChatEvent) error) error
	// DeleteChat removes an idle chat and its events and upload records, returning the upload IDs it held.
	DeleteChat(context.Context, string) ([]string, error)
	CreateChatUpload(context.Context, string, Upload) (Upload, error)
	ChatUpload(context.Context, string, string) (Upload, error)
}

// ChatPageSize bounds snapshot and update reads. History remains in message records.
const ChatPageSize = 64
const ChatMessageBytes = 256 << 10

type ChatMessagePage struct {
	Messages   []ChatMessage `json:"messages"`
	NextBefore int64         `json:"nextBefore,omitempty"`
}

func (m ChatMessage) NeedsAttention() bool {
	for _, op := range m.Operations {
		if op.Action != nil && op.Outcome == "attempted" {
			return true
		}
	}
	return false
}
