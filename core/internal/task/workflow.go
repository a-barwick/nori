package task

import (
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"nori/internal/agent"
	"nori/internal/prompts"
	"nori/internal/state"
)

const WorkflowName = "nori-task-v3"
const Signal = "task-command"

func superseded(err error) bool { return applicationKind(err, activitySuperseded) }

func applicationKind(err error, kind string) bool {
	var application *temporal.ApplicationError
	return errors.As(err, &application) && application.Type() == kind
}

// Checkpoint is deliberately bounded: exact model state lives in the immutable
// history chain, while accounting and user-control state survive ContinueAsNew.
type Checkpoint struct {
	Ref                   string
	Revision, Position    int64
	Spent, Budget         float64
	CostUnknown           bool
	Models, Tools, Checks int
	Question              *state.Question
	QuestionPublished     bool
	ComputerRevisions     []int64
	Stopped               bool
	Request               string
	// Jobs are started and not yet observed ended. They outlive a revision, so
	// only terminal cleanup stops them, not a correction or a question.
	Jobs        []string
	JobsStarted bool
	// Others is what the rest of this task's family has spent, read from the store before each budget check. A
	// subtask inherits a read-only limit from its parent, and that limit outlasts a correction.
	Others             float64
	Subtask, Inherited bool
}

func Workflow(ctx workflow.Context, id string, resume Checkpoint) (result state.Run, err error) {
	r := newTaskRun(ctx, id, resume)
	continuing := false
	defer func() {
		if continuing {
			return
		}
		if e := r.cleanup(); e != nil {
			err = e
		}
	}()
	for {
		continuing, err = r.run()
		if continuing || err == nil {
			return r.result, err
		}
		var failure, publishErr error
		r.result, failure, publishErr = r.c.publishFailure(r.ctx, r.stopCtx, id, err, r.wait, r.result)
		if superseded(publishErr) && r.ctx.Err() == nil {
			continue
		}
		if publishErr != nil {
			return r.result, fmt.Errorf("%w; terminal persistence failed: %v", failure, publishErr)
		}
		return r.result, failure
	}
}

// taskRun is one execution of Workflow: the checkpoint it advances, the activity contexts it uses and the
// result it publishes. Its methods issue activities in the same order the workflow always has, so replay holds.
type taskRun struct {
	id     string
	c      Checkpoint
	result state.Run
	// recovering retries delivery of model and tool checkpoints; quick runs bookkeeping; syncing may repeat a history sync;
	// stopCtx allows for stopping the computer; spending projects spend.
	ctx, quick, syncing, recovering, stopCtx, spending workflow.Context
	signals                                            workflow.ReceiveChannel
}

func newTaskRun(ctx workflow.Context, id string, resume Checkpoint) *taskRun {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 240 * time.Second, HeartbeatTimeout: 10 * time.Second, WaitForCancellation: true,
		RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	r := &taskRun{
		id:  id,
		c:   resume,
		ctx: ctx,
		quick: workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 20 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1},
		}),
		// A sync only appends to the history and saves evidence, so a worker crash during delivery can safely repeat it.
		syncing: workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 20 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3},
		}),
		recovering: workflow.WithRetryPolicy(ctx, temporal.RetryPolicy{MaximumAttempts: 3}),
		stopCtx: workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 200 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1},
		}),
		spending: workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 5 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3},
		}),
		signals: workflow.GetSignalChannel(ctx, Signal),
	}
	if r.c.Budget == 0 {
		r.c.Stopped = true
	}
	return r
}

func (r *taskRun) step() Step { return Step{r.id, r.c.Revision} }

func (r *taskRun) spend() { r.c.projectSpend(r.spending, r.id) }

func (r *taskRun) wait() error {
	var command state.Command
	r.signals.Receive(r.ctx, &command)
	return r.ctx.Err()
}

