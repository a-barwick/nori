package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"
)

var ErrConversationBusy = errors.New("conversation lease is held by another processor")
var ErrConversationConflict = errors.New("message ID already accepted with different text")
var ErrConversationStale = errors.New("conversation attempt superseded")

const (
	ConversationLease = 30 * time.Second
	SchedulerLease    = 30 * time.Second
)

type AttemptStatus string

const (
	AttemptRunning    AttemptStatus = "running"
	AttemptCompleted  AttemptStatus = "completed"
	AttemptError      AttemptStatus = "error"
	AttemptSuperseded AttemptStatus = "superseded"
)

type TurnStatus string

const (
	TurnRunning   TurnStatus = "running"
	TurnCompleted TurnStatus = "completed"
)

type ActionStatus string

const (
	ActionPending  ActionStatus = "pending"
	ActionAccepted ActionStatus = "accepted"
	ActionRejected ActionStatus = "rejected"
)

type CheckpointPhase string

const (
	CheckpointPreparingReply  CheckpointPhase = "preparing_reply"
	CheckpointRequestingReply CheckpointPhase = "requesting_reply"
	CheckpointProposedReply   CheckpointPhase = "proposed_reply"
	CheckpointRequestingPlan  CheckpointPhase = "requesting_plan"
	CheckpointResolvedReply   CheckpointPhase = "resolved_reply"
)

type ConversationInput struct {
	ID             string            `firestore:"id"`
	Text           string            `firestore:"text"`
	Position       int64             `firestore:"position"`
	CreatedAt      time.Time         `firestore:"created_at"`
	Introduction   bool              `firestore:"introduction,omitempty"`
	Retry          bool              `firestore:"retry,omitempty"`
	Event          *TaskEvent        `firestore:"event,omitempty"`
	TraceContext   map[string]string `json:"-" firestore:"trace_context,omitempty"`
	Attachments    []Attachment      `firestore:"attachments,omitempty"`
	ModelSelection *ModelSelection   `json:"model_selection,omitempty" firestore:"model_selection,omitempty"`
}

type ConversationControl struct {
	UserReceived     int64     `firestore:"user_received,omitempty"`
	UserRouted       int64     `firestore:"user_routed,omitempty"`
	Introducing      bool      `firestore:"introducing,omitempty"`
	Received         int64     `firestore:"received"`
	Consumed         int64     `firestore:"consumed"`
	Active           string    `firestore:"active"`
	Owner            string    `firestore:"owner"`
	Fence            int64     `firestore:"fence"`
	LeaseUntil       time.Time `firestore:"lease_until"`
	LatestAt         time.Time `firestore:"latest_at"`
	ContextFrom      time.Time `firestore:"context_from,omitempty"`
	FailedMessageIDs []string  `firestore:"failed_message_ids,omitempty"`
}

type ConversationAction struct {
	ID        string       `json:"id" firestore:"id"`
	Request   string       `json:"request" firestore:"request"`
	Bytes     int          `json:"bytes" firestore:"bytes"`
	Status    ActionStatus `json:"status" firestore:"status"`
	Error     string       `json:"error,omitempty" firestore:"error,omitempty"`
	CreatedAt time.Time    `json:"created_at" firestore:"created_at"`
	Command   Command      `json:"command" firestore:"command"`
}

type ConversationAttempt struct {
	ShowResults      []string                     `firestore:"show_results,omitempty"`
	StructuredReply  bool                         `firestore:"structured_reply,omitempty"`
	ID               string                       `firestore:"id"`
	TurnID           string                       `firestore:"turn_id"`
	Number           int                          `firestore:"number"`
	InputIDs         []string                     `firestore:"input_ids"`
	Model            string                       `firestore:"model"`
	ServedModel      string                       `firestore:"served_model,omitempty"`
	ModelSelection   *ModelSelection              `firestore:"model_selection,omitempty"`
	Status           AttemptStatus                `firestore:"status"`
	StartedAt        time.Time                    `firestore:"started_at"`
	FinishedAt       *time.Time                   `firestore:"finished_at,omitempty"`
	DurationMS       int64                        `firestore:"duration_ms"`
	Hint             string                       `firestore:"hint"`
	Reply            string                       `firestore:"reply"`
	ProtocolInvalid  bool                         `firestore:"protocol_invalid"`
	Text             string                       `firestore:"text"`
	Results          []string                     `firestore:"results"`
	Actions          []ConversationAction         `firestore:"actions"`
	Usage            []Usage                      `firestore:"usage"`
	Error            string                       `firestore:"error"`
	Failure          *MessageError                `firestore:"failure,omitempty"`
	ContextReduced   bool                         `firestore:"context_reduced,omitempty"`
	ContextTruncated bool                         `firestore:"context_truncated"`
	ContextTurns     int                          `firestore:"context_turns"`
	ContextBytes     int                          `firestore:"context_bytes"`
	Memory           []MemoryChange               `firestore:"memory,omitempty"`
	ModelCheckpoint  *ConversationModelCheckpoint `json:"-" firestore:"model_checkpoint,omitempty"`
}

