package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"nori/internal/agent"
	"nori/internal/computer"
	"nori/internal/contextview"
	"nori/internal/dispatch"
	"nori/internal/extensions"
	"nori/internal/observability"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/transcript"
	"nori/internal/usage"
)

type Activities struct {
	Agent *agent.Service
	Store state.TaskStore
	// Executor runs registry tools through App connectors, the MCP client and plugin executors. Nil leaves them
	// unavailable.
	Executor *Executor
}
type Step struct {
	RunID    string
	Revision int64
}
type ModelInput struct {
	Step
	Messages      []transcript.Message
	Interruptible bool
	Subtask       bool
	ContextRetry  bool
}

// ModelOutcome preserves billed usage in Temporal history even when the
// response is unusable. Activity errors discard ordinary return values.
type ModelOutcome struct {
	Response        agent.Response
	Failure         string
	ContextOverflow bool
}
type ToolInput struct {
	Step
	OperationID string
	Call        transcript.ToolCall
	Exec        *extensions.Exec `json:",omitempty"`
}
type Publication struct {
	Step
	Status             state.RunStatus
	Detail, Text       string
	StopConfirmed      bool
	CostUnknown        bool
	Spent              float64
	BypassRouting      bool
	ExpectedIncomplete bool
	Answer             *state.ResultDraft
}
type Projection struct {
	Step
	Kind, Text string
}
type QuestionProjection struct {
	Step
	Question state.Question
}
type BrowserCleanup struct{ RunID, Scope string }
type Spending struct {
	RunID         string
	Spent, Budget float64
	CostUnknown   bool
}

const (
	activitySuperseded     = "superseded"
	activityCancelled      = "cancelled"
	activityRoutingPending = "routing_pending"
	activityPaused         = "paused"
	activityFailed         = "activity_failed"
)

func classified(err error) error {
	if err == nil {
		return nil
	}
	kind := activityFailed
	switch {
	case errors.Is(err, state.ErrSuperseded):
		kind = activitySuperseded
	case errors.Is(err, state.ErrCancelled):
		kind = activityCancelled
	case errors.Is(err, state.ErrRoutingPending):
		kind = activityRoutingPending
	case errors.Is(err, state.ErrPaused):
		kind = activityPaused
	}
	return temporal.NewNonRetryableApplicationError(err.Error(), kind, err)
}
func (a *Activities) Context(ctx context.Context, id string) ([]transcript.Message, error) {
	session, e := a.Store.Session(ctx)
	if e != nil {
		return nil, classified(e)
	}
	// Only completed tasks provide background. Incomplete or cancelled user
	// requests must not become dangling instructions for the next task. Accepted
	// current-task commands are incorporated separately in durable order.
	completed := map[string]bool{}
	for _, run := range session.Runs {
		if run.ID != id && run.Status == state.RunCompleted {
			completed[run.ID] = true
		}
	}
	records := []json.RawMessage{}
	total := 0
	for i := len(session.Messages) - 1; i >= 0 && len(records) < 20; i-- {
		m := session.Messages[i]
		if !completed[m.RunID] {
			continue
		}
		record, e := json.Marshal(map[string]string{"task_id": m.RunID, "status": "completed", "role": m.Role, "text": m.Text})
		if e != nil {
			return nil, classified(e)
		}
		if total+len(record) > 64<<10 {
			break
		}
		records = append([]json.RawMessage{record}, records...)
		total += len(record)
	}
	if len(records) == 0 {
		return nil, nil
	}
	history, e := json.Marshal(records)
	if e != nil {
		return nil, classified(e)
	}
	background := prompts.Runtime("task-history-start", nil) + string(history) +
		prompts.Runtime("task-history-end", nil)
	return []transcript.Message{transcript.UserText(background)}, nil
}
func (a *Activities) Reconcile(ctx context.Context, id string) (state.TaskSnapshot, error) {
	snap, e := a.Store.Reconcile(ctx, id)
	if e != nil {
		return snap, classified(e)
	}
	if snap.Run.CancelRequested {
		return snap, classified(state.ErrCancelled)
	}
	if snap.Run.Status != state.RunRunning || snap.Run.Finishing != nil {
		return snap, classified(state.ErrBusy)
	}
	// Reconciliation and publication do not start paid work. Their revision
	// and cancellation checks remain valid even while admission is paused.
	e = a.Store.Incorporate(ctx, state.IncorporateRequest{RunID: id, Revision: snap.Run.Revision, Position: snap.Run.Position})
	return snap, classified(e)
}

