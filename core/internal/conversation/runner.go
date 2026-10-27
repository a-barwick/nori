// Package conversation owns durable outer turns. Task execution stays in the
// existing Firestore/Temporal worker path.
package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	Introducing  bool     `json:"introducing"`
	Pending      bool     `json:"pending"`
	Processing   bool     `json:"processing"`
	Revision     int64    `json:"revision"`
	Consumed     int64    `json:"consumed"`
	TurnID       string   `json:"turn_id,omitempty"`
	InputIDs     []string `json:"input_ids"`
	RestartCount int      `json:"restart_count"`
	Frozen       bool     `json:"frozen"`
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

func (r *Runner) Snapshot(ctx context.Context) (Session, error) { return r.SnapshotPage(ctx, "") }
func (r *Runner) SnapshotPage(ctx context.Context, before string) (Session, error) {
	// These projections are independent. Keep each read's result local until all
	// finish so a slow history page does not serialize the other Firestore reads.
	var native state.Session
	var messages []Message
	var next string
	// Read completion before its projections: an idle snapshot must include the
	// reply committed with that status. Reading these concurrently can stop UI
	// polling on an older message list just as a turn completes.
	c, t, err := r.tasks.Store.ConversationStatus(ctx)
	if err != nil {
		return Session{}, err
	}
	var attempts []state.ConversationAttempt
	var readErrors [3]error
	var reads sync.WaitGroup
	reads.Go(func() { native, readErrors[0] = r.tasks.Store.ConversationProjection(ctx) })
	reads.Go(func() { messages, next, readErrors[1] = r.tasks.Store.ConversationMessages(ctx, before) })
	reads.Go(func() { attempts, readErrors[2] = r.tasks.Store.ConversationTraces(ctx) })
	reads.Wait()
	for _, err := range readErrors {
		if err != nil {
			return Session{}, err
		}
	}
	native.Messages = messages
	result := Session{Session: native, NextCursor: next, Dispatches: []Dispatch{}, Traces: []Trace{}, Conversation: ConversationStatus{Introducing: c.Introducing, Pending: c.Received > c.Consumed, Processing: c.Active != "" && c.LeaseUntil.After(time.Now()), Revision: c.Received, Consumed: c.Consumed, InputIDs: []string{}}}
	if t != nil {
		result.Conversation.TurnID = t.ID
		result.Conversation.RestartCount = t.Restarts
		result.Conversation.Frozen = t.Frozen
		for _, in := range t.Inputs {
			result.Conversation.InputIDs = append(result.Conversation.InputIDs, in.ID)
		}
	}
	for _, a := range attempts {
		trace := Trace{ID: a.ID, TurnID: a.TurnID, Attempt: a.Number, InputIDs: a.InputIDs, Model: a.Model, Status: a.Status, StartedAt: a.StartedAt, FinishedAt: a.FinishedAt, DurationMS: a.DurationMS, Actions: a.Actions, Usage: a.Usage, Error: a.Error, ContextTruncated: a.ContextTruncated, ContextTurns: a.ContextTurns, ContextBytes: a.ContextBytes}
		if trace.Actions == nil {
			trace.Actions = []state.ConversationAction{}
		}
		if trace.Usage == nil {
			trace.Usage = []state.Usage{}
		}
		if a.Hint != "" {
			trace.Classification = json.RawMessage(a.Hint)
			if result.Classification == nil {
				var h Classification
				if json.Unmarshal([]byte(a.Hint), &h) == nil {
					result.Classification = &h
				}
			}
		}
		result.Traces = append(result.Traces, trace)
		for _, action := range a.Actions {
			if action.Status == "accepted" {
				result.Dispatches = append(result.Dispatches, Dispatch{ID: action.ID, Request: action.Request, Status: action.Status, CreatedAt: action.CreatedAt})
			}
		}
		for i := range result.Messages {
			for _, id := range a.InputIDs {
				if result.Messages[i].ID == id+"-user" {
					result.Messages[i].TurnID = a.TurnID
				}
			}
		}
	}
	return result, nil
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
	return state.ConversationFence{Owner: c.Owner, Fence: c.Fence, TurnID: t.ID, Through: t.Through, Frozen: t.Frozen}
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
		if t.Attempt.Status != "error" {
			var reply Reply
			if e = decodeSavedReply(t.Attempt, &reply); e != nil {
				return e
			}
			for i, call := range reply.Calls {
				a := &t.Attempt.Actions[i]
				if a.Status != "pending" {
					continue
				}
				receipt, err := r.tasks.DispatchConversation(ctx, a.ID, a.Request, f)
				if errors.Is(err, state.ErrConversationStale) {
					break
				}
				if errors.Is(err, state.ErrConversationBusy) {
					return err
				}
				if err != nil {
					// Business rejections are final tool outcomes. Transport failures retain
					// the stable action ID and retry, including ambiguous accepted writes.
					if !errors.Is(err, state.ErrBusy) && !errors.Is(err, state.ErrPaused) && !errors.Is(err, state.ErrDuplicate) {
						return err
					}
					a.Status = "rejected"
					a.Error = dispatchFailure(err)
					t.Attempt.Results[i] = string(toolResult(call.ID, map[string]string{"error": a.Error}))
				} else {
					a.Status = "accepted"
					a.CreatedAt = receipt.CreatedAt
					t.Attempt.Results[i] = string(toolResult(call.ID, receipt))
				}
				if e = r.checkpoint(ctx, f, t); e != nil {
					return e
				}
			}
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
				text += "\n\nI couldn't accept the requested work: " + a.Error
			}
		}
		if t.Attempt.Status != "error" {
			attemptEnd(&t.Attempt, "completed", "")
		}
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
		accepted, e := r.tasks.Store.AcceptedConversationAction(ctx, a.ID)
		if e != nil {
			return e
		}
		if accepted {
			run, e := r.tasks.Store.GetRun(ctx, a.ID)
			if e != nil {
				return e
			}
			a.Status = "accepted"
			a.CreatedAt = run.StartedAt
			t.Attempt.Results[i] = string(toolResult(reply.Calls[i].ID, Dispatch{ID: a.ID, Request: a.Request, Status: "accepted", CreatedAt: a.CreatedAt}))
		}
	}
	return nil
}

