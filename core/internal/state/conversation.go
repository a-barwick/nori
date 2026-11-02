package state

import (
	"errors"
	"fmt"
	"reflect"
	"time"
)

var ErrConversationBusy = errors.New("conversation lease is held by another processor")
var ErrConversationConflict = errors.New("message ID already accepted with different text")
var ErrConversationStale = errors.New("conversation attempt superseded")

const ConversationLease = 30 * time.Second

type ConversationInput struct {
	ID           string            `firestore:"id"`
	Text         string            `firestore:"text"`
	Position     int64             `firestore:"position"`
	CreatedAt    time.Time         `firestore:"created_at"`
	Introduction bool              `firestore:"introduction,omitempty"`
	Event        *TaskEvent        `firestore:"event,omitempty"`
	TraceContext map[string]string `json:"-" firestore:"trace_context,omitempty"`
	// Attachments are the files sent with a user's message.
	Attachments []Attachment `firestore:"attachments,omitempty"`
}

type ConversationControl struct {
	ProtocolVersion int       `firestore:"protocol_version,omitempty"`
	UserReceived    int64     `firestore:"user_received,omitempty"`
	UserRouted      int64     `firestore:"user_routed,omitempty"`
	Introducing     bool      `firestore:"introducing,omitempty"`
	Received        int64     `firestore:"received"`
	Consumed        int64     `firestore:"consumed"`
	Active          string    `firestore:"active"`
	Owner           string    `firestore:"owner"`
	Fence           int64     `firestore:"fence"`
	LeaseUntil      time.Time `firestore:"lease_until"`
	LatestAt        time.Time `firestore:"latest_at"`
}

type ConversationAction struct {
	ID        string    `json:"id" firestore:"id"`
	Request   string    `json:"request" firestore:"request"`
	Bytes     int       `json:"bytes" firestore:"bytes"`
	Status    string    `json:"status" firestore:"status"`
	Error     string    `json:"error,omitempty" firestore:"error,omitempty"`
	CreatedAt time.Time `json:"created_at" firestore:"created_at"`
	Command   Command   `json:"command" firestore:"command"`
}

type ConversationAttempt struct {
	// ShowResults are saved results the outer model chose to show in its reply.
	ShowResults      []string             `firestore:"show_results,omitempty"`
	StructuredReply  bool                 `firestore:"structured_reply,omitempty"`
	ID               string               `firestore:"id"`
	TurnID           string               `firestore:"turn_id"`
	Number           int                  `firestore:"number"`
	InputIDs         []string             `firestore:"input_ids"`
	Model            string               `firestore:"model"`
	Status           string               `firestore:"status"`
	StartedAt        time.Time            `firestore:"started_at"`
	FinishedAt       *time.Time           `firestore:"finished_at,omitempty"`
	DurationMS       int64                `firestore:"duration_ms"`
	Hint             string               `firestore:"hint"`
	Reply            string               `firestore:"reply"`
	ProtocolInvalid  bool                 `firestore:"protocol_invalid"`
	Text             string               `firestore:"text"`
	Results          []string             `firestore:"results"`
	Actions          []ConversationAction `firestore:"actions"`
	Usage            []Usage              `firestore:"usage"`
	Error            string               `firestore:"error"`
	ContextTruncated bool                 `firestore:"context_truncated"`
	ContextTurns     int                  `firestore:"context_turns"`
	ContextBytes     int                  `firestore:"context_bytes"`
}

type ConversationTurn struct {
	ID          string              `firestore:"id"`
	Inputs      []ConversationInput `firestore:"inputs"`
	Through     int64               `firestore:"through"`
	UserThrough int64               `firestore:"user_through,omitempty"`
	Restarts    int                 `firestore:"restarts"`
	Frozen      bool                `firestore:"frozen"`
	StartedAt   time.Time           `firestore:"started_at"`
	FinishedAt  *time.Time          `firestore:"finished_at,omitempty"`
	Status      string              `firestore:"status"`
	Protocol    []string            `firestore:"protocol"`
	Context     []string            `firestore:"context"`
	Attempt     ConversationAttempt `firestore:"attempt"`
}

// ConversationFence identifies one processor's claim on the active turn.
type ConversationFence struct {
	Owner       string
	Fence       int64
	TurnID      string
	Through     int64
	UserThrough int64
	Frozen      bool
}