// bounded tracks pause and Stop throughout I/O. Corrections invalidate
// the next boundary; already admitted bounded work is allowed to finish.
func (a *Activities) bounded(parent context.Context, step Step, interruptible bool) (context.Context, func(), state.Run, error) {
	run, e := a.Store.Guard(parent, step.RunID, step.Revision)
	if e != nil {
		return nil, nil, state.Run{}, classified(e)
	}
	ctx, cancel := context.WithCancel(usage.WithScope(parent, step.RunID, ""))
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				activity.RecordHeartbeat(ctx)
				current, e := a.Store.GetRun(ctx, step.RunID)
				interrupted := interruptible && current.InterruptRevision > step.Revision
				if e != nil || current.CancelRequested || current.Status != state.RunRunning || interrupted {
					cancel()
					return
				}
				control, e := a.Store.Control(ctx)
				if e != nil {
					cancel()
					return
				}
				if e = control.Check(); e != nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(); <-done }, run, nil
}

// ioError distinguishes durable Stop from an ordinary canceled HTTP request.
func (a *Activities) ioError(step Step, err error) error {
	if err == nil {
		return nil
	}
	check, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	current, readErr := a.Store.GetRun(check, step.RunID)
	if readErr == nil && current.CancelRequested {
		return classified(state.ErrCancelled)
	}
	return classified(err)
}
func modelActivityID(info activity.Info) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(info.WorkflowExecution.RunID+":"+info.ActivityID)).String()
}

func (a *Activities) Model(ctx context.Context, in ModelInput) (ModelOutcome, error) {
	ctx, done, run, e := a.bounded(ctx, in.Step, in.Interruptible)
	if e != nil {
		return ModelOutcome{}, e
	}
	defer done()
	ctx, span := startActivity(ctx, "task.model", "AGENT", run, in.Revision)
	defer span.End()
	trace := state.TaskStep{
		ID:   modelActivityID(activity.GetInfo(ctx)),
		Kind: "model", Status: "running", Revision: in.Revision, StartedAt: time.Now().UTC(),
		Model: a.Agent.Model.Name(), CostStatus: "unknown",
	}
	if in.ContextRetry {
		trace.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(trace.ID+":context")).String()
	}
	a.recordStep(in.RunID, trace)
	// Detailed answers share the output allowance with thinking. Keep a bounded
	// model deadline that leaves room for both, while bounded still observes Stop.
	modelCtx, stop := context.WithTimeout(ctx, 2*time.Minute)
	defer stop()
	response, e := a.Agent.Decide(observability.SuppressErrorCapture(modelCtx), in.Messages, true, !in.Subtask)
	span.SetAttributes(attribute.Bool("nori.output.present", response.Answer != ""), attribute.Int("nori.tool.call_count", len(response.Calls)))
	recordActivityFailure(modelCtx, e, "task.model")
	outcome := ModelOutcome{Response: response, ContextOverflow: errors.Is(e, contextview.ErrOverflow)}
	if e != nil {
		outcome.Failure = e.Error()
	}
	if response.Usage != nil {
		u := response.Usage
		input, output, cached, thinking := u.InputTokens, u.OutputTokens, u.CachedTokens, u.ReasoningTokens
		trace.InputTokens, trace.OutputTokens, trace.CachedTokens, trace.ThinkingTokens = &input, &output, &cached, &thinking
	}
	if response.CostKnown {
		trace.CostUSD, trace.CostStatus = &response.Cost, "reported"
		if a.Agent.Model.Provider() == "openai" {
			trace.CostStatus = "calculated"
		}
	} else if response.NotSent {
		trace.CostStatus = "not_sent"
	}
	for _, call := range response.Calls {
		trace.Calls = append(trace.Calls, state.TraceCall{Name: call.Name, Arguments: traceJSON(call.Args)})
	}
	trace.Result = traceText(response.Answer)
	finishStep(&trace, e)
	a.recordStep(in.RunID, trace)
	return outcome, nil
}
func finishStep(step *state.TaskStep, err error) {
	now := time.Now().UTC()
	step.FinishedAt, step.DurationMS, step.Status = &now, now.Sub(step.StartedAt).Milliseconds(), "completed"
	if err != nil {
		step.Status, step.Error = "failed", traceText(err.Error())
	}
}

// Trace projection never retries an external call or changes its outcome.
func (a *Activities) recordStep(runID string, step state.TaskStep) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Store.RecordTaskStep(ctx, runID, step); err != nil {
		log.Printf("task trace unavailable for step %s: %v", step.ID, err)
	}
}

func traceJSON(value any) string {
	if value == nil {
		return ""
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "unavailable"
	}
	return traceText(string(encoded))
}

