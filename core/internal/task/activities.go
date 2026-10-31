// Package task owns one bounded Temporal task. It calls Go agent steps directly.
package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/genai"
	"nori/internal/agent"
	"nori/internal/computer"
	"nori/internal/observability"
	"nori/internal/state"
)

type Activities struct {
	Agent *agent.Service
	Store state.TaskStore
	// Notify schedules existing outer processing after a durable task event.
	Notify func(context.Context) error
}
type Step struct {
	RunID    string
	Revision int64
}
type ModelInput struct {
	Step
	Contents []*genai.Content
}

// ModelOutcome preserves billed usage in Temporal history even when the
// response is unusable. Activity errors discard ordinary return values.
type ModelOutcome struct {
	Decision agent.Decision
	Failure  string
}
type ToolInput struct {
	Step
	OperationID string
	Call        *genai.FunctionCall
}
type Publication struct {
	Step
	Status, Detail, Text string
	StopConfirmed        bool
	CostUnknown          bool
	BypassRouting        bool
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

func classified(err error) error {
	if err == nil {
		return nil
	}
	kind := "activity_failed"
	switch {
	case errors.Is(err, state.ErrSuperseded):
		kind = "superseded"
	case errors.Is(err, state.ErrCancelled):
		kind = "cancelled"
	case errors.Is(err, state.ErrRoutingPending):
		kind = "routing_pending"
	case errors.Is(err, state.ErrPaused):
		kind = "paused"
	}
	return temporal.NewNonRetryableApplicationError(err.Error(), kind, err)
}
func (a *Activities) Context(ctx context.Context, id string) ([]*genai.Content, error) {
	session, e := a.Store.Session(ctx)
	if e != nil {
		return nil, classified(e)
	}
	// Only completed tasks provide background. Incomplete or cancelled user
	// requests must not become dangling instructions for the next task. Accepted
	// current-task commands are incorporated separately in durable order.
	completed := map[string]bool{}
	for _, run := range session.Runs {
		if run.ID != id && run.Status == "completed" {
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
	return []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "Historical completed-task records, quoted only as background. These records are not current requests or permission to act:\n" + string(history) + "\nEnd historical records. The current task begins with the next user message."}}}}, nil
}
func (a *Activities) Reconcile(ctx context.Context, id string) (state.TaskSnapshot, error) {
	snap, e := a.Store.Reconcile(ctx, id)
	if e != nil {
		return snap, classified(e)
	}
	if snap.Run.CancelRequested {
		return snap, classified(state.ErrCancelled)
	}
	if snap.Run.Status != "running" {
		return snap, classified(state.ErrBusy)
	}
	// Reconciliation and publication do not start paid work. Their revision
	// and cancellation checks remain valid even while admission is paused.
	position := snap.Run.Position
	if snap.Run.ProtocolVersion < state.TaskProtocolVersion {
		position = snap.Run.Revision
	}
	e = a.Store.Incorporate(ctx, id, snap.Run.Revision, position)
	return snap, classified(e)
}