// Limits on what one conversation turn carries.
const (
	// TurnInputLimit and TurnInputBytes bound the new inputs one turn takes. The byte bound keeps the turn document
	// comfortably below Firestore's 1 MiB document limit.
	TurnInputLimit = 64
	TurnInputBytes = 128 << 10
	// RoutingContextBytes bounds the unresolved user inputs replayed to a new turn as context.
	RoutingContextBytes = 256 << 10
	// MaxTurnRestarts is the number of times new user input can restart a turn before its coverage freezes.
	MaxTurnRestarts = 3
	// RestartQuiet is how long the newest user input must sit before a restart takes in the waiting batch.
	RestartQuiet = time.Second
	// MessagePageSize is the number of messages one history page returns.
	MessagePageSize = 80
	// HistoryTurnsExamined, HistoryTurnLimit and HistoryProtocolBytes bound the completed turns replayed to the model.
	HistoryTurnsExamined = 25
	HistoryTurnLimit     = 24
	HistoryProtocolBytes = 192 << 10
	// TraceLimit bounds the attempts returned for debugging.
	TraceLimit = 60
)

func (in ConversationInput) IsUser() bool { return !in.Introduction && in.Event == nil }

// NewConversationControl is the control record of a conversation that has no saved control document yet.
func NewConversationControl() ConversationControl { return ConversationControl{ProtocolVersion: 2} }

// Normalize upgrades a control record saved before user input was counted separately from task events.
func (c *ConversationControl) Normalize() {
	if c.ProtocolVersion < 2 {
		c.UserReceived, c.UserRouted = c.Received, c.Consumed
		c.ProtocolVersion = 2
	}
}

// Normalize fills in the user coverage of a turn saved before it was recorded.
func (t *ConversationTurn) Normalize() {
	if t.UserThrough == 0 {
		t.UserThrough = userThrough(t.Inputs)
	}
}

// RoutingPending reports whether accepted user input is still waiting for the outer loop to route it.
func (c ConversationControl) RoutingPending() bool { return c.UserReceived > c.UserRouted }

func userThrough(inputs []ConversationInput) int64 {
	var through int64
	for _, in := range inputs {
		if in.IsUser() && in.Position > through {
			through = in.Position
		}
	}
	return through
}

// CheckOwned verifies that the fence still holds the conversation's lease.
func CheckOwned(c ConversationControl, f ConversationFence, now time.Time) error {
	if c.Owner != f.Owner || c.Fence != f.Fence || c.Active != f.TurnID || !c.LeaseUntil.After(now) {
		return ErrConversationBusy
	}
	return nil
}

// CheckFresh additionally rejects a turn that newer user input has superseded. A frozen turn keeps its coverage.
func CheckFresh(c ConversationControl, f ConversationFence, now time.Time) error {
	if err := CheckOwned(c, f, now); err != nil {
		return err
	}
	if StaleFor(c, f) {
		return ErrConversationStale
	}
	return nil
}

// StaleFor reports whether user input arrived that the fence's turn does not cover.
func StaleFor(c ConversationControl, f ConversationFence) bool {
	return !f.Frozen && c.UserReceived > f.UserThrough
}

// ReplayUserInput checks a repeated submission of an accepted message ID. The same text is a harmless replay.
func ReplayUserInput(saved ConversationInput, text string, attachments []string) error {
	if saved.Text != text || len(saved.Attachments) != len(attachments) {
		return ErrConversationConflict
	}
	for i, attachment := range saved.Attachments {
		if attachment.ID != attachments[i] {
			return ErrConversationConflict
		}
	}
	return nil
}

// AdmitUserInput numbers a new user message and returns the updated control record, the inbox input and the visible
// message to create in one transaction.
func AdmitUserInput(
	c ConversationControl, id, text string, attachments []Attachment, now time.Time, traceContext map[string]string,
) (ConversationControl, ConversationInput, Message) {
	c.Received++
	c.UserReceived = c.Received
	c.LatestAt = now
	input := ConversationInput{ID: id, Text: text, Position: c.Received, CreatedAt: now, TraceContext: traceContext, Attachments: attachments}
	parts := []Part{{Type: PartText, Text: text}}
	for _, attachment := range attachments {
		parts = append(parts, AttachmentPart(attachment))
	}
	message := Message{ID: UserMessageID(id), Role: "user", Text: text, Parts: parts, CreatedAt: now, TurnID: id}
	return c, input, message
}

// UserMessageID and AssistantMessageID name the visible messages of an input and of a turn.
func UserMessageID(inputID string) string     { return inputID + "-user" }
func AssistantMessageID(turnID string) string { return turnID + "-assistant" }
func TaskEventInputID(eventID string) string  { return "event-" + eventID }
func IntroductionInputID() string             { return "nori-introduction" }

// NeedsIntroduction reports whether a conversation with no messages should receive its introduction.
func NeedsIntroduction(c ConversationControl) bool { return c.Received == 0 }

// Introduce queues the introduction prompt as the first input. It shares admission ordering with real messages.
func Introduce(c ConversationControl, prompt string, now time.Time) (ConversationControl, ConversationInput) {
	c.Received = 1
	c.Introducing = true
	c.LatestAt = now
	return c, ConversationInput{ID: IntroductionInputID(), Text: prompt, Position: 1, CreatedAt: now, Introduction: true}
}

