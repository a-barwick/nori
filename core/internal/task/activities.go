package task

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"nori/internal/agent"
	"nori/internal/contextview"
	"nori/internal/extensions"
	"nori/internal/homeai"
	"nori/internal/invoke"
	"nori/internal/llmguard"
	"nori/internal/observability"
	"nori/internal/permissions"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/transcript"
	"nori/internal/usage"
)

type Activities struct {
	Agent                 *agent.Service
	ModelForRun           func(*state.ModelSelection) agent.Model
	HomeModelAvailability func(context.Context, *state.ModelSelection) (bool, error)
	AccountModel          func(context.Context, *state.ModelSelection) agent.Model
	Store                 state.TaskStore
	Executor              *Executor
	UserID                string
	Wakes                 WakeNotifier
	Now                   func() time.Time
	Sleep                 func(context.Context, time.Duration) error
	NewTicker             func(time.Duration) (<-chan time.Time, func())
}

type receiptDeadlineKey struct{}

func withReceiptDeadline(ctx context.Context, deadline time.Time) context.Context {
	return context.WithValue(ctx, receiptDeadlineKey{}, deadline.UTC().Truncate(time.Microsecond))
}

func (a *Activities) receiptDeadline(ctx context.Context) time.Time {
	if v, ok := ctx.Value(receiptDeadlineKey{}).(time.Time); ok && !v.IsZero() {
		return v.UTC().Truncate(time.Microsecond)
	}
	return receiptDeadline(ctx)
}

func (a *Activities) now() time.Time {
	if a != nil && a.Now != nil {
		return a.Now()
	}
	if a != nil && a.Executor != nil && a.Executor.Now != nil {
		return a.Executor.Now()
	}
	return time.Now()
}

func (a *Activities) sleep(ctx context.Context, d time.Duration) error {
	if a != nil && a.Sleep != nil {
		return a.Sleep(ctx, d)
	}
	if a != nil && a.Executor != nil && a.Executor.Sleep != nil {
		return a.Executor.Sleep(ctx, d)
	}
	return defaultSleep(ctx, d)
}

func defaultSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
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

func (a *Activities) newTicker(interval time.Duration) (<-chan time.Time, func()) {
	if a != nil && a.NewTicker != nil {
		return a.NewTicker(interval)
	}
	ticker := time.NewTicker(interval)
	return ticker.C, ticker.Stop
}

type Step struct {
	RunID    string
	Revision int64
}
type ModelInput struct {
	Step
	ModelID       string
	Messages      []transcript.Message
	Interruptible bool
	Subtask       bool
	ContextRetry  bool
}

type ModelOutcome struct {
	Response        agent.Response
	Failure         string
	HomeUnavailable bool
	ContextOverflow bool
}
type ToolInput struct {
	Step
	OperationID       string
	Call              transcript.ToolCall
	Exec              *extensions.Exec `json:",omitempty"`
	ReadOnly          bool             `json:",omitempty"`
	ParentOperationID string           `json:",omitempty"`
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
	Parts              []state.Part
}
type Projection struct {
	Step
	Kind, Text string
}
type QuestionProjection struct {
	Step
	Question state.Question
}
type Spending struct {
	RunID         string
	Spent, Budget float64
	CostUnknown   bool
}

