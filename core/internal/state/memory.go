package state

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Memory is one thing Austin asked Nori to remember. It is a preference or fact, never permission: nothing reads it
// when deciding whether an action is approved.
type Memory struct {
	ID   string `json:"id" firestore:"id"`
	Text string `json:"text" firestore:"text"`
	// About names who the memory is about: Austin, or another person he named.
	About string `json:"about" firestore:"about"`
	// SourceInputID and SourceText are the message that asked for it, in Austin's words.
	SourceInputID string    `json:"source_input_id" firestore:"source_input_id"`
	SourceText    string    `json:"source_text" firestore:"source_text"`
	CreatedAt     time.Time `json:"created_at" firestore:"created_at"`
	UpdatedAt     time.Time `json:"updated_at" firestore:"updated_at"`
}

// MemoryChange is one remember or forget the outer model asked for. It is applied when the turn's reply is published,
// in the same transaction, so a restart can't apply it twice or apply it without the reply that reports it.
type MemoryChange struct {
	Kind string `json:"kind" firestore:"kind"`
	// ID is the memory written or deleted. A correction keeps the ID of the memory it replaces.
	ID    string `json:"id" firestore:"id"`
	Text  string `json:"text,omitempty" firestore:"text,omitempty"`
	About string `json:"about,omitempty" firestore:"about,omitempty"`
	// Replaces is set when a remember corrects an existing memory.
	Replaces bool `json:"replaces,omitempty" firestore:"replaces,omitempty"`
	// Error says why the change was refused. A turn with any refused change applies none of them.
	Error string `json:"error,omitempty" firestore:"error,omitempty"`
}

const (
	MemoryRemember = "remember"
	MemoryForget   = "forget"
	// MaxMemoryText and MaxMemoryAbout bound one memory. MemoryBytes bounds all saved memory text, which keeps the
	// memories the outer model sees under the context budget.
	MaxMemoryText  = 300
	MaxMemoryAbout = 80
	MemoryBytes    = 4 << 10
	// MemorySourceBytes bounds the copy of Austin's message kept with a memory.
	MemorySourceBytes = 1 << 10
)

// PlanMemoryChanges checks a turn's changes against the saved memories. It fills in Error on each change that can't be
// applied and reports whether all of them can.
func PlanMemoryChanges(saved []Memory, changes []MemoryChange) bool {
	byID := map[string]Memory{}
	total := 0
	for _, m := range saved {
		byID[m.ID] = m
		total += len(m.Text)
	}
	ok := true
	for i := range changes {
		c := &changes[i]
		old, exists := byID[c.ID]
		switch {
		case c.Kind == MemoryForget && !exists:
			c.Error = "there is no saved memory with that ID"
		case c.Kind == MemoryForget:
			delete(byID, c.ID)
			total -= len(old.Text)
		case c.Kind != MemoryRemember:
			c.Error = "unknown memory change"
		case c.Replaces && !exists:
			c.Error = "there is no saved memory with that ID to correct"
		case strings.TrimSpace(c.Text) == "" || len(c.Text) > MaxMemoryText || !utf8.ValidString(c.Text):
			c.Error = fmt.Sprintf("a memory needs 1 to %d bytes of text", MaxMemoryText)
		case len(c.About) > MaxMemoryAbout:
			c.Error = fmt.Sprintf("who a memory is about can be at most %d bytes", MaxMemoryAbout)
		case total-len(old.Text)+len(c.Text) > MemoryBytes:
			c.Error = "memory is full; ask Austin which saved memory to forget"
		default:
			total += len(c.Text) - len(old.Text)
			byID[c.ID] = Memory{ID: c.ID, Text: c.Text}
		}
		ok = ok && c.Error == ""
	}
	return ok
}

// ApplyMemoryChange returns the memory a remember writes. existing is the saved memory it corrects, if any.
func ApplyMemoryChange(c MemoryChange, existing *Memory, source ConversationInput, now time.Time) Memory {
	m := Memory{ID: c.ID, Text: c.Text, About: c.About, SourceInputID: source.ID, SourceText: source.Text, CreatedAt: now, UpdatedAt: now}
	if m.About == "" {
		m.About = "Austin"
	}
	if len(m.SourceText) > MemorySourceBytes {
		limit := MemorySourceBytes
		for limit > 0 && !utf8.RuneStart(m.SourceText[limit]) {
			limit--
		}
		m.SourceText = m.SourceText[:limit]
	}
	if existing != nil {
		m.CreatedAt = existing.CreatedAt
	}
	return m
}

// SourceInput is the user message a turn's memory changes cite: its last one.
func SourceInput(inputs []ConversationInput) ConversationInput {
	var source ConversationInput
	for _, in := range inputs {
		if in.IsUser() {
			source = in
		}
	}
	return source
}