// InputBatch collects the new inputs for a turn, oldest first, stopping before the turn would grow too large. The
// first input is always taken.
type InputBatch struct {
	Inputs []ConversationInput
	bytes  int
}

func NewInputBatch() *InputBatch { return &InputBatch{Inputs: []ConversationInput{}} }

// Add takes the next input and reports whether the batch can keep growing.
func (b *InputBatch) Add(in ConversationInput) bool {
	size := len(in.Text)
	if in.Event != nil {
		size += len(in.Event.Text)
		for _, hold := range in.Event.Holds {
			size += len(hold.Question)
		}
	}
	if b.bytes+size > TurnInputBytes && len(b.Inputs) > 0 {
		return false
	}
	b.bytes += size
	b.Inputs = append(b.Inputs, in)
	return true
}

// RoutingContext collects the user inputs of earlier turns that were never routed.
type RoutingContext struct {
	Inputs []ConversationInput
	bytes  int
}

// Add takes an input in position order. Events and introductions are not user input and are skipped.
func (r *RoutingContext) Add(in ConversationInput) error {
	if !in.IsUser() {
		return nil
	}
	r.bytes += len(in.Text)
	if r.bytes > RoutingContextBytes {
		return fmt.Errorf("unresolved conversation inputs exceed the routing context limit")
	}
	r.Inputs = append(r.Inputs, in)
	return nil
}

// NeedsUnrouted reports whether a new turn must also replay earlier user inputs that were never routed. A failed
// routing attempt stays unresolved until the next user turn, and its inputs are replayed as context without creating
// another visible message.
func NeedsUnrouted(c ConversationControl, inputs []ConversationInput) bool {
	return userThrough(inputs) > 0 && c.UserRouted < c.Consumed
}

// CanAcquire reports ErrConversationBusy while another processor holds the lease.
func CanAcquire(c ConversationControl, now time.Time) error {
	if c.Owner != "" && c.LeaseUntil.After(now) {
		return ErrConversationBusy
	}
	return nil
}

// Idle reports whether the conversation has no active turn and nothing waiting.
func Idle(c ConversationControl) bool { return c.Active == "" && c.Received <= c.Consumed }

// NewTurn starts a turn over the waiting inputs. unresolved are earlier user inputs replayed ahead of them.
func NewTurn(id string, inputs, unresolved []ConversationInput, c ConversationControl, now time.Time) ConversationTurn {
	t := ConversationTurn{ID: id, Inputs: inputs, Through: inputs[len(inputs)-1].Position, StartedAt: now, Status: "running"}
	if len(unresolved) > 0 {
		t.Inputs = append(unresolved, inputs...)
	}
	t.UserThrough = max(userThrough(t.Inputs), min(c.UserReceived, c.Consumed))
	t.Frozen = t.Through < c.Received
	return t
}

// Lease gives owner the conversation under a new fence.
func Lease(c ConversationControl, owner string, now time.Time) ConversationControl {
	c.Owner = owner
	c.Fence++
	c.LeaseUntil = now.Add(ConversationLease)
	return c
}

// Renew extends the lease and reports whether newer user input has made the turn stale.
func Renew(c ConversationControl, f ConversationFence, now time.Time) (ConversationControl, bool, error) {
	if err := CheckOwned(c, f, now); err != nil {
		return c, false, err
	}
	c.LeaseUntil = now.Add(ConversationLease)
	return c, StaleFor(c, f), nil
}

// Release gives up the lease without finishing the turn.
func Release(c ConversationControl, f ConversationFence, now time.Time) (ConversationControl, error) {
	if err := CheckOwned(c, f, now); err != nil {
		return c, err
	}
	c.Owner = ""
	c.LeaseUntil = time.Time{}
	return c, nil
}

// RestartWait reports whether a turn can take in newer user input, and how long to wait first. Input is taken in
// only after one quiet second, and never into a frozen turn.
func RestartWait(c ConversationControl, t ConversationTurn, now time.Time) (restart bool, wait time.Duration) {
	if c.UserReceived <= t.UserThrough || t.Frozen {
		return false, 0
	}
	wait = c.LatestAt.Add(RestartQuiet).Sub(now)
	return wait <= 0, wait
}

// Restart is the outcome of folding the waiting inputs into a turn.
type Restart struct {
	Turn ConversationTurn
	// Replaced is set when the turn took a new batch. When false the waiting suffix could not fit, and the turn only
	// froze its original coverage.
	Replaced bool
	// Superseded is the finished record of the abandoned attempt, when there was one.
	Superseded *ConversationAttempt
}