type ConversationModelCheckpoint struct {
	Phase      CheckpointPhase `firestore:"phase"`
	Message    string          `firestore:"message,omitempty"`
	Text       string          `firestore:"text,omitempty"`
	Structured bool            `firestore:"structured,omitempty"`
}

type ConversationTurn struct {
	ID             string              `firestore:"id"`
	Inputs         []ConversationInput `firestore:"inputs"`
	Through        int64               `firestore:"through"`
	UserThrough    int64               `firestore:"user_through,omitempty"`
	Restarts       int                 `firestore:"restarts"`
	Frozen         bool                `firestore:"frozen"`
	StartedAt      time.Time           `firestore:"started_at"`
	FinishedAt     *time.Time          `firestore:"finished_at,omitempty"`
	Status         TurnStatus          `firestore:"status"`
	Protocol       []string            `firestore:"protocol"`
	Context        []string            `firestore:"context"`
	ModelSelection *ModelSelection     `firestore:"model_selection,omitempty"`
	Attempt        ConversationAttempt `firestore:"attempt"`
}

type ConversationFence struct {
	Owner       string
	Fence       int64
	TurnID      string
	Through     int64
	UserThrough int64
	Frozen      bool
}

const (
	TurnInputLimit       = 64
	TurnInputBytes       = 128 << 10
	RoutingContextBytes  = 256 << 10
	MaxTurnRestarts      = 3
	RestartQuiet         = time.Second
	MessagePageSize      = 80
	HistoryTurnsExamined = 64
	TraceLimit           = 60
)

func (in ConversationInput) IsUser() bool {
	return !in.Introduction && in.Event == nil && !in.Retry
}

func hasRetryInput(inputs []ConversationInput) bool {
	for _, in := range inputs {
		if in.Retry {
			return true
		}
	}
	return false
}

func (c ConversationControl) RoutingPending() bool { return c.UserReceived > c.UserRouted }

func (c ConversationControl) Pending() bool { return c.Received > c.Consumed }

func (c ConversationControl) Processing(now time.Time) bool {
	return c.Active != "" && c.LeaseUntil.After(now)
}

func userThrough(inputs []ConversationInput) int64 {
	var through int64
	for _, in := range inputs {
		if in.IsUser() && in.Position > through {
			through = in.Position
		}
	}
	return through
}

func CheckOwned(c ConversationControl, f ConversationFence, now time.Time) error {
	if c.Owner != f.Owner || c.Fence != f.Fence || c.Active != f.TurnID || !c.LeaseUntil.After(now) {
		return ErrConversationBusy
	}
	return nil
}

func CheckFresh(c ConversationControl, f ConversationFence, now time.Time) error {
	if err := CheckOwned(c, f, now); err != nil {
		return err
	}
	if StaleFor(c, f) {
		return ErrConversationStale
	}
	return nil
}

func StaleFor(c ConversationControl, f ConversationFence) bool {
	return !f.Frozen && c.UserReceived > f.UserThrough
}

func ReplayUserInputWithModel(saved ConversationInput, text string, attachments []string, sel *ModelSelection) error {
	if saved.Text != text || len(saved.Attachments) != len(attachments) || !ModelSelectionEqual(saved.ModelSelection, sel) {
		return ErrConversationConflict
	}
	for i, attachment := range saved.Attachments {
		if attachment.ID != attachments[i] {
			return ErrConversationConflict
		}
	}
	return nil
}