func (a *Activities) Context(ctx context.Context, id string) ([]transcript.Message, error) {
	session, e := a.Store.Session(ctx)
	if e != nil {
		return nil, e
	}
	pinnedRunIDs := map[string]bool{}
	var currentRun *state.Run
	for i := range session.Runs {
		if session.Runs[i].ID == id {
			currentRun = &session.Runs[i]
			break
		}
	}
	if currentRun != nil {
		if currentRun.Origin != nil && currentRun.Origin.ContinuesTaskID != "" {
			pinnedRunIDs[currentRun.Origin.ContinuesTaskID] = true
		}
		if currentRun.Revises != "" {
			if res, rErr := a.Store.GetResult(ctx, currentRun.Revises); rErr == nil && res.RunID != "" {
				pinnedRunIDs[res.RunID] = true
			}
		}
	}

	completedRuns := make([]state.Run, 0, len(session.Runs))
	completedSet := map[string]bool{}
	for _, run := range session.Runs {
		if run.ID != id && run.Status == state.RunCompleted {
			completedRuns = append(completedRuns, run)
			completedSet[run.ID] = true
		}
	}
	if len(completedRuns) == 0 {
		return nil, nil
	}
	slices.SortStableFunc(completedRuns, func(a, b state.Run) int {
		if a.StartedAt.After(b.StartedAt) {
			return -1
		}
		if b.StartedAt.After(a.StartedAt) {
			return 1
		}
		return 0
	})

	const (
		maxIndexTasks     = 8
		maxIndexBytes     = 3 << 10
		maxPinnedBytes    = 72 << 10
		maxRequestPreview = 120
		maxResultPreview  = 160
	)
	records := []json.RawMessage{}
	total := 0
	if len(pinnedRunIDs) > 0 {
		for i := len(session.Messages) - 1; i >= 0; i-- {
			m := session.Messages[i]
			if !pinnedRunIDs[m.RunID] || !completedSet[m.RunID] {
				continue
			}
			rec, e := json.Marshal(map[string]string{"task_id": m.RunID, "status": "completed", "role": m.Role, "text": clip(m.Text, maxPinnedBytes)})
			if e != nil {
				return nil, e
			}
			if total+len(rec) > maxPinnedBytes {
				break
			}
			records = append([]json.RawMessage{rec}, records...)
			total += len(rec)
		}
	}
	indexBytes := 0
	indexed := 0
	for _, run := range completedRuns {
		if pinnedRunIDs[run.ID] {
			continue
		}
		if indexed >= maxIndexTasks {
			break
		}
		entry := map[string]string{
			"task_id": run.ID,
			"status":  "completed",
			"request": clip(run.Request, maxRequestPreview),
		}
		if run.Result != "" {
			entry["summary"] = clip(run.Result, maxResultPreview)
		}
		rec, e := json.Marshal(entry)
		if e != nil {
			return nil, e
		}
		if indexBytes+len(rec) > maxIndexBytes {
			break
		}
		records = append(records, rec)
		indexBytes += len(rec)
		indexed++
	}
	if len(records) == 0 {
		return nil, nil
	}
	history, e := json.Marshal(records)
	if e != nil {
		return nil, e
	}
	background := prompts.Runtime("task-history-start", nil) + string(history) +
		prompts.Runtime("task-history-end", nil)
	return []transcript.Message{transcript.UserText(background)}, nil
}
func (a *Activities) Reconcile(ctx context.Context, id string) (state.TaskSnapshot, error) {
	snap, e := a.Store.Reconcile(ctx, id)
	if e != nil {
		return snap, e
	}
	if snap.Run.CancelRequested {
		return snap, state.ErrCancelled
	}
	if snap.Run.Status != state.RunRunning || snap.Run.Finishing != nil {
		return snap, state.ErrBusy
	}
	e = a.Store.Incorporate(ctx, state.IncorporateRequest{RunID: id, Revision: snap.Run.Revision, Position: snap.Run.Position})
	return snap, e
}

const boundedSafetyTick = 10 * time.Second