// RestartTurn incorporates the full waiting batch. The fourth attempt freezes its coverage, and later input stays
// queued for the next turn.
func RestartTurn(c ConversationControl, t ConversationTurn, inputs []ConversationInput, now time.Time) (Restart, error) {
	if len(inputs) == 0 {
		return Restart{}, errors.New("no waiting conversation inputs to incorporate")
	}
	through := inputs[len(inputs)-1].Position
	if through == t.Through {
		t.Frozen = true
		return Restart{Turn: t}, nil
	}
	var superseded *ConversationAttempt
	if t.Attempt.ID != "" {
		accepted := false
		for _, a := range t.Attempt.Actions {
			accepted = accepted || a.Status == "accepted"
		}
		if accepted && t.Attempt.Reply != "" {
			t.Context = append(t.Context, t.Attempt.Reply)
			t.Context = append(t.Context, t.Attempt.Results...)
		}
		attempt := t.Attempt
		attempt.Status = "superseded"
		attempt.Error = ""
		attempt.FinishedAt = &now
		attempt.DurationMS = now.Sub(attempt.StartedAt).Milliseconds()
		superseded = &attempt
	}
	t.Inputs = inputs
	t.Through = through
	t.UserThrough = max(userThrough(inputs), min(c.UserReceived, c.Consumed))
	t.Restarts++
	t.Frozen = t.Restarts >= MaxTurnRestarts || t.Through < c.Received
	t.Attempt = ConversationAttempt{}
	return Restart{Turn: t, Replaced: true, Superseded: superseded}, nil
}

// CompleteTurn finishes the turn with its published reply. It returns the updated control record, the completed turn
// and the assistant message. User input is marked routed unless the attempt failed.
func CompleteTurn(c ConversationControl, t ConversationTurn, text string, now time.Time) (ConversationControl, ConversationTurn, Message) {
	t.Status = "completed"
	t.FinishedAt = &now
	c.Consumed = t.Through
	if t.Attempt.Status != "error" {
		c.UserRouted = max(c.UserRouted, userThrough(t.Inputs))
	}
	c.Introducing = false
	c.Active = ""
	c.Owner = ""
	c.LeaseUntil = time.Time{}
	message := Message{ID: AssistantMessageID(t.ID), Role: "assistant", Text: text, Parts: TextParts(text), CreatedAt: now, TurnID: t.ID}
	return c, t, message
}

// EnqueueTaskEvent adds one task event to the conversation inbox. It returns the updated control record and the inbox
// input to create. A zero CreatedAt takes now.
func EnqueueTaskEvent(
	c ConversationControl, event TaskEvent, now time.Time, traceContext map[string]string,
) (ConversationControl, ConversationInput) {
	c.Received++
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	return c, ConversationInput{
		ID: TaskEventInputID(event.ID), Position: c.Received, CreatedAt: event.CreatedAt, Event: &event, TraceContext: traceContext,
	}
}

// ClassifyEventReplay decides what finding a saved inbox input under a task event's ID means. A nil result means the
// same event is already in the inbox and nothing needs to be written. Any other saved input, or an event whose
// content differs, is a conflict.
func ClassifyEventReplay(prior ConversationInput, event TaskEvent) error {
	if prior.Event == nil {
		return ErrDuplicate
	}
	old := *prior.Event
	old.CreatedAt, event.CreatedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(old, event) {
		return ErrDuplicate
	}
	return nil
}

// MessagePage returns one page of messages in time order from a newest-first read of up to MessagePageSize+1
// messages, and the cursor for the next older page. The cursor is empty on the last page.
func MessagePage(newestFirst []Message) (page []Message, next string) {
	if len(newestFirst) > MessagePageSize {
		newestFirst = newestFirst[:MessagePageSize]
		next = newestFirst[len(newestFirst)-1].ID
	}
	return Chronological(append([]Message{}, newestFirst...)), next
}

// HistoryWindow collects completed turns from a newest-first read, within the replay bounds.
type HistoryWindow struct {
	Turns     []ConversationTurn
	Truncated bool
	examined  int
	size      int
}

func NewHistoryWindow() *HistoryWindow { return &HistoryWindow{Turns: []ConversationTurn{}} }

// Add takes the next newest turn and reports whether to keep reading.
func (w *HistoryWindow) Add(t ConversationTurn) bool {
	w.examined++
	if w.examined >= HistoryTurnsExamined {
		w.Truncated = true
	}
	if t.Status != "completed" {
		return true
	}
	n := 0
	for _, p := range t.Protocol {
		n += len(p)
	}
	if w.size+n > HistoryProtocolBytes || len(w.Turns) >= HistoryTurnLimit {
		w.Truncated = true
		return false
	}
	w.size += n
	w.Turns = append(w.Turns, t)
	return true
}

// Chronological returns the collected turns oldest first.
func (w *HistoryWindow) Chronological() []ConversationTurn { return Chronological(w.Turns) }