func AdmitUserInputWithModel(
	c ConversationControl, id, text string, attachments []Attachment, sel *ModelSelection, now time.Time, traceContext map[string]string,
) (ConversationControl, ConversationInput, Message) {
	c.Received++
	c.UserReceived = c.Received
	c.LatestAt = now
	input := ConversationInput{
		ID: id, Text: text, Position: c.Received, CreatedAt: now,
		TraceContext: traceContext, Attachments: attachments, ModelSelection: sel.Clone(),
	}
	parts := []Part{{Type: PartText, Text: text}}
	for _, attachment := range attachments {
		parts = append(parts, AttachmentPart(attachment))
	}
	message := Message{ID: UserMessageID(id), Role: "user", Text: text, Parts: parts, CreatedAt: now, TurnID: id}
	return c, input, message
}

func UserMessageID(inputID string) string     { return inputID + "-user" }
func AssistantMessageID(turnID string) string { return turnID + "-assistant" }
func TaskEventInputID(eventID string) string  { return "event-" + eventID }
func IntroductionInputID() string             { return "nori-introduction" }

func NeedsIntroduction(c ConversationControl) bool { return c.Received == 0 }

func Introduce(c ConversationControl, prompt string, now time.Time) (ConversationControl, ConversationInput) {
	c.Received = 1
	c.Introducing = true
	c.LatestAt = now
	return c, ConversationInput{ID: IntroductionInputID(), Text: prompt, Position: 1, CreatedAt: now, Introduction: true}
}

type InputBatch struct {
	Inputs    []ConversationInput
	bytes     int
	hasUser   bool
	selection *ModelSelection
}

func NewInputBatch() *InputBatch { return &InputBatch{Inputs: []ConversationInput{}} }

func (b *InputBatch) Add(in ConversationInput) bool {
	if in.IsUser() {
		if b.hasUser && !ModelSelectionEqual(b.selection, in.ModelSelection) {
			return false
		}
	}
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
	if in.IsUser() && !b.hasUser {
		b.hasUser = true
		b.selection = in.ModelSelection.Clone()
	}
	b.bytes += size
	b.Inputs = append(b.Inputs, in)
	return true
}

type RoutingContext struct {
	Inputs []ConversationInput
	bytes  int
}

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

func AdmitRetryInput(c ConversationControl, id string, now time.Time, traceContext map[string]string) (ConversationControl, ConversationInput) {
	c.Received++
	c.LatestAt = now
	return c, ConversationInput{ID: id, Position: c.Received, CreatedAt: now, Retry: true, TraceContext: traceContext}
}

func NeedsUnrouted(c ConversationControl, inputs []ConversationInput) bool {
	return (userThrough(inputs) > 0 || hasRetryInput(inputs)) && c.UserRouted < c.Consumed
}

func CanAcquire(c ConversationControl, now time.Time) error {
	if c.Owner != "" && c.LeaseUntil.After(now) {
		return ErrConversationBusy
	}
	return nil
}

func Idle(c ConversationControl) bool { return c.Active == "" && !c.Pending() }

func turnModelSelection(inputs []ConversationInput) *ModelSelection {
	for i := len(inputs) - 1; i >= 0; i-- {
		if inputs[i].IsUser() && !inputs[i].ModelSelection.IsEmpty() {
			return inputs[i].ModelSelection.Clone()
		}
	}
	return nil
}

func hasUserInput(inputs []ConversationInput) bool {
	for _, in := range inputs {
		if in.IsUser() {
			return true
		}
	}
	return false
}

func NewTurn(id string, inputs, unresolved []ConversationInput, c ConversationControl, now time.Time) ConversationTurn {
	t := ConversationTurn{
		ID: id, Inputs: inputs, Through: inputs[len(inputs)-1].Position,
		StartedAt: now, Status: TurnRunning, ModelSelection: turnModelSelection(inputs),
	}
	if len(unresolved) > 0 {
		t.Inputs = append(unresolved, inputs...)
		if !hasUserInput(inputs) && t.ModelSelection.IsEmpty() {
			t.ModelSelection = turnModelSelection(unresolved)
		}
	}
	t.UserThrough = max(userThrough(t.Inputs), min(c.UserReceived, c.Consumed))
	t.Frozen = t.Through < c.Received
	return t
}