func traceText(text string) string {
	const limit = 4096
	if len(text) <= limit {
		return text
	}
	text = text[:limit]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text + "… [truncated]"
}
func computerRun(step Step) string { return fmt.Sprintf("%s-r%d", step.RunID, step.Revision) }
func (a *Activities) Stop(ctx context.Context, ids []string) (err error) {
	// The workflow ID identifies the task without changing historical activity
	// inputs. Recovery calls outside a workflow retain their existing context.
	id := ""
	if activity.IsActivity(ctx) {
		workflowID := activity.GetInfo(ctx).WorkflowExecution.ID
		_, candidate, ok := dispatch.WorkflowUser(workflowID)
		if ok {
			id = candidate
		}
	}
	run := state.Run{ID: id}
	if id != "" {
		run = a.activityRun(ctx, id)
	}
	ctx, span := startActivity(ctx, "task.stop_computer", "CHAIN", run, run.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.stop_computer"); span.End() }()
	return classified(StopComputer(observability.SuppressErrorCapture(ctx), a.Agent.Computer, ids))
}
func StopComputer(ctx context.Context, config computer.Provider, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	c, e := computer.Attach(ctx, config, ids[0])
	if errors.Is(e, computer.ErrStopped) {
		return nil
	}
	if e != nil {
		return e
	}
	defer c.Close()
	reattached := false
	for _, id := range ids {
		c.SetRun(id)
		e = c.Cancel(ctx)
		if !reattached && errors.Is(e, computer.ErrDraining) {
			// Only this explicit host rejection proves /cancel never reached
			// the guest. Re-attach once without starting a stopped computer.
			c.Close()
			reattached = true
			c, e = computer.Attach(ctx, config, id)
			if errors.Is(e, computer.ErrStopped) {
				return nil // GCE confirmed TERMINATED; every remaining scope stopped.
			}
			if e != nil {
				return e
			}
			defer c.Close()
			c.SetRun(id)
			e = c.Cancel(ctx)
		}
		if e != nil {
			return e
		}
	}
	return nil
}
func (a *Activities) Ask(ctx context.Context, p QuestionProjection) (err error) {
	ctx, span := startActivity(ctx, "task.question", "CHAIN", a.activityRun(ctx, p.RunID), p.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.question"); span.End() }()
	if err := a.Store.ProjectQuestion(ctx, p.RunID, p.Revision, p.Question); err != nil {
		return classified(err)
	}
	return nil
}
func (a *Activities) Publish(ctx context.Context, p Publication) (result state.Run, resultErr error) {
	run := a.activityRun(ctx, p.RunID)
	ctx, span := startActivity(ctx, "task.publish", "CHAIN", run, p.Revision)
	defer func() {
		span.SetAttributes(attribute.String("nori.task.status", string(result.Status)), attribute.Bool("nori.output.present", result.Result != ""))
		recordActivityFailure(ctx, resultErr, "task.publish")
		if resultErr == nil && result.Status == state.RunFailed && !p.ExpectedIncomplete {
			span.SetStatus(codes.Error, "task failed")
			if run.Status == state.RunRunning || run.Status == "" {
				observability.RecordErrorCategory(ctx, "task.failed", errors.New("task failed"))
			}
		}
		span.End()
	}()
	if p.Status != state.RunCompleted {
		current, err := a.Store.GetRun(ctx, p.RunID)
		if err != nil {
			return state.Run{}, classified(err)
		}
		// Terminal cleanup cannot resume execution. Stop and unknown-cost facts
		// apply to the whole task, including a correction accepted during cleanup.
		if current.CancelRequested && p.StopConfirmed {
			p.Status, p.Detail, p.Text, p.Revision = state.RunCancelled, prompts.Runtime("stopped", nil), "", current.Revision
			p.BypassRouting = true
		} else if !p.StopConfirmed || p.Revision == 0 {
			p.Revision = current.Revision
		}
	}
	r, err := a.Store.Publish(ctx, state.PublishRequest{
		RunID: p.RunID, Revision: p.Revision, Status: p.Status, Detail: p.Detail, Text: p.Text,
		CostUnknown: p.CostUnknown, Spent: p.Spent, BypassRouting: p.BypassRouting, Answer: p.Answer, ExpectedIncomplete: p.ExpectedIncomplete,
	})
	return r, classified(err)
}
func (a *Activities) Project(ctx context.Context, p Projection) error {
	return classified(a.Store.Project(ctx, state.ProjectRequest{RunID: p.RunID, Revision: p.Revision, Kind: p.Kind, Text: p.Text}))
}
func (a *Activities) CloseBrowser(ctx context.Context, in BrowserCleanup) (err error) {
	run := a.activityRun(ctx, in.RunID)
	ctx, span := startActivity(ctx, "task.browser_cleanup", "CHAIN", run, run.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.browser_cleanup"); span.End() }()
	computerCtx := observability.SuppressErrorCapture(ctx)
	c, err := computer.Attach(computerCtx, a.Agent.Computer, in.Scope)
	if errors.Is(err, computer.ErrStopped) {
		return a.Store.FinishCleanup(ctx, in.RunID, "")
	}
	if err != nil {
		return classified(err)
	}
	defer c.Close()
	if err = c.CloseTaskBrowser(computerCtx, in.RunID); err != nil {
		return classified(err)
	}
	return a.Store.FinishCleanup(ctx, in.RunID, "")
}

// Spend is unguarded: cost already incurred is recorded even if a correction
// superseded the revision that spent it.
func (a *Activities) Spend(ctx context.Context, s Spending) error {
	return a.Store.Spend(ctx, state.SpendUpdate{RunID: s.RunID, Spent: s.Spent, Budget: s.Budget, CostUnknown: s.CostUnknown})
}