func (a *Activities) bounded(parent context.Context, step Step, interruptible bool) (context.Context, func(), state.Run, error) {
	var wakeCh <-chan struct{}
	var unsub func()
	if a != nil && a.Wakes != nil && a.UserID != "" {
		wakeCh, unsub = a.Wakes.Subscribe(a.UserID, step.RunID)
	}
	run, e := a.Store.Guard(parent, step.RunID, step.Revision)
	if e != nil {
		if unsub != nil {
			unsub()
		}
		return nil, nil, state.Run{}, e
	}
	ctx, cancel := context.WithCancelCause(usage.WithScope(parent, step.RunID, ""))
	done := make(chan struct{})
	check := func() bool {
		current, err := a.Store.GetRun(ctx, step.RunID)
		if err != nil {
			cancel(err)
			return false
		}
		if current.CancelRequested {
			cancel(state.ErrCancelled)
			return false
		}
		if current.Status != state.RunRunning || current.Finishing != nil {
			cancel(state.ErrBusy)
			return false
		}
		if interruptible && current.InterruptRevision > step.Revision {
			cancel(state.ErrSuperseded)
			return false
		}
		control, err := a.Store.Control(ctx)
		if err != nil {
			cancel(err)
			return false
		}
		if err = control.Check(); err != nil {
			cancel(err)
			return false
		}
		return true
	}
	go func() {
		defer close(done)
		if unsub != nil {
			defer unsub()
		}
		tickCh, stopTick := a.newTicker(boundedSafetyTick)
		defer stopTick()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-wakeCh:
				if !ok {
					wakeCh = nil
					continue
				}
				if !check() {
					return
				}
			case <-tickCh:
				if !check() {
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(nil); <-done }, run, nil
}

func (a *Activities) agentForRun(ctx context.Context, run state.Run) *agent.Service {
	svc := a.Agent
	if a.AccountModel != nil {
		cp := *svc
		cp.Model = a.AccountModel(ctx, run.ModelSelection)
		svc = &cp
	} else if a.ModelForRun != nil && !run.ModelSelection.IsEmpty() {
		if resolved := a.ModelForRun(run.ModelSelection); resolved != nil {
			cp := *a.Agent
			cp.Model = resolved
			svc = &cp
		}
	}
	if isUnattendedOrigin(run) {
		cp := *svc
		cp.ToolOrigin = invoke.OriginAutomation
		svc = &cp
	}
	return svc
}

func (a *Activities) Model(parent context.Context, in ModelInput) (ModelOutcome, error) {
	ctx, done, run, e := a.bounded(parent, in.Step, in.Interruptible)
	if e != nil {
		return ModelOutcome{Response: agent.Response{NotSent: true}}, e
	}
	defer done()
	ctx, span := startActivity(ctx, "task.model", "AGENT", run, in.Revision)
	defer span.End()
	traceID := in.ModelID
	if traceID == "" {
		traceID = uuid.NewString()
	}
	svc := a.agentForRun(ctx, run)
	trace := state.TaskStep{
		ID:   traceID,
		Kind: "model", Status: "running", Revision: in.Revision, StartedAt: a.now().UTC(),
		Model: svc.Model.Name(), CostStatus: "unknown",
	}
	if in.ContextRetry {
		trace.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(trace.ID+":context")).String()
	}
	a.recordStep(in.RunID, trace)
	modelCtx, stop := context.WithDeadline(ctx, modelRequestDeadline(ctx, time.Now()))
	defer stop()
	response, e := svc.Decide(observability.SuppressErrorCapture(modelCtx), in.Messages, true, !in.Subtask)
	span.SetAttributes(
		attribute.Bool("nori.output.present", response.Answer != ""),
		attribute.Int("nori.tool.call_count", len(response.Calls)),
		attribute.Int("nori.web_search.call_count", len(response.WebSearches)),
	)
	recordActivityFailure(modelCtx, e, "task.model")
	outcome := ModelOutcome{Response: response, ContextOverflow: errors.Is(e, contextview.ErrOverflow)}
	if svc.Model.Provider() == llmguard.ProviderHome &&
		(errors.Is(e, homeai.ErrOffline) || errors.Is(e, homeai.ErrBusy) || errors.Is(e, homeai.ErrOllamaUnavailable)) {
		outcome.HomeUnavailable = true
		outcome.Response.NotSent = true
	}
	if e != nil {
		outcome.Failure = e.Error()
		if errors.Is(modelCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			outcome.Failure = modelTimeoutFailure
		}
	}
	if response.Usage != nil {
		u := response.Usage
		input, output, cached, thinking := u.InputTokens, u.OutputTokens, u.CachedTokens, u.ReasoningTokens
		trace.InputTokens, trace.OutputTokens, trace.CachedTokens, trace.ThinkingTokens = &input, &output, &cached, &thinking
	}
	if response.CostLocal {
		trace.CostStatus = "local"
	} else if response.CostKnown {
		trace.CostUSD, trace.CostStatus = &response.Cost, "reported"
		if svc.Model.Provider() == "openai" {
			trace.CostStatus = "calculated"
		}
	} else if response.NotSent {
		trace.CostStatus = "not_sent"
	}
	for _, ws := range response.WebSearches {
		trace.Calls = append(trace.Calls, state.TraceCall{Name: "web_search", Arguments: traceJSON(ws)})
	}
	for _, call := range response.Calls {
		trace.Calls = append(trace.Calls, state.TraceCall{Name: call.Name, Arguments: traceJSON(call.Args)})
	}
	trace.Result = traceText(response.Answer)
	a.finishStep(&trace, e)
	a.recordStep(in.RunID, trace)
	if e != nil {
		if parent.Err() != nil {
			return ModelOutcome{Response: response}, parent.Err()
		}
		if ctx.Err() != nil {
			cause := context.Cause(ctx)
			if errors.Is(cause, state.ErrCancelled) || errors.Is(cause, state.ErrPaused) ||
				errors.Is(cause, state.ErrSuperseded) || errors.Is(cause, state.ErrBusy) {
				return ModelOutcome{Response: response}, cause
			}
		}
	}
	return outcome, nil
}