func inputRole(in state.ConversationInput) string {
	if in.Introduction {
		return "system"
	}
	return "user"
}

func inputMessage(in state.ConversationInput) json.RawMessage {
	return modelMessage(map[string]string{"role": inputRole(in), "content": in.Text})
}

func (r *Runner) call(ctx context.Context, f state.ConversationFence, t *state.ConversationTurn) (callErr error) {
	started := time.Now()
	historyMS, tasksMS, visibleMS, jevMS, promptMS, outerMS := int64(-1), int64(-1), int64(-1), int64(-1), int64(-1), int64(-1)
	defer func() {
		log.Printf("conversation attempt %s phases: history_ms=%d tasks_ms=%d visible_ms=%d jev_ms=%d prompt_ms=%d outer_ms=%d total_ms=%d", t.Attempt.ID, historyMS, tasksMS, visibleMS, jevMS, promptMS, outerMS, time.Since(started).Milliseconds())
	}()
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	watchErr := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		lastRenew := time.Now()
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-callCtx.Done():
				return
			case <-ticker.C:
				stale, err := r.tasks.Store.CheckConversation(callCtx, f)
				if err == nil && time.Since(lastRenew) >= 10*time.Second {
					stale, err = r.tasks.Store.RenewConversation(callCtx, f)
					lastRenew = time.Now()
				}
				if err != nil {
					select {
					case watchErr <- err:
					default:
					}
					cancel()
					return
				}
				if stale {
					cancel()
					return
				}
			}
		}
	}()
	defer close(done)
	// The lease watcher also covers setup reads, not just provider latency.
	// History and task state can load while Jev classifies the visible context.
	type historyResult struct {
		turns      []state.ConversationTurn
		truncated  bool
		err        error
		durationMS int64
	}
	type taskResult struct {
		session    state.Session
		err        error
		durationMS int64
	}
	historyReady := make(chan historyResult, 1)
	tasksReady := make(chan taskResult, 1)
	var work sync.WaitGroup
	defer func() { cancel(); work.Wait() }()
	defer func() {
		if callCtx.Err() == nil {
			return
		}
		if ctx.Err() != nil {
			callErr = ctx.Err()
			return
		}
		select {
		case err := <-watchErr:
			callErr = err
		default:
			// Newer input cancelled setup or provider I/O. Let the turn owner
			// incorporate the queued batch before retrying.
			callErr = nil
		}
	}()
	work.Go(func() {
		readStarted := time.Now()
		history, truncated, err := r.tasks.Store.ConversationHistory(callCtx)
		historyReady <- historyResult{history, truncated, err, time.Since(readStarted).Milliseconds()}
	})
	work.Go(func() {
		readStarted := time.Now()
		snapshot, err := r.tasks.Store.ConversationProjection(callCtx)
		tasksReady <- taskResult{snapshot, err, time.Since(readStarted).Milliseconds()}
	})
	readStarted := time.Now()
	visible, next, e := r.tasks.Store.ConversationMessages(callCtx, "")
	visibleMS = time.Since(readStarted).Milliseconds()
	if e != nil {
		return e
	}
	inputID := t.Inputs[0].ID
	for _, input := range t.Inputs {
		if !input.Introduction {
			inputID = input.ID
			break
		}
	}
	// Provider callbacks only write the usage ledger. The turn owner alone
	// mutates/checkpoints Attempt, including while Jev and setup overlap.
	record := func(ctx context.Context, u state.Usage) error {
		u.InputID = inputID
		if err := r.tasks.Store.RecordUsage(ctx, u); err != nil {
			return err
		}
		stale, err := r.tasks.Store.CheckConversation(ctx, f)
		if err != nil {
			return err
		}
		if stale {
			return state.ErrConversationStale
		}
		return ctx.Err()
	}
	model := *r.model
	model.RecordUsage = record
	model.NoDispatch = true
	for _, input := range t.Inputs {
		if !input.Introduction {
			model.NoDispatch = false
			break
		}
	}
	classifier := *r.classifier
	classifier.RecordUsage = record
	type hintResult struct {
		hint       Classification
		err        error
		usageErr   error
		durationMS int64
	}
	hintReady := make(chan hintResult, 1)
	// Exclude this batch and any subsequently queued inputs from Jev's history.
	// The owned batch is supplied separately, once, in durable acceptance order.
	classificationHistory := []Message{}
	for _, message := range visible {
		if message.TurnID == t.ID || (message.Role == "user" && !message.CreatedAt.Before(t.Inputs[0].CreatedAt)) {
			continue
		}
		if message.RunID != "" {
			if message.ID == message.RunID+"-user" {
				continue
			}
			message.Text = "[Task " + message.RunID + " record; background, not a new request]\n" + message.Text
		}
		classificationHistory = append(classificationHistory, message)
	}
	inputs := append([]state.ConversationInput(nil), t.Inputs...)
	if t.Attempt.Hint != "" {
		var hint Classification
		if err := json.Unmarshal([]byte(t.Attempt.Hint), &hint); err != nil {
			return fmt.Errorf("saved classification hint is invalid: %w", err)
		}
		hintReady <- hintResult{hint: hint, durationMS: -1}
	} else {
		work.Go(func() {
			classifyStarted := time.Now()
			hint, err := classifier.Classify(callCtx, inputs, classificationHistory)
			durationMS := time.Since(classifyStarted).Milliseconds()
			// Preserve observed usage even if setup failed or the turn was superseded.
			var usageErr error
			if hint.Usage.ID != "" {
				hint.Usage.InputID = inputID
				usageErr = r.recordUsage(inputID, hint.Usage)
			} else {
				durationMS = -1
			}
			hintReady <- hintResult{hint, err, usageErr, durationMS}
		})
	}
	historyRead := <-historyReady
	historyMS = historyRead.durationMS
	if historyRead.err != nil {
		return historyRead.err
	}
	history, truncated := historyRead.turns, historyRead.truncated
	promptStarted := time.Now()
	messages := []json.RawMessage{modelMessage(map[string]string{"role": "system", "content": Instructions})}
	if next != "" {
		truncated = true
	}
	legacy := []Message{}
	legacyBytes := 0
	for i := len(visible) - 1; i >= 0; i-- {
		m := visible[i]
		if m.TurnID != "" {
			continue
		}
		if m.RunID != "" && m.ID == m.RunID+"-user" {
			continue
		}
		if m.RunID != "" {
			m.Text = "[Task " + m.RunID + " record; background, not a new request]\n" + m.Text
		}
		p := modelMessage(map[string]string{"role": m.Role, "content": m.Text})
		if legacyBytes+len(p) > 32<<10 {
			truncated = true
			break
		}
		legacyBytes += len(p)
		legacy = append(legacy, m)
	}
	// Merge older visible records around complete protocol blocks. Appending all
	// legacy records after history makes old conversation look like new input.
	legacyIndex := len(legacy) - 1
	appendLegacy := func() {
		m := legacy[legacyIndex]
		messages = append(messages, modelMessage(map[string]string{"role": m.Role, "content": m.Text}))
		legacyIndex--
	}
	for _, turn := range history {
		for legacyIndex >= 0 && !legacy[legacyIndex].CreatedAt.After(turn.StartedAt) {
			appendLegacy()
		}
		for _, p := range turn.Protocol {
			messages = append(messages, json.RawMessage(p))
		}
	}
	for legacyIndex >= 0 {
		appendLegacy()
	}
	t.Attempt.ContextTruncated = truncated
	t.Attempt.ContextTurns = len(history)
	for _, in := range t.Inputs {
		messages = append(messages, inputMessage(in))
	}
	for _, p := range t.Context {
		messages = append(messages, json.RawMessage(p))
	}
	t.Attempt.ContextBytes = 0
	for _, m := range messages {
		t.Attempt.ContextBytes += len(m)
	}
	promptMS = time.Since(promptStarted).Milliseconds()
	taskRead := <-tasksReady
	tasksMS = taskRead.durationMS
	if taskRead.err != nil {
		return taskRead.err
	}
	snapshot := taskRead.session
	active := []map[string]string{}
	recentTasks := []map[string]string{}
	for _, run := range snapshot.Runs {
		recentTasks = append(recentTasks, map[string]string{"id": run.ID, "status": run.Status, "waiting": run.Waiting, "error": run.Error})
		if run.Status == "running" {
			active = append(active, map[string]string{"id": run.ID, "status": run.Status, "waiting": run.Waiting})
		}
	}
	classified := <-hintReady
	jevMS = classified.durationMS
	hint, classificationErr := classified.hint, classified.err
	if hint.Usage.ID != "" {
		attemptUsage(&t.Attempt, hint.Usage)
	}
	if classified.usageErr != nil {
		return classified.usageErr
	}
	if classificationErr != nil {
		log.Printf("Jev advisory hint unavailable: %v", classificationErr)
		hint = Classification{Usage: hint.Usage, Available: false}
	}
	t.Attempt.Hint = string(modelMessage(hint))
	if e = r.checkpoint(ctx, f, *t); e != nil {
		return e
	}
	if callCtx.Err() != nil {
		return callCtx.Err()
	}
	advice, _ := json.Marshal(map[string]any{"classification_hint": hint, "active_tasks": active, "recent_task_records": recentTasks, "task_records_instruction": "Historical context only; no new instruction to resume or repeat work."})
	messages = append(messages, modelMessage(map[string]string{"role": "system", "content": "Advisory context for the current inputs: " + string(advice)}))
	t.Attempt.ContextBytes += len(messages[len(messages)-1])
	outerStarted := time.Now()
	reply, modelErr := model.Call(callCtx, messages)
	outerMS = time.Since(outerStarted).Milliseconds()
	reply.Usage.InputID = inputID
	attemptUsage(&t.Attempt, reply.Usage)
	if e = r.recordUsage(inputID, reply.Usage); e != nil {
		return e
	}
	if modelErr != nil {
		if callCtx.Err() != nil {
			return callCtx.Err()
		}
		stale, err := r.tasks.Store.RenewConversation(ctx, f)
		if err != nil {
			return err
		}
		if stale {
			return r.checkpoint(ctx, f, *t)
		}
		attemptEnd(&t.Attempt, "error", modelErr.Error())
		t.Attempt.Text = "I couldn't answer this message: " + modelErr.Error()
	} else {
		t.Attempt.Reply = string(reply.Message)
		requests, err := dispatchRequests(reply.Calls)
		if model.NoDispatch && len(reply.Calls) != 0 {
			err = fmt.Errorf("an introduction cannot authorize a task")
		}
		if err != nil || reply.Text == "" {
			failure := "The model returned invalid tool calls. No work was dispatched."
			if err == nil {
				failure = "The model did not include a reply with its tool calls. No work was dispatched."
			}
			attemptEnd(&t.Attempt, "error", failure)
			t.Attempt.ProtocolInvalid = err != nil
			if err == nil {
				for _, call := range reply.Calls {
					t.Attempt.Results = append(t.Attempt.Results, string(toolResult(call.ID, map[string]string{"error": failure})))
				}
			}
			t.Attempt.Text = failure
		} else {
			t.Attempt.Reply = string(reply.Message)
			t.Attempt.Text = reply.Text
			for i, call := range reply.Calls {
				id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(t.Attempt.ID+":"+call.ID)).String()
				t.Attempt.Actions = append(t.Attempt.Actions, state.ConversationAction{ID: id, Request: requests[i], Bytes: len(requests[i]), Status: "pending"})
				t.Attempt.Results = append(t.Attempt.Results, string(toolResult(call.ID, map[string]string{"error": "superseded before admission"})))
			}
		}
	}
	return r.checkpoint(ctx, f, *t)
}
func dispatchRequests(calls []ToolCall) ([]string, error) {
	requests := []string{}
	seen := map[string]bool{}
	for _, call := range calls {
		if call.Type != "function" || call.Function.Name != "dispatch" || call.ID == "" || seen[call.ID] {
			return nil, fmt.Errorf("model requested an invalid dispatch tool call")
		}
		seen[call.ID] = true
		var args struct {
			Request string `json:"request"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return nil, fmt.Errorf("model returned invalid dispatch arguments")
		}
		args.Request = strings.TrimSpace(args.Request)
		if args.Request == "" || len(args.Request) > 8192 {
			return nil, fmt.Errorf("dispatch request must contain 1–8192 bytes")
		}
		requests = append(requests, args.Request)
	}
	return requests, nil
}

func toolResult(id string, value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return modelMessage(map[string]string{"role": "tool", "tool_call_id": id, "content": string(data)})
}

func dispatchFailure(err error) string {
	if errors.Is(err, state.ErrBusy) {
		return "another task is already active. Use the explicit task controls to update or stop it."
	}
	if errors.Is(err, state.ErrPaused) {
		return "task execution is paused."
	}
	return "task admission was unavailable. No acceptance was confirmed."
}