// boundary reports whether err only means the task's admission changed, so the step should be retried. A paused
// task first waits for its next command, and a failed wait becomes the returned error.
func (r *taskRun) boundary(err error) (bool, error) {
	if superseded(err) {
		return true, nil
	}
	if applicationKind(err, activityPaused) {
		if err := r.wait(); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, err
}

func (r *taskRun) cleanup() error {
	var a *Activities
	cleanup, _ := workflow.NewDisconnectedContext(r.ctx)
	cleanup = workflow.WithActivityOptions(cleanup, workflow.GetActivityOptions(r.stopCtx))
	r.c.projectSpend(workflow.WithActivityOptions(cleanup, workflow.GetActivityOptions(r.spending)), r.id)
	if workflow.GetVersion(r.ctx, "structured-task-cleanup", workflow.DefaultVersion, 1) != workflow.DefaultVersion {
		// The stored ownership plan includes operations whose replies never reached this checkpoint.
		return workflow.ExecuteActivity(cleanup, a.Cleanup, r.id).Get(cleanup, nil)
	}
	var err error
	if e := r.c.stopTask(cleanup, r.id); e != nil {
		err = fmt.Errorf("computer stop could not be confirmed: %w", e)
	}
	if r.c.Stopped && r.result.Status != "" && len(r.c.ComputerRevisions) > 0 {
		browser := BrowserCleanup{RunID: r.id, Scope: computerRun(Step{r.id, r.c.ComputerRevisions[0]})}
		if e := workflow.ExecuteActivity(cleanup, a.CloseBrowser, browser).Get(cleanup, nil); e != nil {
			workflow.GetLogger(r.ctx).Error("Task browser cleanup remains pending", "run_id", r.id)
		}
	}
	return err
}

func (r *taskRun) publish(p RefPublication) error {
	var a *Activities
	for {
		e := workflow.ExecuteActivity(r.quick, a.PublishRef, p).Get(r.ctx, &r.result)
		if !applicationKind(e, activityRoutingPending) {
			return e
		}
		if e = r.wait(); e != nil {
			return e
		}
	}
}

func (r *taskRun) run() (bool, error) {
	for turns := 0; ; turns++ {
		if turns >= 32 {
			return true, workflow.NewContinueAsNewError(r.ctx, WorkflowName, r.id, r.c)
		}
		done, err := r.turn()
		if done || err != nil {
			return false, err
		}
	}
}

func (r *taskRun) turn() (bool, error) {
	var a *Activities
	c := &r.c
	var signal state.Command
	for r.signals.ReceiveAsync(&signal) {
	}
	var synced HistorySync
	err := workflow.ExecuteActivity(r.syncing, a.SyncHistory, HistoryInput{
		Step: r.step(), Ref: c.Ref, Position: c.Position, Question: c.Question,
	}).Get(r.ctx, &synced)
	if retry, err := r.boundary(err); retry || err != nil {
		return false, err
	}
	c.applySync(synced)
	if c.Question == nil && c.Budget > 0 && c.Spent+c.Others >= c.Budget {
		if err := c.stopTask(r.stopCtx, r.id); err != nil {
			return false, err
		}
		detail := prompts.Runtime("spending-limit", map[string]any{"Budget": c.Budget})
		err := r.publish(RefPublication{Publication: Publication{Step: r.step(), Status: state.RunFailed, Detail: detail, Text: detail, StopConfirmed: true, ExpectedIncomplete: true, Spent: c.Spent, CostUnknown: c.CostUnknown}})
		retry, err := r.boundary(err)
		return !retry, err
	}
	if c.Question != nil {
		return false, r.ask()
	}
	model, err := c.callModel(r.recovering, r.quick, r.spending, r.id)
	if retry, err := r.boundary(err); retry || err != nil {
		return false, err
	}
	if model.Invalid {
		return false, nil
	}
	// Account and retain every model response before checking newer instructions.
	// Close each abandoned call explicitly to preserve the exact Responses prefix.
	var current CurrentPosition
	if err := workflow.ExecuteActivity(r.quick, a.CurrentPosition, r.id).Get(r.ctx, &current); err != nil {
		retry, err := r.boundary(err)
		if !retry {
			return false, err
		}
		for _, call := range model.Calls {
			if err := r.appendResult(call, map[string]any{"skipped": true, "reason": prompts.Runtime("workflow-execution-admission-changed-before-this-call-ran", nil)}); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	changed := current.Revision != c.Revision || current.Position != c.Position
	return r.handleCalls(model.Calls, changed)
}

func (r *taskRun) ask() error {
	var a *Activities
	c := &r.c
	if err := c.stopComputer(r.stopCtx, r.id); err != nil {
		return err
	}
	if !c.QuestionPublished {
		err := workflow.ExecuteActivity(r.quick, a.Ask, QuestionProjection{Step: r.step(), Question: *c.Question}).Get(r.ctx, nil)
		if retry, err := r.boundary(err); retry || err != nil {
			return err
		}
		c.QuestionPublished = true
	}
	return r.wait()
}

type callStep int

const (
	nextCall    callStep = iota // handle the next call
	reviseCalls                 // close the remaining calls unrun: what they assumed has changed
	endCalls                    // stop handling this response and take another turn
	taskDone                    // the task has been published
)

// handleCalls runs a model response's calls in order. Once input has changed or a call has revised the
// task's state, it closes each remaining call without running it.
func (r *taskRun) handleCalls(calls []CallRef, changed bool) (bool, error) {
	revised := false
	for _, call := range calls {
		if changed || revised {
			if err := r.appendResult(call, map[string]any{"skipped": true, "reason": prompts.Runtime("workflow-accepted-input-changed-before-this-call-ran", nil)}); err != nil {
				return false, err
			}
			continue
		}
		next, err := r.handleCall(call)
		if err != nil {
			return false, err
		}
		switch next {
		case taskDone:
			return true, nil
		case endCalls:
			return false, nil
		case reviseCalls:
			revised = true
		}
	}
	return false, nil
}

func (r *taskRun) handleCall(call CallRef) (callStep, error) {
	switch {
	case call.Name == "request_input" || call.Name == "request_work":
		return endCalls, r.requestInput(call)
	case call.Name == "complete" && len(r.c.Jobs) > 0:
		return endCalls, r.appendResult(call, map[string]any{"accepted": false, "reason": prompts.Runtime("workflow-a-background-job-is-still-running-call-job-wait-or-job", nil), "running_jobs": r.c.Jobs})
	case call.Name == "complete":
		return r.complete(call)
	case call.Name == "job_wait":
		ended, err := r.c.awaitJob(r.ctx, r.wait, r.id, call)
		if err != nil {
			return nextCall, err
		}
		if !ended {
			return reviseCalls, r.appendResult(call, map[string]any{"waiting": false, "state": "running", "reason": prompts.Runtime("job-still-running", nil)})
		}
	}
	return r.runTool(call)
}

// appendResult closes a call with a result in place of running it, so the history keeps its exact prefix.
func (r *taskRun) appendResult(call CallRef, response map[string]any) error {
	var a *Activities
	var ref string
	err := workflow.ExecuteActivity(r.quick, a.AppendToolResult, AppendResult{
		Step: r.step(), Ref: r.c.Ref, CallID: call.ID, Name: call.Name, Response: response,
	}).Get(r.ctx, &ref)
	if err != nil {
		return err
	}
	r.c.Ref = ref
	return nil
}

func (r *taskRun) requestInput(call CallRef) error {
	var a *Activities
	c := &r.c
	var q state.Question
	err := workflow.ExecuteActivity(r.quick, a.InputRef, RefInput{Step: r.step(), Ref: c.Ref, Index: call.Index, ReadOnly: c.Inherited}).Get(r.ctx, &q)
	if err != nil {
		return err
	}
	q.ID = fmt.Sprintf("%s-question-m%d", r.id, c.Models)
	if call.Name == "request_work" {
		q.ID = fmt.Sprintf("%s-work-m%d", r.id, c.Models)
	}
	c.Question = &q
	c.QuestionPublished = false
	return nil
}

func (r *taskRun) complete(call CallRef) (callStep, error) {
	var a *Activities
	c := &r.c
	c.Checks++
	var check CompletionOutcome
	err := workflow.ExecuteActivity(r.quick, a.CheckCompletion, CompletionInput{
		Step: r.step(), Ref: c.Ref, ID: fmt.Sprintf("%s-check-%d", r.id, c.Checks),
		Position: c.Position,
	}).Get(r.ctx, &check)
	retry, err := r.boundary(err)
	if retry {
		return reviseCalls, r.appendResult(call, map[string]any{
			"accepted": false,
			"reason":   prompts.Runtime("workflow-execution-admission-changed-during-completion-checking", nil),
		})
	}
	if err != nil {
		return nextCall, err
	}
	c.Ref = check.Ref
	switch check.Action {
	case "publish", "partial":
		return r.publishCompletion(call, check)
	case "reconcile":
	default:
		return nextCall, errors.New("completion check returned an invalid decision")
	}
	return endCalls, nil
}

func (r *taskRun) publishCompletion(call CallRef, check CompletionOutcome) (callStep, error) {
	c := &r.c
	if err := c.stopTask(r.stopCtx, r.id); err != nil {
		return nextCall, err
	}
	status := state.RunCompleted
	if check.Action != "publish" {
		status = state.RunFailed
	}
	err := r.publish(RefPublication{
		Publication: Publication{Step: r.step(), Status: status, Detail: check.Detail, StopConfirmed: true, Spent: c.Spent, CostUnknown: c.CostUnknown},
		Ref:         c.Ref,
	})
	retry, err := r.boundary(err)
	if !retry {
		return taskDone, err
	}
	return endCalls, r.appendResult(call, map[string]any{
		"accepted": false,
		"reason":   prompts.Runtime("workflow-accepted-instructions-changed-before-publication-reconc", nil),
	})
}

func (r *taskRun) runTool(call CallRef) (callStep, error) {
	var a *Activities
	c := &r.c
	c.Tools++
	operation := fmt.Sprintf("%s-m%d-t%d", r.id, c.Models, c.Tools)
	err := workflow.ExecuteActivity(r.quick, a.Project, Projection{Step: r.step(), Kind: "tool", Text: call.Name}).Get(r.ctx, nil)
	retry, err := r.boundary(err)
	if retry {
		return reviseCalls, r.appendResult(call, map[string]any{"skipped": true, "reason": prompts.Runtime("workflow-execution-admission-changed-before-this-call-ran", nil)})
	}
	if err != nil {
		return nextCall, err
	}
	// A tool the worker runs itself never touches the computer, so it leaves nothing to stop.
	policy := agent.PolicyFor(call.Name, call.Exec)
	if !policy.Runtime {
		if len(c.ComputerRevisions) == 0 || c.ComputerRevisions[len(c.ComputerRevisions)-1] != c.Revision {
			c.ComputerRevisions = append(c.ComputerRevisions, c.Revision)
		}
		c.Stopped = false
	}
	c.JobsStarted = c.JobsStarted || call.Name == "job_start"
	var output RefToolOutcome
	err = workflow.ExecuteActivity(r.recovering, a.ToolRef, RefToolInput{
		Step: r.step(), Ref: c.Ref, OperationID: operation, Index: call.Index, ReadOnly: c.Inherited, Exec: call.Exec,
	}).Get(r.ctx, &output)
	retry, err = r.boundary(err)
	if retry {
		return reviseCalls, r.appendResult(call, map[string]any{"outcome": "uncertain", "operation_id": operation, "replayed": false})
	}
	if err != nil {
		// Without a settled receipt, a paid operation may still have incurred cost.
		var application *temporal.ApplicationError
		if !errors.As(err, &application) || !application.NonRetryable() {
			c.CostUnknown = c.CostUnknown || policy.InterruptedCostUnknown
			if policy.InterruptedCostUSD > 0 {
				c.Spent += policy.InterruptedCostUSD
			}
		}
		return nextCall, err
	}
	c.Ref = output.Ref
	c.trackJobs(output)
	if output.Cost > 0 || output.CostUnknown {
		c.Spent += output.Cost
		c.CostUnknown = c.CostUnknown || output.CostUnknown
		r.spend()
	}
	if c.afterTool(output, r.id) {
		return reviseCalls, nil
	}
	return nextCall, nil
}

func (c *Checkpoint) projectSpend(w workflow.Context, id string) {
	var a *Activities

	if e := workflow.ExecuteActivity(w, a.Spend, Spending{RunID: id, Spent: c.Spent, Budget: c.Budget, CostUnknown: c.CostUnknown}).Get(w, nil); e != nil {
		workflow.GetLogger(w).Warn("Spend projection pending; workflow retains accounting", "run_id", id)
	}
}

func (c *Checkpoint) stopComputer(w workflow.Context, id string) error {
	var a *Activities

	if c.Stopped {
		return nil
	}
	scopes := make([]string, 0, len(c.ComputerRevisions))
	for _, revision := range c.ComputerRevisions {
		scopes = append(scopes, computerRun(Step{id, revision}))
	}
	if e := workflow.ExecuteActivity(w, a.Stop, scopes).Get(w, nil); e != nil {
		return e
	}
	c.Stopped = true
	return nil
}

func (c *Checkpoint) afterTool(output RefToolOutcome, id string) bool {
	switch {
	case output.Outcome == "needs_input":
		c.Question = &state.Question{
			ID:       fmt.Sprintf("%s-correction-q%d", id, c.Tools),
			Kind:     "input",
			Text:     prompts.Runtime("read-only-question", nil),
			Revision: c.Revision,
		}
	case output.Outcome == "needs_interaction" && output.InteractionID != "":
		// Only registry tools produce this outcome, so histories recorded before it existed never reach here.
		c.Question = &state.Question{ID: output.InteractionID, Kind: "input", Text: output.Prompt, Revision: c.Revision}
	case output.Outcome == "uncertain" && output.EffectClass != "read":
		text := prompts.Runtime("uncertain-action-question", nil)
		if output.Unresolved != "" {
			text = output.Unresolved + prompts.Runtime("uncertain-action-followup", nil)
		}
		c.Question = &state.Question{
			ID:       fmt.Sprintf("%s-operation-q%d", id, c.Tools),
			Kind:     "input",
			Text:     text,
			Revision: c.Revision,
			Clears:   output.Clears,
		}
	default:
		return false
	}
	c.QuestionPublished = false
	return true
}

// applySync updates only checkpoint state; the workflow retains activity order.
func (c *Checkpoint) applySync(synced HistorySync) {
	c.Ref, c.Revision, c.Position, c.Request = synced.Ref, synced.Revision, synced.Position, synced.Request
	if synced.QuestionResolved {
		c.Question = nil
		c.QuestionPublished = false
	}
	c.Budget = synced.Budget
	c.Others = synced.Others
	c.CostUnknown = c.CostUnknown || synced.CostUnknown
	c.Subtask, c.Inherited = synced.Subtask, synced.ReadOnly
}

// callModel retains and accounts for the response before any newer-input check.
func (c *Checkpoint) callModel(ctx, quick, spending workflow.Context, id string) (RefModelOutcome, error) {
	var a *Activities
	var model RefModelOutcome
	err := workflow.ExecuteActivity(quick, a.Project, Projection{Step: Step{id, c.Revision}, Kind: "model", Text: "Thinking"}).Get(ctx, nil)
	if err != nil {
		return model, err
	}
	err = workflow.ExecuteActivity(ctx, a.ModelRef, RefModelInput{Step: Step{id, c.Revision}, Ref: c.Ref, Subtask: c.Subtask}).Get(ctx, &model)
	if err != nil {
		var application *temporal.ApplicationError
		if !errors.As(err, &application) || !application.NonRetryable() {
			c.CostUnknown = true
		}
		return model, err
	}
	c.Models++
	c.Spent += model.Cost
	c.CostUnknown = c.CostUnknown || (!model.CostKnown && !model.NotSent)
	c.Ref = model.Ref
	c.projectSpend(spending, id)
	if model.Failure != "" {
		err = errors.New(model.Failure)
		return model, err
	}
	return model, nil
}

func (c *Checkpoint) publishFailure(ctx, stopCtx workflow.Context, id string, failure error, wait func() error, result state.Run) (state.Run, error, error) {
	var a *Activities
	cleanup, _ := workflow.NewDisconnectedContext(ctx)
	cleanup = workflow.WithActivityOptions(cleanup, workflow.GetActivityOptions(stopCtx))
	if e := c.stopTask(cleanup, id); e != nil {
		failure = fmt.Errorf("computer stop could not be confirmed: %w", e)
	}
	status := state.RunFailed
	if c.Stopped && (temporal.IsCanceledError(failure) || applicationKind(failure, activityCancelled)) {
		status = state.RunCancelled
	}
	p := RefPublication{Publication: Publication{
		Step: Step{id, c.Revision}, Status: status, Detail: failure.Error(), StopConfirmed: c.Stopped,
		CostUnknown: c.CostUnknown, Spent: c.Spent, BypassRouting: !c.Stopped || status == state.RunCancelled,
	}}
	var publishErr error
	for {
		publishErr = workflow.ExecuteActivity(cleanup, a.PublishRef, p).Get(cleanup, &result)
		if !applicationKind(publishErr, activityRoutingPending) {
			break
		}
		if e := wait(); e != nil {
			p.Status = state.RunCancelled
			p.Detail = e.Error()
			p.BypassRouting = true
		}
	}
	return result, failure, publishErr
}