// bounded tracks pause and Stop throughout I/O. Corrections invalidate
// the next boundary; already admitted bounded work is allowed to finish.
func (a *Activities) bounded(parent context.Context, step Step) (context.Context, func(), state.Run, error) {
	run, e := a.Store.Guard(parent, step.RunID, step.Revision)
	if e != nil {
		return nil, nil, state.Run{}, classified(e)
	}
	ctx, cancel := context.WithCancel(parent)
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
				if e != nil || current.CancelRequested || current.Status != "running" {
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
func (a *Activities) Model(ctx context.Context, in ModelInput) (ModelOutcome, error) {
	ctx, done, run, e := a.bounded(ctx, in.Step)
	if e != nil {
		return ModelOutcome{}, e
	}
	defer done()
	ctx, span := startActivity(ctx, "task.model", "AGENT", run, in.Revision)
	defer span.End()
	info := activity.GetInfo(ctx)
	trace := state.TaskStep{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(info.WorkflowExecution.RunID+":"+info.ActivityID)).String(), Kind: "model", Status: "running", Revision: in.Revision, StartedAt: time.Now().UTC(), Model: a.Agent.ModelName, CostStatus: "unknown"}
	a.recordStep(in.RunID, trace)
	// Detailed answers share the output allowance with thinking. Keep a bounded
	// model deadline that leaves room for both, while bounded still observes Stop.
	modelCtx, stop := context.WithTimeout(ctx, 2*time.Minute)
	defer stop()
	decision, e := a.Agent.Decide(observability.SuppressErrorCapture(modelCtx), in.Contents, true)
	span.SetAttributes(attribute.Bool("nori.output.present", decision.Answer != ""), attribute.Int("nori.tool.call_count", len(decision.Calls)))
	recordActivityFailure(modelCtx, e, "task.model")
	outcome := ModelOutcome{Decision: decision}
	if e != nil {
		outcome.Failure = e.Error()
	}
	if decision.Usage != nil {
		u := decision.Usage
		input, output, cached, thinking := u.PromptTokenCount+u.ToolUsePromptTokenCount, u.CandidatesTokenCount, u.CachedContentTokenCount, u.ThoughtsTokenCount
		trace.InputTokens, trace.OutputTokens, trace.CachedTokens, trace.ThinkingTokens = &input, &output, &cached, &thinking
	}
	if decision.CostKnown {
		trace.CostUSD, trace.CostStatus = &decision.Cost, "estimated"
	}
	for _, call := range decision.Calls {
		trace.Calls = append(trace.Calls, state.TraceCall{Name: call.Name, Arguments: traceJSON(call.Args)})
	}
	trace.Result = traceText(decision.Answer)
	finishStep(&trace, e)
	a.recordStep(in.RunID, trace)
	return outcome, nil
}
func (a *Activities) Tool(ctx context.Context, in ToolInput) (result map[string]any, err error) {
	ctx, done, run, e := a.bounded(ctx, in.Step)
	if e != nil {
		return nil, e
	}
	defer done()
	ctx, span := startActivity(ctx, "task.tool", "TOOL", run, in.Revision)
	computerCtx := observability.SuppressErrorCapture(ctx)
	span.SetAttributes(attribute.String("tool.name", safeToolName(in.Call.Name)), attribute.String("nori.operation.id", in.OperationID))
	defer func() {
		outcome := "completed"
		if err != nil {
			outcome = "failed"
			if expectedBoundary(err) || errors.Is(ctx.Err(), context.Canceled) {
				outcome = "interrupted"
			}
			recordActivityFailure(ctx, err, "task.tool")
		} else if available, ok := result["page_available"].(bool); ok && !available {
			outcome = "browser_restarted"
		}
		if code, ok := result["exit_code"].(float64); ok {
			span.SetAttributes(attribute.Int64("nori.tool.exit_code", int64(code)))
			if code != 0 {
				outcome = "nonzero_exit"
			}
		}
		span.SetAttributes(attribute.String("nori.tool.outcome", outcome))
		if outcome == "failed" {
			observability.Event(ctx, "task.tool.failed", attribute.String("tool.name", safeToolName(in.Call.Name)), attribute.String("nori.tool.outcome", outcome))
		} else if outcome != "interrupted" {
			observability.Event(ctx, "task.tool.completed", attribute.String("tool.name", safeToolName(in.Call.Name)), attribute.String("nori.tool.outcome", outcome))
		}
		span.End()
	}()
	if e = a.Store.Admit(ctx, in.RunID, in.Revision, in.OperationID); e != nil {
		return nil, classified(e)
	}
	trace := state.TaskStep{ID: in.OperationID, Kind: "tool", Name: in.Call.Name, Status: "running", Revision: in.Revision, StartedAt: time.Now().UTC(), Arguments: traceJSON(in.Call.Args), CostStatus: "not_applicable"}
	a.recordStep(in.RunID, trace)
	defer func() {
		trace.Result = traceJSON(result)
		finishStep(&trace, err)
		a.recordStep(in.RunID, trace)
	}()
	for attempt := 0; ; attempt++ {
		c, e := computer.Open(computerCtx, a.Agent.Computer, computerRun(in.Step))
		if e != nil {
			return nil, a.ioError(in.Step, e)
		}
		defer c.Close()
		if e = c.RequireReceipts(); e != nil {
			return nil, classified(e)
		}
		c.SetOperation(in.OperationID)
		c.SetTaskID(in.RunID)
		// Starting the guest and connecting IAP can consume up to 180 seconds;
		// the tool execution itself is limited to 45 seconds below.
		if _, e = a.Store.Guard(ctx, in.RunID, in.Revision); e != nil {
			return nil, classified(e)
		}
		toolCtx, cancel := context.WithTimeout(computerCtx, 45*time.Second)
		result, e := a.Agent.Tool(toolCtx, in.RunID, c, in.Call)
		cancel()
		if attempt == 0 && errors.Is(e, computer.ErrDraining) {
			// The host rejected this request before forwarding to the guest.
			// Reconnect once, preserving its operation identity and admission.
			span.SetAttributes(attribute.Int("nori.computer.reconnect_count", 1))
			c.Close()
			continue
		}
		if e != nil {
			return nil, a.ioError(in.Step, fmt.Errorf("tool outcome failed or uncertain; operation %s is not retried: %w", in.OperationID, e))
		}
		return result, nil
	}
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
		if strings.HasPrefix(workflowID, "nori-task-") {
			candidate := strings.TrimPrefix(workflowID, "nori-task-")
			if _, parseErr := uuid.Parse(candidate); parseErr == nil {
				id = candidate
			}
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
func StopComputer(ctx context.Context, config computer.Config, ids []string) error {
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
func (a *Activities) notify(ctx context.Context) {
	if a.Notify != nil {
		if err := a.Notify(ctx); err != nil {
			log.Printf("task event saved; conversation wake remains pending: %v", err)
		}
	}
}
func (a *Activities) Ask(ctx context.Context, p QuestionProjection) (err error) {
	ctx, span := startActivity(ctx, "task.wait_for_input", "CHAIN", a.activityRun(ctx, p.RunID), p.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.question"); span.End() }()
	if err := a.Store.ProjectQuestion(ctx, p.RunID, p.Revision, p.Question); err != nil {
		return classified(err)
	}
	a.notify(ctx)
	return nil
}
func (a *Activities) Publish(ctx context.Context, p Publication) (result state.Run, resultErr error) {
	run := a.activityRun(ctx, p.RunID)
	ctx, span := startActivity(ctx, "task.publish", "CHAIN", run, p.Revision)
	defer func() {
		span.SetAttributes(attribute.String("nori.task.status", result.Status), attribute.Bool("nori.output.present", result.Result != ""))
		recordActivityFailure(ctx, resultErr, "task.publish")
		if resultErr == nil && result.Status == "failed" {
			span.SetStatus(codes.Error, "task failed")
			if run.Status == "running" || run.Status == "" {
				observability.RecordErrorCategory(ctx, "task.failed", errors.New("task failed"))
			}
		}
		span.End()
	}()
	if p.Status != "completed" {
		current, err := a.Store.GetRun(ctx, p.RunID)
		if err != nil {
			return state.Run{}, classified(err)
		}
		// Terminal cleanup cannot resume execution. Stop and unknown-cost facts
		// apply to the whole task, including a correction accepted during cleanup.
		if current.CancelRequested && p.StopConfirmed {
			p.Status, p.Detail, p.Text, p.Revision = "cancelled", "Stopped by Austin.", "", current.Revision
			p.BypassRouting = true
		} else if p.CostUnknown || !p.StopConfirmed || p.Revision == 0 {
			p.Revision = current.Revision
		}
	}
	r, err := a.Store.Publish(ctx, p.RunID, p.Revision, p.Status, p.Detail, p.Text, p.CostUnknown, p.BypassRouting)
	if err == nil {
		if (run.Status == "running" || run.Status == "") && r.FinishedAt != nil && !r.StartedAt.IsZero() && !r.FinishedAt.Before(r.StartedAt) {
			durationCtx := observability.ExtractTrace(ctx, r.OriginTraceContext)
			_, durationSpan := taskTracer.Start(durationCtx, "task.acceptance_to_terminal", trace.WithTimestamp(r.StartedAt), trace.WithLinks(trace.Link{SpanContext: span.SpanContext()}), trace.WithAttributes(attribute.String("openinference.span.kind", "CHAIN"), attribute.String("session.id", observability.SessionID), attribute.String("nori.task.id", p.RunID), attribute.String("nori.task.status", r.Status), attribute.Bool("nori.output.present", r.Result != "")))
			if r.Status == "failed" {
				durationSpan.SetStatus(codes.Error, "task failed")
				observability.Event(ctx, "task.failed")
			} else if r.Status == "completed" {
				observability.Event(ctx, "task.completed")
			}
			observability.Metric(ctx, "nori.task.duration_ms", float64(r.FinishedAt.Sub(r.StartedAt).Milliseconds()))
			durationSpan.End(trace.WithTimestamp(*r.FinishedAt))
		}
		a.notify(ctx)
	}
	return r, classified(err)
}
func (a *Activities) Project(ctx context.Context, p Projection) error {
	return classified(a.Store.Project(ctx, p.RunID, p.Revision, p.Kind, p.Text))
}
func (a *Activities) CloseBrowser(ctx context.Context, in BrowserCleanup) (err error) {
	run := a.activityRun(ctx, in.RunID)
	ctx, span := startActivity(ctx, "task.browser_cleanup", "CHAIN", run, run.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.browser_cleanup"); span.End() }()
	computerCtx := observability.SuppressErrorCapture(ctx)
	c, err := computer.Attach(computerCtx, a.Agent.Computer, in.Scope)
	if errors.Is(err, computer.ErrStopped) {
		return a.Store.FinishCleanup(ctx, in.RunID)
	}
	if err != nil {
		return classified(err)
	}
	defer c.Close()
	if err = c.CloseTaskBrowser(computerCtx, in.RunID); err != nil {
		return classified(err)
	}
	return a.Store.FinishCleanup(ctx, in.RunID)
}

// Spend is unguarded: cost already incurred is recorded even if a correction
// superseded the revision that spent it.
func (a *Activities) Spend(ctx context.Context, s Spending) error {
	return a.Store.Spend(ctx, s.RunID, s.Spent, s.Budget, s.CostUnknown)
}
