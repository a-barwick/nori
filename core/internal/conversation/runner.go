// Package conversation owns durable outer turns. Task execution stays in the
// existing Firestore/Temporal worker path.
package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"nori/internal/state"
)

var ErrBusy = state.ErrConversationBusy
var ErrConflict = state.ErrConversationConflict

type Input struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}
type Message = state.Message
type Dispatch struct {
	ID        string    `json:"id"`
	Request   string    `json:"request"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}
type ConversationStatus struct {
	Introducing    bool     `json:"introducing"`
	Pending        bool     `json:"pending"`
	Processing     bool     `json:"processing"`
	Revision       int64    `json:"revision"`
	Consumed       int64    `json:"consumed"`
	TurnID         string   `json:"turn_id,omitempty"`
	InputIDs       []string `json:"input_ids"`
	RestartCount   int      `json:"restart_count"`
	Frozen         bool     `json:"frozen"`
	RoutingBlocked bool     `json:"routing_blocked"`
}
type Trace struct {
	ID               string                     `json:"id"`
	TurnID           string                     `json:"turn_id"`
	Attempt          int                        `json:"attempt"`
	InputIDs         []string                   `json:"input_ids"`
	Model            string                     `json:"model"`
	Status           string                     `json:"status"`
	StartedAt        time.Time                  `json:"started_at"`
	FinishedAt       *time.Time                 `json:"finished_at,omitempty"`
	DurationMS       int64                      `json:"duration_ms"`
	Classification   json.RawMessage            `json:"classification_hint,omitempty"`
	Actions          []state.ConversationAction `json:"actions"`
	Usage            []state.Usage              `json:"usage"`
	Error            string                     `json:"error,omitempty"`
	ContextTruncated bool                       `json:"context_truncated"`
	ContextTurns     int                        `json:"context_turns"`
	ContextBytes     int                        `json:"context_bytes"`
}
type Session struct {
	state.Session
	Dispatches     []Dispatch         `json:"dispatches"`
	Classification *Classification    `json:"classification_hint,omitempty"`
	Conversation   ConversationStatus `json:"conversation"`
	Traces         []Trace            `json:"traces"`
	NextCursor     string             `json:"next_cursor,omitempty"`
}
type Runner struct {
	model      *Model
	classifier *Classifier
	tasks      *Tasks
	process    sync.Mutex
}

func New(model *Model, classifier *Classifier, tasks *Tasks) *Runner {
	return &Runner{model: model, classifier: classifier, tasks: tasks}
}

// HandleInput only accepts the idempotent inbox item; provider I/O is performed
// by Process independently of the submitting HTTP request.
func (r *Runner) HandleInput(ctx context.Context, input Input) (Session, error) {
	if _, e := uuid.Parse(input.ID); e != nil {
		return Session{}, fmt.Errorf("message ID must be a UUID")
	}
	if strings.TrimSpace(input.Message) == "" || len(input.Message) > 8192 {
		return Session{}, fmt.Errorf("message must contain 1–8192 bytes")
	}
	if e := r.tasks.Store.AcceptConversationInput(ctx, input.ID, input.Message); e != nil {
		return Session{}, e
	}
	return r.Snapshot(ctx)
}
func fenceFor(c state.ConversationControl, t state.ConversationTurn) state.ConversationFence {
	return state.ConversationFence{Owner: c.Owner, Fence: c.Fence, TurnID: t.ID, Through: t.Through, UserThrough: t.UserThrough, Frozen: t.Frozen}
}

// Process drains durable work. A live competing lease is retryable so a queue
// wake never acknowledges away the only remaining notification.
func (r *Runner) Process(ctx context.Context) error {
	if !r.process.TryLock() {
		return ErrBusy
	}
	defer r.process.Unlock()
	for {
		c, t, e := r.tasks.Store.AcquireConversation(ctx, uuid.NewString(), uuid.NewString())
		if e != nil {
			return e
		}
		if t == nil {
			return nil
		}
		f := fenceFor(c, *t)
		e = r.processTurn(ctx, c, *t)
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = r.tasks.Store.ReleaseConversation(cleanup, f)
		cancel()
		if e != nil {
			return e
		}
	}
}
func attemptEnd(a *state.ConversationAttempt, statusText, detail string) {
	now := time.Now().UTC()
	a.Status = statusText
	a.Error = detail
	a.FinishedAt = &now
	a.DurationMS = now.Sub(a.StartedAt).Milliseconds()
}
func (r *Runner) checkpoint(ctx context.Context, f state.ConversationFence, t state.ConversationTurn) error {
	return r.tasks.Store.SaveConversation(ctx, f, t)
}
func attemptUsage(a *state.ConversationAttempt, usage state.Usage) {
	for i := range a.Usage {
		if a.Usage[i].ID == usage.ID {
			a.Usage[i] = usage
			return
		}
	}
	a.Usage = append(a.Usage, usage)
}
func (r *Runner) recordUsage(inputID string, usage state.Usage) error {
	usage.InputID = inputID
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.tasks.Store.RecordUsage(ctx, usage)
}
func (r *Runner) processTurn(ctx context.Context, c state.ConversationControl, t state.ConversationTurn) error {
	for {
		f := fenceFor(c, t)
		stale, e := r.tasks.Store.RenewConversation(ctx, f)
		if e != nil {
			return e
		}
		if stale {
			// Refresh accepted receipts before the restart transaction records the
			// superseded attempt and carries its accepted tool protocol forward.
			if t.Attempt.ID != "" {
				if e = r.recoverActions(ctx, &t); e != nil {
					return e
				}
			}
			var wait time.Duration
			t, wait, e = r.tasks.Store.RestartConversation(ctx, f, t)
			if e != nil {
				return e
			}
			if wait > 0 {
				timer := time.NewTimer(min(wait, 500*time.Millisecond))
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
				continue
			}
			f = fenceFor(c, t)
		}
		if t.Attempt.ID == "" {
			a := state.ConversationAttempt{ID: fmt.Sprintf("%s-attempt-%d", t.ID, t.Restarts+1), TurnID: t.ID, Number: t.Restarts + 1, Model: r.model.Name, Status: "running", StartedAt: time.Now().UTC(), Actions: []state.ConversationAction{}, Usage: []state.Usage{}}
			for _, in := range t.Inputs {
				a.InputIDs = append(a.InputIDs, in.ID)
			}
			t.Attempt = a
			if e = r.checkpoint(ctx, f, t); e != nil {
				return e
			}
		}
		f = fenceFor(c, t)
		if t.Attempt.Reply == "" && t.Attempt.Status != "error" {
			if e = r.call(ctx, f, &t); e != nil {
				return e
			}
			// Freshness is checked again after the provider result was durably saved.
			continueIfStale, e := r.tasks.Store.RenewConversation(ctx, f)
			if e != nil {
				return e
			}
			if continueIfStale {
				continue
			}
		}
		if e = r.recoverActions(ctx, &t); e != nil {
			return e
		}
		if e = r.dispatchPending(ctx, f, &t); errors.Is(e, state.ErrConversationStale) {
			continue
		} else if e != nil {
			return e
		}
		stale, e = r.tasks.Store.RenewConversation(ctx, f)
		if e != nil {
			return e
		}
		if stale {
			continue
		}
		text := t.Attempt.Text
		for _, a := range t.Attempt.Actions {
			if a.Status == "rejected" {
				text = "I couldn't apply this set of task changes: " + a.Error + ". No commands in this set were accepted."
				break
			}
		}
		if t.Attempt.Status != "error" {
			attemptEnd(&t.Attempt, "completed", "")
		}
		buildProtocol(&t, text)
		e = r.tasks.Store.PublishConversation(ctx, f, t, text)
		if errors.Is(e, state.ErrConversationStale) {
			continue
		}
		return e
	}
}
func decodeSavedReply(a state.ConversationAttempt, reply *Reply) error {
	var m struct {
		Content *string    `json:"content"`
		Calls   []ToolCall `json:"tool_calls"`
	}
	if e := json.Unmarshal([]byte(a.Reply), &m); e != nil {
		return e
	}
	reply.Message = json.RawMessage(a.Reply)
	reply.Text = a.Text
	reply.Calls = m.Calls
	return nil
}
func (r *Runner) recoverActions(ctx context.Context, t *state.ConversationTurn) error {
	if t.Attempt.Reply == "" {
		return nil
	}
	var reply Reply
	if e := decodeSavedReply(t.Attempt, &reply); e != nil {
		return e
	}
	for i := range t.Attempt.Actions {
		a := &t.Attempt.Actions[i]
		if a.Status != "pending" {
			continue
		}
		receipt, e := r.tasks.Store.ConversationCommand(ctx, a.ID)
		if e != nil {
			return e
		}
		if receipt != nil {
			a.Status, a.CreatedAt, a.Command = "accepted", receipt.Command.CreatedAt, receipt.Command
			t.Attempt.Results[i] = string(toolResult(reply.Calls[i].ID, commandResult(*receipt)))
		}
	}
	return nil
}

func (r *Runner) dispatchPending(ctx context.Context, f state.ConversationFence, t *state.ConversationTurn) error {
	if t.Attempt.Status != "error" {
		var reply Reply
		if e := decodeSavedReply(t.Attempt, &reply); e != nil {
			return e
		}
		commands := []state.Command{}
		indices := []int{}
		for i := range t.Attempt.Actions {
			a := &t.Attempt.Actions[i]
			if a.Status != "pending" {
				continue
			}
			// M2 checkpoints contained only dispatch requests.
			if a.Command.ID == "" {
				a.Command = state.Command{ID: a.ID, RunID: a.ID, Kind: "start", Text: a.Request}
			}
			commands = append(commands, a.Command)
			indices = append(indices, i)
		}
		if len(commands) > 0 {
			receipts, err := r.tasks.AcceptBatch(ctx, commands, &f)
			if errors.Is(err, state.ErrConversationStale) {
				return err
			}
			if err != nil && !commandRejected(err) {
				return err
			}
			for j, i := range indices {
				a := &t.Attempt.Actions[i]
				if err != nil {
					a.Status, a.Error = "rejected", err.Error()
					t.Attempt.Results[i] = string(toolResult(reply.Calls[i].ID, map[string]string{"error": "The entire command batch was rejected: " + a.Error}))
				} else {
					receipt := receipts[j]
					a.Status, a.CreatedAt, a.Command = "accepted", receipt.Command.CreatedAt, receipt.Command
					t.Attempt.Results[i] = string(toolResult(reply.Calls[i].ID, commandResult(receipt)))
				}
			}
			if e := r.checkpoint(ctx, f, *t); e != nil {
				return e
			}
		}
	}
	return nil
}

func buildProtocol(t *state.ConversationTurn, text string) {
	t.Protocol = []string{}
	for _, in := range t.Inputs {
		if in.Introduction {
			continue
		}
		t.Protocol = append(t.Protocol, string(inputMessage(in)))
	}
	t.Protocol = append(t.Protocol, t.Context...)
	if t.Attempt.Reply != "" && !t.Attempt.ProtocolInvalid {
		t.Protocol = append(t.Protocol, t.Attempt.Reply)
		t.Protocol = append(t.Protocol, t.Attempt.Results...)
	}
	if t.Attempt.Reply == "" || t.Attempt.Status == "error" || text != t.Attempt.Text {
		t.Protocol = append(t.Protocol, string(modelMessage(map[string]string{"role": "assistant", "content": text})))
	}
}

const introductionPrompt = `This is the first load of a new conversation. Introduce yourself as Nori in a short, friendly greeting. Be cheery, helpful, and warm, with a natural invitation to chat or ask for help. Do not invent personal knowledge or claim work has been done. If an actual user message follows this instruction, briefly introduce yourself and answer that message instead of giving a separate greeting. Do not dispatch a task just to introduce yourself.`

func (r *Runner) EnsureIntroduction(ctx context.Context) error {
	return r.tasks.Store.EnsureConversationIntroduction(ctx, introductionPrompt)
}
