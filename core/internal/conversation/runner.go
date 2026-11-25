// Package conversation owns durable outer turns. Task execution runs in the
// unified Postgres-native task worker pool.
package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"nori/internal/observability"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/users"
)

type Input struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	// Attachments are the IDs of files already uploaded.
	Attachments []string `json:"attachments,omitempty"`
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
	Results        []state.Result     `json:"results"`
	// Position is the event log's position when this snapshot was read. Events after it may already be included, and
	// applying one again changes nothing.
	Position int64 `json:"position"`
	// ReadPosition is shared by every client and only moves forward.
	ReadPosition state.ReadMark `json:"read_position"`
}
type Runner struct {
	UserFacts string
	// Profile, when set, supplies the user facts and clock at the time of each call instead of UserFacts and Clock.
	Profile    users.Profile
	model      ReplyModel
	classifier IntentClassifier
	// summarizer writes coupled flush/compaction, decoupled extraction, and AutoDream passes.
	// compactAfter, extractAfter, and dreamAfter pause background maintenance after failures.
	summarizer   SummaryModel
	compactAfter time.Time
	extractAfter time.Time
	dreamAfter   time.Time
	tasks        *Tasks
	process      sync.Mutex
	Clock        Clock
	// Optional determinism hooks. Nil defaults to time.Now, uuid.NewString, time.NewTimer, and time.NewTicker.
	Now       func() time.Time
	NewID     func() string
	After     func(time.Duration) <-chan time.Time
	NewTicker func(time.Duration) (<-chan time.Time, func())
	SyncWork  bool
}

func (r *Runner) runWork(work *sync.WaitGroup, fn func()) {
	if r != nil && r.SyncWork {
		fn()
		return
	}
	work.Go(fn)
}

func (r *Runner) now() time.Time {
	if r != nil && r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) newID() string {
	if r != nil && r.NewID != nil {
		return r.NewID()
	}
	return uuid.NewString()
}

func (r *Runner) wait(ctx context.Context, d time.Duration) error {
	if r != nil && r.After != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.After(d):
			return nil
		}
	}
	timer := time.NewTimer(d)
	select {
	case <-ctx.Done():
		timer.Stop()
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *Runner) runPeriodic(ctx context.Context, done <-chan struct{}, interval time.Duration, fn func() bool) {
	if r != nil && r.NewTicker != nil {
		ch, stop := r.NewTicker(interval)
		defer stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ch:
				if !fn() {
					return
				}
			}
		}
	}
	if r != nil && r.After != nil {
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case _, ok := <-r.After(interval):
				if !ok || !fn() {
					return
				}
			}
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !fn() {
				return
			}
		}
	}
}

func New(model ReplyModel, classifier IntentClassifier, summarizer SummaryModel, tasks *Tasks) *Runner {
	return &Runner{model: model, classifier: classifier, summarizer: summarizer, tasks: tasks}
}

// HandleInput only accepts the idempotent inbox item; provider I/O is performed
// by Process independently of the submitting HTTP request.
func (r *Runner) HandleInput(ctx context.Context, input Input) (Session, error) {
	if _, e := uuid.Parse(input.ID); e != nil {
		return Session{}, fmt.Errorf("message ID must be a UUID")
	}
	if (strings.TrimSpace(input.Message) == "" && len(input.Attachments) == 0) || len(input.Message) > state.MaxMessageBytes {
		return Session{}, fmt.Errorf("message must contain 1–8192 bytes")
	}
	ctx, span := conversationTracer.Start(ctx, "conversation.accept", trace.WithAttributes(
		attribute.String("openinference.span.kind", "CHAIN"), attribute.String("session.id", observability.SessionID()),
		attribute.String("nori.input.id", input.ID),
	))
	defer span.End()
	if e := r.tasks.Store.AcceptConversationInput(ctx, input.ID, input.Message, input.Attachments); e != nil {
		if !errors.Is(e, state.ErrConversationConflict) && !errors.Is(ctx.Err(), context.Canceled) {
			observability.RecordErrorCategory(ctx, "conversation.accept", e)
		}
		return Session{}, e
	}
	observability.Event(ctx, "conversation.accepted")
	session, err := r.Snapshot(ctx)
	if err != nil && !errors.Is(ctx.Err(), context.Canceled) {
		observability.RecordErrorCategory(ctx, "conversation.snapshot", err)
	}
	return session, err
}