const (
	modelRequestLimit   = 2 * time.Minute
	modelSaveReserve    = 15 * time.Second
	modelTimeoutFailure = "The model did not answer within 2 minutes, so the task stopped. This step changed nothing; ask again to retry."
	modelSaveFailure    = "Nori could not store the model's answer, so the task stopped. This step changed nothing; ask again to retry."
)

func modelRequestDeadline(ctx context.Context, now time.Time) time.Time {
	until := now.Add(modelRequestLimit)
	if deadline, ok := ctx.Deadline(); ok && deadline.Add(-modelSaveReserve).Before(until) {
		until = deadline.Add(-modelSaveReserve)
	}
	return until
}

func (a *Activities) finishStep(step *state.TaskStep, err error) {
	now := a.now().UTC()
	step.FinishedAt, step.DurationMS, step.Status = &now, now.Sub(step.StartedAt).Milliseconds(), "completed"
	if err != nil {
		step.Status, step.Error = "failed", traceText(err.Error())
	}
}

func (a *Activities) recordStep(runID string, step state.TaskStep) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Store.RecordTaskStep(ctx, runID, step); err != nil {
		slog.WarnContext(ctx, "task trace unavailable", "step_id", step.ID, "err", err)
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
func questionPayloadJSON(q state.Question) json.RawMessage {
	input := q.Input
	if input == "" {
		input = "text"
	}
	payload := struct {
		Input   string                 `json:"input"`
		Options []state.QuestionOption `json:"options,omitempty"`
	}{
		Input:   input,
		Options: q.Options,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage(`{"input":"text"}`)
	}
	return data
}

func isStructuredQuestion(q state.Question) bool {
	if len(q.Clears) > 0 {
		return false
	}
	switch q.Input {
	case "choice", "multi_choice", "confirm":
		return true
	default:
		return false
	}
}

func (a *Activities) Ask(ctx context.Context, p QuestionProjection) (err error) {
	ctx, span := startActivity(ctx, "task.question", "CHAIN", a.activityRun(ctx, p.RunID), p.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.question"); span.End() }()
	run, err := a.Store.Guard(ctx, p.RunID, p.Revision)
	if err != nil {
		return err
	}
	if p.Question.Kind != state.QuestionWork && a.Executor != nil && a.Executor.Permissions != nil {
		if _, lookupErr := a.Executor.Permissions.Interaction(ctx, p.Question.ID); lookupErr == nil {
			// An approval, sign-in, or account-selection card was already opened for this question.
			p.Question.Interactive = true
		} else if isStructuredQuestion(p.Question) {
			revision := p.Revision
			title := state.TaskTitle(run)
			if _, err := a.Executor.Permissions.Open(ctx, permissions.Interaction{
				ID: p.Question.ID, TaskID: p.RunID, InstructionRevision: &revision, Kind: "question", Prompt: p.Question.Text,
				Payload: questionPayloadJSON(p.Question), LegacyQuestionID: p.Question.ID, TaskTitle: title,
			}, ""); err != nil {
				return err
			}
			p.Question.Interactive = true
		}
	}
	return a.Store.ProjectQuestion(ctx, p.RunID, p.Revision, p.Question)
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
			return state.Run{}, err
		}
		if current.CancelRequested && p.StopConfirmed {
			p.Status, p.Detail, p.Text, p.Revision = state.RunCancelled, prompts.Runtime("stopped", nil), "", current.Revision
			p.BypassRouting = true
		} else if !p.StopConfirmed || p.Revision == 0 {
			p.Revision = current.Revision
		}
	}
	r, err := a.Store.Publish(ctx, state.PublishRequest{
		RunID: p.RunID, Revision: p.Revision, Status: p.Status, Detail: p.Detail, Text: p.Text,
		CostUnknown: p.CostUnknown, Spent: p.Spent, BypassRouting: p.BypassRouting, Answer: p.Answer,
		ExpectedIncomplete: p.ExpectedIncomplete, Parts: p.Parts,
	})
	return r, err
}
func (a *Activities) Project(ctx context.Context, p Projection) error {
	return a.Store.Project(ctx, state.ProjectRequest{RunID: p.RunID, Revision: p.Revision, Kind: p.Kind, Text: p.Text})
}

func (a *Activities) Spend(ctx context.Context, s Spending) error {
	return a.Store.Spend(ctx, state.SpendUpdate{RunID: s.RunID, Spent: s.Spent, Budget: s.Budget, CostUnknown: s.CostUnknown})
}