func Lease(c ConversationControl, owner string, now time.Time) ConversationControl {
	c.Owner = owner
	c.Fence++
	c.LeaseUntil = now.Add(ConversationLease)
	return c
}

func Renew(c ConversationControl, f ConversationFence, now time.Time) (ConversationControl, bool, error) {
	if err := CheckOwned(c, f, now); err != nil {
		return c, false, err
	}
	c.LeaseUntil = now.Add(ConversationLease)
	return c, StaleFor(c, f), nil
}

func Release(c ConversationControl, f ConversationFence, now time.Time) (ConversationControl, error) {
	if err := CheckOwned(c, f, now); err != nil {
		return c, err
	}
	c.Owner = ""
	c.LeaseUntil = time.Time{}
	return c, nil
}

func RestartWait(c ConversationControl, t ConversationTurn, now time.Time) (restart bool, wait time.Duration) {
	if c.UserReceived <= t.UserThrough || t.Frozen {
		return false, 0
	}
	wait = c.LatestAt.Add(RestartQuiet).Sub(now)
	return wait <= 0, wait
}

type Restart struct {
	Turn       ConversationTurn
	Replaced   bool
	Superseded *ConversationAttempt
}

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
			accepted = accepted || a.Status == ActionAccepted
		}
		if accepted && t.Attempt.Reply != "" {
			t.Context = append(t.Context, t.Attempt.Reply)
			t.Context = append(t.Context, t.Attempt.Results...)
			if len(t.Attempt.Memory) > 0 {
				note, _ := json.Marshal(map[string]string{"role": "system", "content": "The preceding response was " +
					"superseded by newer input before it was published. Its task commands were accepted, but none of its " +
					"remember or forget changes were applied. Make any of them again if they still apply."})
				t.Context = append(t.Context, string(note))
			}
		}
		attempt := t.Attempt
		attempt.Status = AttemptSuperseded
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

func CompleteTurn(c ConversationControl, t ConversationTurn, text string, now time.Time) (ConversationControl, ConversationTurn, Message) {
	t.Status = TurnCompleted
	t.FinishedAt = &now
	c.Consumed = t.Through
	if t.Attempt.Status != AttemptError {
		c.UserRouted = max(c.UserRouted, userThrough(t.Inputs))
	}
	c.Introducing = false
	c.Active = ""
	c.Owner = ""
	c.LeaseUntil = time.Time{}
	message := Message{ID: AssistantMessageID(t.ID), Role: "assistant", Text: text, Parts: TextParts(text), CreatedAt: now, TurnID: t.ID}
	if t.Attempt.Status == AttemptError {
		message.Status = MessageStatusFailed
		if t.Attempt.Failure != nil {
			cp := *t.Attempt.Failure
			message.Error = &cp
		} else {
			message.Error = &MessageError{
				Code:      "reply_failed",
				Message:   text,
				Retryable: userThrough(t.Inputs) > 0,
			}
		}
		if userThrough(t.Inputs) > 0 {
			seen := false
			for _, id := range c.FailedMessageIDs {
				if id == message.ID {
					seen = true
					break
				}
			}
			if !seen {
				c.FailedMessageIDs = append(c.FailedMessageIDs, message.ID)
			}
		}
	}
	return c, t, message
}

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

func MessagePage(newestFirst []Message) (page []Message, next string) {
	if len(newestFirst) > MessagePageSize {
		newestFirst = newestFirst[:MessagePageSize]
		next = newestFirst[len(newestFirst)-1].ID
	}
	return Chronological(append([]Message{}, newestFirst...)), next
}

type HistoryWindow struct {
	Turns     []ConversationTurn
	Truncated bool
	size      int
}

func NewHistoryWindow() *HistoryWindow { return &HistoryWindow{Turns: []ConversationTurn{}} }

func (w *HistoryWindow) Add(t ConversationTurn) bool {
	if t.Status != TurnCompleted {
		return true
	}
	n := TurnBytes(t)
	if w.size+n > RecentTurnBytes || len(w.Turns) >= RecentTurnLimit {
		w.Truncated = true
		return false
	}
	w.size += n
	w.Turns = append(w.Turns, t)
	return true
}

func (w *HistoryWindow) Chronological() []ConversationTurn { return Chronological(w.Turns) }