// Retry replays unrouted user input(s) from a failed turn without duplicating the visible user message.
func (r *Runner) Retry(ctx context.Context, turnID, messageID string) (Session, error) {
	ctx, span := conversationTracer.Start(ctx, "conversation.retry", trace.WithAttributes(
		attribute.String("openinference.span.kind", "CHAIN"), attribute.String("session.id", observability.SessionID()),
	))
	defer span.End()
	if e := r.tasks.Store.RetryConversation(ctx, turnID, messageID); e != nil {
		if !errors.Is(ctx.Err(), context.Canceled) {
			observability.RecordErrorCategory(ctx, "conversation.retry", e)
		}
		return Session{}, e
	}
	observability.Event(ctx, "conversation.retried")
	session, err := r.Snapshot(ctx)
	if err != nil && !errors.Is(ctx.Err(), context.Canceled) {
		observability.RecordErrorCategory(ctx, "conversation.snapshot", err)
	}
	return session, err
}
func fenceFor(c state.ConversationControl, t state.ConversationTurn) state.ConversationFence {
	return state.ConversationFence{Owner: c.Owner, Fence: c.Fence, TurnID: t.ID, Through: t.Through, UserThrough: t.UserThrough, Frozen: t.Frozen}
}

// Process drains durable work. A live competing lease is retryable so a queue
// wake never acknowledges away the only remaining notification.
func (r *Runner) Process(ctx context.Context) error {
	if !r.process.TryLock() {
		return state.ErrConversationBusy
	}
	defer r.process.Unlock()
	for {
		c, t, e := r.tasks.Store.AcquireConversation(ctx, r.newID(), r.newID())
		if e != nil {
			return e
		}
		if t == nil {
			// Nothing is waiting, so coupled flush/compaction and idle AutoDream can run without delaying a reply.
			r.compact(ctx)
			r.maybeDream(ctx)
			return nil
		}
		f := fenceFor(c, *t)
		turnCtx, turnSpan := startTurn(ctx, *t)
		e = r.processTurn(turnCtx, c, *t)
		if e != nil && !errors.Is(e, context.Canceled) && !errors.Is(e, state.ErrConversationStale) && !errors.Is(e, state.ErrConversationBusy) {
			observability.RecordErrorCategory(turnCtx, "conversation.process", e)
		}
		turnSpan.End()
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = r.tasks.Store.ReleaseConversation(cleanup, f)
		cancel()
		if e != nil {
			return e
		}
	}
}
func (r *Runner) attemptEnd(a *state.ConversationAttempt, statusText, detail string) {
	now := r.now().UTC()
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
				if e = r.wait(ctx, min(wait, 500*time.Millisecond)); e != nil {
					return e
				}
				continue
			}
			f = fenceFor(c, t)
		}
		if t.Attempt.ID == "" {
			a := state.ConversationAttempt{
				ID: fmt.Sprintf("%s-attempt-%d", t.ID, t.Restarts+1), TurnID: t.ID, Number: t.Restarts + 1, Model: r.model.Name(),
				Status: "running", StartedAt: r.now().UTC(), Actions: []state.ConversationAction{}, Usage: []state.Usage{},
				ModelCheckpoint: &state.ConversationModelCheckpoint{Phase: preparingReply},
			}
			for _, in := range t.Inputs {
				a.InputIDs = append(a.InputIDs, in.ID)
			}
			t.Attempt = a
			if e = r.checkpoint(ctx, f, t); e != nil {
				return e
			}
		}
		span := trace.SpanFromContext(ctx)
		span.SetAttributes(
			attribute.String("nori.attempt.id", t.Attempt.ID), attribute.Int("nori.attempt.number", t.Attempt.Number),
			attribute.Int("nori.conversation.restart_count", t.Restarts),
		)
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
		// Resolve explicit retrievals even when resuming a saved model reply. A missing
		// ID must not leave the model's display claim or the "Here it is" fallback.
		shown := []string{}
		missing := false
		for _, id := range t.Attempt.ShowResults {
			if slices.Contains(shown, id) {
				continue
			}
			if _, err := r.tasks.Store.GetResult(ctx, id); errors.Is(err, state.ErrNotFound) {
				missing = true
			} else if err != nil {
				return err
			} else {
				shown = append(shown, id)
			}
		}
		t.Attempt.ShowResults = shown
		showFailure := ""
		if missing {
			showFailure = prompts.Runtime("results-missing", nil)
			if len(shown) > 0 {
				showFailure = prompts.Runtime("results-partially-missing", nil)
			}
		}
		text := t.Attempt.Text
		commandFailure := ""
		for _, a := range t.Attempt.Actions {
			if a.Status == "rejected" {
				commandFailure = prompts.Runtime("task-changes-rejected", map[string]any{"Error": a.Error})
				text = commandFailure
				break
			}
		}
		// Failed retrievals or refused changes replace model claims, while accepted tasks still say so.
		if failures := strings.TrimSpace(showFailure + " " + memoryFailureText(t.Attempt.Memory)); failures != "" {
			text = strings.TrimSpace(failures + " " + commandFailure + " " + acceptedCommandText(t.Attempt.Actions))
		}
		if text == "" {
			text = acceptedCommandText(t.Attempt.Actions)
			span.SetAttributes(attribute.Bool("nori.reply.receipt_ack", true))
			observability.Event(ctx, "conversation.command_acknowledgment", attribute.Int("nori.tool.call_count", len(t.Attempt.Actions)))
		}
		if t.Attempt.Status != "error" {
			r.attemptEnd(&t.Attempt, "completed", "")
		}
		if strings.TrimSpace(text) == "" {
			text = memoryText(t.Attempt.Memory)
		}
		if strings.TrimSpace(text) == "" && len(t.Attempt.ShowResults) > 0 {
			text = "Here it is."
		}
		buildProtocol(&t, text)
		e = r.tasks.Store.PublishConversation(ctx, f, t, text)
		if errors.Is(e, state.ErrConversationStale) {
			continue
		}
		if e == nil {
			span.SetAttributes(attribute.String("nori.reply.status", t.Attempt.Status), attribute.Bool("nori.output.present", text != ""))
			if t.Attempt.Status == "error" {
				span.SetStatus(codes.Error, "conversation reply failed")
				observability.Event(ctx, "conversation.failed")
			} else {
				observability.Event(ctx, "conversation.completed")
			}
			recordReplies(ctx, t, text, r.now())
		}
		return e
	}
}
func decodeSavedReply(a state.ConversationAttempt, reply *Reply) error {
	if err := restoreReply(a.Reply, a.Text, a.StructuredReply, reply); err != nil {
		return err
	}
	// Actions line up with task commands only: shown results and memory changes have no action.
	reply.Calls = slices.DeleteFunc(reply.Calls, func(call ToolCall) bool {
		name := call.Function.Name
		return name == "show_result" || name == state.MemoryRemember || name == state.MemoryForget
	})
	return nil
}

