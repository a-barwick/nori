package state

import (
	"context"
	"errors"
	"time"
)

// Chat is the additive foreground conversation. The assistant's existing records
// are deliberately absent from this representation and remain at their old paths.
type Chat struct {
	ID         string        `json:"id" firestore:"id"`
	Title      string        `json:"title" firestore:"title"`
	Mode       string        `json:"mode" firestore:"mode"`
	Messages   []ChatMessage `json:"messages" firestore:"messages"`
	ActiveTurn *string       `json:"activeTurn" firestore:"active_turn"`
	Position   int64         `json:"position" firestore:"position"`
	UpdatedAt  time.Time     `json:"updatedAt" firestore:"updated_at"`
	// Started identifies the process that owns a provider attempt. A new process
	// closes interrupted attempts rather than submitting an unknown-cost call again.
	Started    string    `json:"-" firestore:"started"`
	LeaseUntil time.Time `json:"-" firestore:"lease_until"`
	// Attention is true while a turn is open or a write's outcome is unknown, so
	// recovery reads only these chats instead of every chat.
	Attention bool `json:"-" firestore:"attention"`
}

type ChatMessage struct {
	ID          string          `json:"id" firestore:"id"`
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

// ChatSummary is one row of the conversation list.
type ChatSummary struct {
	ID         string    `json:"id" firestore:"id"`
	Title      string    `json:"title" firestore:"title"`
	Mode       string    `json:"mode" firestore:"mode"`
	ActiveTurn *string   `json:"activeTurn" firestore:"active_turn"`
	UpdatedAt  time.Time `json:"updatedAt" firestore:"updated_at"`
}

// ChatHeader is a conversation without its messages.
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
	ErrChatFull        = errors.New("This chat reached its length limit. Start a new chat.")
	ErrApprovalPending = errors.New("Approve or decline the pending action first.")
	ErrMessageConflict = errors.New("That message ID was already used for different content.")
	ErrChatBusy        = errors.New("A reply or action is still open. Stop it or wait for it to finish.")
)

func (c Chat) Header() ChatHeader {
	return ChatHeader{ID: c.ID, Title: c.Title, Mode: c.Mode, ActiveTurn: c.ActiveTurn, Position: c.Position, UpdatedAt: c.UpdatedAt}
}

// WaitingForApproval reports whether the open turn is paused on a prepared write.
func (c Chat) WaitingForApproval() bool {
	if c.ActiveTurn == nil || len(c.Messages) == 0 {
		return false
	}
	ops := c.Messages[len(c.Messages)-1].Operations
	return len(ops) > 0 && ops[len(ops)-1].Outcome == "prepared"
}

// NeedsAttention is true while a turn is open or an attempted write has no recorded outcome.
func (c Chat) NeedsAttention() bool {
	if c.ActiveTurn != nil {
		return true
	}
	for _, m := range c.Messages {
		for _, op := range m.Operations {
			if op.Action != nil && op.Outcome == "attempted" {
				return true
			}
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
	UpdateChat(context.Context, string, func(*Chat) (string, error)) (Chat, error)
	// WatchChatEvents delivers events after a position as they are committed, until ctx ends or deliver fails.
	WatchChatEvents(context.Context, string, int64, func([]ChatEvent) error) error
	// DeleteChat removes an idle chat and its events and upload records, returning the upload IDs it held.
	DeleteChat(context.Context, string) ([]string, error)
	CreateChatUpload(context.Context, string, Upload) (Upload, error)
	ChatUpload(context.Context, string, string) (Upload, error)
}
