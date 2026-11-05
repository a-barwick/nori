package state

import (
	"context"
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
	// Started identifies the process that owns a provider attempt. A new process
	// closes interrupted attempts rather than submitting an unknown-cost call again.
	Started    string    `json:"-" firestore:"started"`
	LeaseUntil time.Time `json:"-" firestore:"lease_until"`
	Usage      []Usage   `json:"-" firestore:"usage"`
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

type ChatEvent struct {
	Seq          int64  `json:"seq" firestore:"seq"`
	Kind         string `json:"kind" firestore:"kind"`
	Conversation Chat   `json:"conversation" firestore:"conversation"`
}

// ChatDatabase commits a foreground update and its replay event atomically.
// Returning an empty event kind makes an idempotent read without a write.
type ChatDatabase interface {
	CreateChat(context.Context, Chat) error
	Chats(context.Context) ([]Chat, error)
	Chat(context.Context, string) (Chat, error)
	UpdateChat(context.Context, string, func(*Chat) (string, error)) (Chat, error)
	ChatEvents(context.Context, string, int64) ([]ChatEvent, error)
	CreateChatUpload(context.Context, string, Upload) (Upload, error)
	ChatUpload(context.Context, string, string) (Upload, error)
}