func restoreReply(message, text string, structured bool, reply *Reply) error {
	var m struct {
		Content *string    `json:"content"`
		Calls   []ToolCall `json:"tool_calls"`
	}
	if e := json.Unmarshal([]byte(message), &m); e != nil {
		return e
	}
	*reply = Reply{Message: json.RawMessage(message), Text: text, Calls: m.Calls, Structured: structured}
	if structured {
		if m.Content == nil {
			return fmt.Errorf("saved structured routing response is missing")
		}
		var err error
		_, reply.Calls, err = decodeResponse(*m.Content)
		if err != nil {
			return err
		}
	}
	return nil
}
func (r *Runner) recoverActions(ctx context.Context, t *state.ConversationTurn) error {
	if t.Attempt.Reply == "" || t.Attempt.ProtocolInvalid {
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
			callID := a.ID
			if i < len(reply.Calls) && reply.Calls[i].ID != "" {
				callID = reply.Calls[i].ID
			}
			if i < len(t.Attempt.Results) {
				t.Attempt.Results[i] = string(toolResult(callID, commandResult(*receipt)))
			}
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
				callID := a.ID
				if i < len(reply.Calls) && reply.Calls[i].ID != "" {
					callID = reply.Calls[i].ID
				}
				if err != nil {
					a.Status, a.Error = "rejected", err.Error()
					if i < len(t.Attempt.Results) {
						t.Attempt.Results[i] = string(toolResult(callID, map[string]string{"error": prompts.Runtime("command-batch-rejected", nil) + a.Error}))
					}
				} else {
					receipt := receipts[j]
					a.Status, a.CreatedAt, a.Command = "accepted", receipt.Command.CreatedAt, receipt.Command
					if i < len(t.Attempt.Results) {
						t.Attempt.Results[i] = string(toolResult(callID, commandResult(receipt)))
					}
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
		if in.Introduction || in.Retry {
			continue
		}
		t.Protocol = append(t.Protocol, string(inputMessage(in)))
	}
	t.Protocol = append(t.Protocol, t.Context...)
	if t.Attempt.Reply != "" && !t.Attempt.ProtocolInvalid {
		t.Protocol = append(t.Protocol, t.Attempt.Reply)
		if t.Attempt.StructuredReply && len(t.Attempt.Results) > 0 {
			results, _ := json.Marshal(t.Attempt.Results)
			t.Protocol = append(t.Protocol, string(modelMessage(map[string]string{
				"role":    "system",
				"content": prompts.Runtime("runner-saved-command-receipts-for-the-preceding-structured-res", nil) + string(results),
			})))
		} else {
			t.Protocol = append(t.Protocol, t.Attempt.Results...)
		}
	}
	if len(t.Attempt.Memory) > 0 {
		changes, _ := json.Marshal(t.Attempt.Memory)
		t.Protocol = append(t.Protocol, string(modelMessage(map[string]string{
			"role":    "system",
			"content": prompts.Runtime("runner-saved-memory-changes-for-the-preceding-response-a-chang", nil) + string(changes),
		})))
	}
	if t.Attempt.StructuredReply || t.Attempt.Reply == "" || t.Attempt.Status == "error" || text != t.Attempt.Text {
		t.Protocol = append(t.Protocol, string(modelMessage(map[string]string{"role": "assistant", "content": text})))
	}
}

func (r *Runner) EnsureIntroduction(ctx context.Context) error {
	return r.tasks.Store.EnsureConversationIntroduction(ctx, prompts.Introduction)
}

// backgroundIdleBlocked reports whether out-of-band background maintenance
// (compaction, idle extraction, AutoDream) must stay parked because user input
// or routing work is still pending or active. Shared by NextConversationWake,
// compact, flushAndCompact, extractIdleMemory, and maybeDream so wake planning
// and execution never disagree.
func (r *Runner) backgroundIdleBlocked(ctx context.Context, control state.ConversationControl) (bool, error) {
	if control.Pending() || control.Active != "" {
		return true, nil
	}
	pending, err := r.tasks.Store.UserRoutingPending(ctx)
	if err != nil {
		return true, err
	}
	return pending, nil
}

// NextConversationWake returns the earliest deferred idle wake timestamp for
// out-of-band flush/compaction or AutoDream consolidation, or zero when idle.
func (r *Runner) NextConversationWake(ctx context.Context) (time.Time, error) {
	if r.summarizer == nil {
		return time.Time{}, nil
	}
	control, _, err := r.tasks.Store.ConversationStatus(ctx)
	if err != nil {
		return time.Time{}, err
	}
	blocked, err := r.backgroundIdleBlocked(ctx, control)
	if err != nil || blocked {
		return time.Time{}, err
	}
	summary, err := r.tasks.Store.ConversationSummary(ctx)
	if err != nil {
		return time.Time{}, err
	}
	now := r.now()

	var earliest time.Time
	consider := func(candidate time.Time) {
		if candidate.IsZero() {
			return
		}
		if earliest.IsZero() || candidate.Before(earliest) {
			earliest = candidate
		}
	}

	flushState, err := r.planFlush(ctx, control, summary, now, false)
	if err != nil {
		return time.Time{}, err
	}
	if flushState.hasWork {
		consider(maxTime(now, r.compactAfter))
	}

	extractState, err := r.planExtract(ctx, control, summary, now, false)
	if err != nil {
		return time.Time{}, err
	}
	if extractState.hasWork {
		target := extractState.wakeAt
		if extractState.ready || target.IsZero() {
			target = now
		}
		consider(maxTime(target, r.extractAfter))
	}

	latestTurnAt := maxTime(flushState.latestTurnAt, extractState.latestTurnAt)
	dreamState, err := r.planDream(ctx, control, summary, latestTurnAt, now)
	if err != nil {
		return time.Time{}, err
	}
	if dreamState.eligible {
		switch {
		case r.dreamAfter.After(now):
			consider(r.dreamAfter)
		case !dreamState.wakeAt.IsZero():
			consider(dreamState.wakeAt)
		default:
			consider(now)
		}
	}

	return earliest, nil
}

// ConsolidateNow triggers a manual AutoDream consolidation pass.
func (r *Runner) ConsolidateNow(ctx context.Context) (state.ConversationSummary, error) {
	return r.dreamConsolidate(ctx, true)
}
