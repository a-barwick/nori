package task

import (
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"nori/internal/agent"
	"nori/internal/state"
)

const WorkflowName = "nori-task-v3"
const Signal = "task-command"

func superseded(err error) bool { return applicationKind(err, "superseded") }

func applicationKind(err error, kind string) bool {
	var application *temporal.ApplicationError
	return errors.As(err, &application) && application.Type() == kind
}

// Checkpoint is deliberately bounded: exact model state lives in the immutable
// history chain, while accounting and user-control state survive ContinueAsNew.
type Checkpoint struct {
	Ref                                string
	Revision, Position                 int64
	Spent, Budget                      float64
	CostUnknown                        bool
	Models, Tools, Checks, Corrections int
	Question                           *state.Question
	QuestionPublished                  bool
	ComputerRevisions                  []int64
	Stopped                            bool
	Request                            string
	ReadOnly                           bool
	// Jobs are started and not yet observed ended. They outlive a revision, so
	// only terminal cleanup stops them, not a correction or a question.
	Jobs        []string
	JobsStarted bool
	// Action is the write waiting for a decision or, once approved, to run.
	Action *PendingAction
	// Others is what the rest of this task's family has spent, read from the store before each budget check. A
	// subtask inherits a read-only limit from its parent, and that limit outlasts a correction.
	Others             float64
	Subtask, Inherited bool
}

func Workflow(ctx workflow.Context, id string, resume Checkpoint) (result state.Run, err error) {
	options := workflow.ActivityOptions{
		StartToCloseTimeout: 240 * time.Second, HeartbeatTimeout: 10 * time.Second, WaitForCancellation: true,
		RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1},
	}
	ctx = workflow.WithActivityOptions(ctx, options)
	quick := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 20 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	// A sync only appends to the history and saves evidence, so a worker crash during delivery can safely repeat it.
	syncing := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 20 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3},
	})
	stopCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 200 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	spending := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3},
	})
	c := resume
	if c.Budget == 0 {
		c.Budget = agent.BudgetUSD
		c.Stopped = true
	}
	var a *Activities
	signals := workflow.GetSignalChannel(ctx, Signal)
	spend := func(w workflow.Context) { c.projectSpend(w, id) }
	wait := func() error { var command state.Command; signals.Receive(ctx, &command); return ctx.Err() }
	stop := func(w workflow.Context) error { return c.stopComputer(w, id) }
	finish := func(w workflow.Context) error { return c.stopTask(w, id) }
	boundary := func(e error) bool {
		if superseded(e) {
			return true
		}
		if applicationKind(e, "paused") {
			err = wait()
			return err == nil
		}
		return false
	}
	continuing := false
	defer func() {
		if continuing {
			return
		}
		cleanup, _ := workflow.NewDisconnectedContext(ctx)
		cleanup = workflow.WithActivityOptions(cleanup, workflow.GetActivityOptions(stopCtx))
		spend(workflow.WithActivityOptions(cleanup, workflow.GetActivityOptions(spending)))
		if e := finish(cleanup); e != nil {
			err = fmt.Errorf("computer stop could not be confirmed: %w", e)
		}
		if c.Stopped && result.Status != "" && len(c.ComputerRevisions) > 0 {
			browser := BrowserCleanup{RunID: id, Scope: computerRun(Step{id, c.ComputerRevisions[0]})}
			if e := workflow.ExecuteActivity(cleanup, a.CloseBrowser, browser).Get(cleanup, nil); e != nil {
				workflow.GetLogger(ctx).Error("Task browser cleanup remains pending", "run_id", id)
			}
		}
	}()
	publish := func(p RefPublication) error {
		for {
			e := workflow.ExecuteActivity(quick, a.PublishRef, p).Get(ctx, &result)
			if !applicationKind(e, "routing_pending") {
				return e
			}
			if e = wait(); e != nil {
				return e
			}
		}
	}
	runLoop := func() {
		for turns := 0; ; turns++ {
			if turns >= 32 && !c.CostUnknown {
				continuing = true
				err = workflow.NewContinueAsNewError(ctx, WorkflowName, id, c)
				return
			}
			var signal state.Command
			for signals.ReceiveAsync(&signal) {
			}
			var synced HistorySync
			err = workflow.ExecuteActivity(syncing, a.SyncHistory, HistoryInput{
				Step: Step{id, c.Revision}, Ref: c.Ref, Position: c.Position, Question: c.Question, Action: c.Action,
			}).Get(ctx, &synced)
			if boundary(err) {
				continue
			}
			if err != nil {
				return
			}
			c.applySync(synced)
			if synced.Grants > 0 {
				spend(spending)
			}
			if c.CostUnknown {
				err = state.ErrCostUnknown
				return
			}
			// A decided write runs before the budget check, because it spends nothing.
			err = c.settleAction(ctx, quick, id, synced)
			if boundary(err) {
				continue
			}
			if err != nil {
				return
			}
			if c.Question == nil && c.Spent+c.Others >= c.Budget {
				c.Question = &state.Question{
					ID:   fmt.Sprintf("%s-budget-p%d", id, c.Position),
					Kind: "budget",
					Text: fmt.Sprintf("I reached this task’s $%.2f model allowance ($%.4f spent across %d model calls and %d tool attempts, plus $%.4f by its related tasks). "+
						"The task is not finished. Request: %s\n\nReply continue to grant another $%.2f, or Stop to end the task.",
						c.Budget, c.Spent, c.Models, c.Tools, c.Others, c.Request, agent.BudgetUSD),
					Revision: c.Revision,
				}
				c.QuestionPublished = false
			}
			if c.Question != nil {
				if err = stop(stopCtx); err != nil {
					return
				}
				if !c.QuestionPublished {
					err = workflow.ExecuteActivity(quick, a.Ask, QuestionProjection{Step: Step{id, c.Revision}, Question: *c.Question}).Get(ctx, nil)
					if boundary(err) {
						continue
					}
					if err != nil {
						return
					}
					c.QuestionPublished = true
				}
				if err = wait(); err != nil {
					return
				}
				continue
			}
			var model RefModelOutcome
			model, err = c.callModel(ctx, quick, spending, id)
			if boundary(err) {
				continue
			}
			if err != nil {
				return
			}
			if model.Invalid {
				continue
			}
			// Account and retain every model response before checking newer instructions.
			// Close each abandoned call explicitly to preserve the exact Responses prefix.
			var current CurrentPosition
			err = workflow.ExecuteActivity(quick, a.CurrentPosition, id).Get(ctx, &current)
			if err != nil {
				if boundary(err) {
					for _, call := range model.Calls {
						var ref string
						e := workflow.ExecuteActivity(quick, a.AppendToolResult, AppendResult{
							Step: Step{id, c.Revision}, Ref: c.Ref, CallID: call.ID, Name: call.Name,
							Response: map[string]any{"skipped": true, "reason": "Execution admission changed before this call ran."},
						}).Get(ctx, &ref)
						if e != nil {
							err = e
							return
						}
						c.Ref = ref
					}
					continue
				}
				return
			}
			changed := current.Revision != c.Revision || current.Position != c.Position
			revised := false
			for _, call := range model.Calls {
				if changed || revised {
					var ref string
					err = workflow.ExecuteActivity(quick, a.AppendToolResult, AppendResult{
						Step: Step{id, c.Revision}, Ref: c.Ref, CallID: call.ID, Name: call.Name,
						Response: map[string]any{"skipped": true, "reason": "Accepted input changed before this call ran."},
					}).Get(ctx, &ref)
					if err != nil {
						return
					}
					c.Ref = ref
					continue
				}
				if call.Name == "request_input" || call.Name == "request_work" {
					var q state.Question
					err = workflow.ExecuteActivity(quick, a.InputRef, RefInput{Step: Step{id, c.Revision}, Ref: c.Ref, Index: call.Index, ReadOnly: c.ReadOnly}).Get(ctx, &q)
					if err != nil {
						return
					}
					q.ID = fmt.Sprintf("%s-question-m%d", id, c.Models)
					if call.Name == "request_work" {
						q.ID = fmt.Sprintf("%s-work-m%d", id, c.Models)
					}
					c.Question = &q
					c.QuestionPublished = false
					break
				}
				if call.Name == "complete" && len(c.Jobs) > 0 {
					var ref string
					err = workflow.ExecuteActivity(quick, a.AppendToolResult, AppendResult{Step: Step{id, c.Revision}, Ref: c.Ref, CallID: call.ID, Name: call.Name, Response: map[string]any{"accepted": false, "reason": "A background job is still running. Call job_wait or job_stop before proposing completion.", "running_jobs": c.Jobs}}).Get(ctx, &ref)
					if err != nil {
						return
					}
					c.Ref = ref
					break
				}
				if call.Name == "complete" {
					c.Checks++
					var check CompletionOutcome
					err = workflow.ExecuteActivity(quick, a.CheckCompletion, CompletionInput{
						Step: Step{id, c.Revision}, Ref: c.Ref, ID: fmt.Sprintf("%s-check-%d", id, c.Checks),
						Position: c.Position, CorrectionAttempt: c.Corrections,
					}).Get(ctx, &check)
					if boundary(err) {
						var ref string
						err = workflow.ExecuteActivity(quick, a.AppendToolResult, AppendResult{
							Step: Step{id, c.Revision}, Ref: c.Ref, CallID: call.ID, Name: call.Name,
							Response: map[string]any{
								"accepted": false,
								"reason":   "Execution admission changed during completion checking. Reconcile before proposing again.",
							},
						}).Get(ctx, &ref)
						if err != nil {
							return
						}
						c.Ref = ref
						revised = true
						continue
					}
					if err != nil {
						return
					}
					c.Ref = check.Ref
					switch check.Action {
					// New checks never return unverified; retained histories still replay it.
					case "publish", "unverified", "partial":
						if err = finish(stopCtx); err != nil {
							return
						}
						status := state.RunCompleted
						if check.Action != "publish" {
							status = "failed"
						}
						err = publish(RefPublication{
							Publication: Publication{Step: Step{id, c.Revision}, Status: status, Detail: check.Detail, StopConfirmed: true, Spent: c.Spent},
							Ref:         c.Ref, Unverified: check.Action == "unverified",
						})
						if boundary(err) {
							var ref string
							err = workflow.ExecuteActivity(quick, a.AppendToolResult, AppendResult{
								Step: Step{id, c.Revision}, Ref: c.Ref, CallID: call.ID, Name: call.Name,
								Response: map[string]any{
									"accepted": false,
									"reason":   "Accepted instructions changed before publication. Reconcile them before proposing completion again.",
								},
							}).Get(ctx, &ref)
							if err != nil {
								return
							}
							c.Ref = ref
							revised = true
							break
						}
						return
					case "ask":
						c.Question = &state.Question{
							ID: fmt.Sprintf("%s-completion-q%d", id, c.Checks), Kind: "input", Text: check.Detail, Revision: c.Revision,
						}
						c.QuestionPublished = false
					case "correct":
						c.Corrections++
						c.ReadOnly = true
					case "reconcile":
						revised = true
					default:
						err = errors.New("completion check returned an invalid decision")
						return
					}
					break
				}
				if call.Name == "job_wait" {
					var ended bool
					ended, err = c.awaitJob(ctx, wait, id, call)
					if err != nil {
						return
					}
					if !ended {
						var ref string
						err = workflow.ExecuteActivity(quick, a.AppendToolResult, AppendResult{Step: Step{id, c.Revision}, Ref: c.Ref, CallID: call.ID, Name: call.Name, Response: map[string]any{"waiting": false, "state": "running", "reason": "Austin's instructions changed while waiting. The job is still running; job_status, job_wait or job_stop can act on it."}}).Get(ctx, &ref)
						if err != nil {
							return
						}
						c.Ref = ref
						revised = true
						continue
					}
				}
				c.Tools++
				operation := fmt.Sprintf("%s-m%d-t%d", id, c.Models, c.Tools)
				err = workflow.ExecuteActivity(quick, a.Project, Projection{Step: Step{id, c.Revision}, Kind: "tool", Text: call.Name}).Get(ctx, nil)
				if boundary(err) {
					revised = true
					var ref string
					err = workflow.ExecuteActivity(quick, a.AppendToolResult, AppendResult{
						Step: Step{id, c.Revision}, Ref: c.Ref, CallID: call.ID, Name: call.Name,
						Response: map[string]any{"skipped": true, "reason": "Execution admission changed before this call ran."},
					}).Get(ctx, &ref)
					if err != nil {
						return
					}
					c.Ref = ref
					continue
				}
				if err != nil {
					return
				}
				if agent.WriteEffect(call.Name) != "" {
					var prepared ActionPrepared
					err = workflow.ExecuteActivity(ctx, a.PrepareAction, RefToolInput{
						Step: Step{id, c.Revision}, Ref: c.Ref, OperationID: operation, Index: call.Index, ReadOnly: c.ReadOnly || c.Inherited,
					}).Get(ctx, &prepared)
					if boundary(err) {
						revised = true
						var ref string
						err = workflow.ExecuteActivity(quick, a.AppendToolResult, AppendResult{
							Step: Step{id, c.Revision}, Ref: c.Ref, CallID: call.ID, Name: call.Name,
							Response: map[string]any{"skipped": true, "outcome": "failed", "operation_id": operation, "evidence_ref": operation, "reason": "Execution admission changed before this write was prepared. Nothing was sent."},
						}).Get(ctx, &ref)
						if err != nil {
							return
						}
						c.Ref = ref
						continue
					}
					if err != nil {
						return
					}
					c.Ref = prepared.Ref
					if prepared.Question != nil {
						c.Question, c.QuestionPublished = prepared.Question, false
						c.Action = &PendingAction{Operation: operation, CallID: call.ID, Name: call.Name}
					} else {
						c.afterTool(prepared.RefToolOutcome, id)
					}
					revised = true
					break
				}
				// A tool the worker runs itself never touches the computer, so it leaves nothing to stop.
				if !agent.RuntimeTool(call.Name) {
					if len(c.ComputerRevisions) == 0 || c.ComputerRevisions[len(c.ComputerRevisions)-1] != c.Revision {
						c.ComputerRevisions = append(c.ComputerRevisions, c.Revision)
					}
					c.Stopped = false
				}
				c.JobsStarted = c.JobsStarted || call.Name == "job_start"
				var output RefToolOutcome
				err = workflow.ExecuteActivity(ctx, a.ToolRef, RefToolInput{
					Step: Step{id, c.Revision}, Ref: c.Ref, OperationID: operation, Index: call.Index, ReadOnly: c.ReadOnly || c.Inherited,
				}).Get(ctx, &output)
				if boundary(err) {
					revised = true
					var ref string
					err = workflow.ExecuteActivity(quick, a.AppendToolResult, AppendResult{
						Step: Step{id, c.Revision}, Ref: c.Ref, CallID: call.ID, Name: call.Name,
						Response: map[string]any{"outcome": "uncertain", "operation_id": operation, "replayed": false},
					}).Get(ctx, &ref)
					if err != nil {
						return
					}
					c.Ref = ref
					continue
				}
				if err != nil {
					// A worker that died mid-call leaves a paid tool's cost open. Attempts are capped at 1.
					var application *temporal.ApplicationError
					if !errors.As(err, &application) {
						switch call.Name {
						case "screenshot_inspect":
							c.CostUnknown = true
						case "web_search":
							c.Spent += agent.SearchCostUSD
						}
					}
					return
				}
				c.Ref = output.Ref
				c.trackJobs(output)
				if output.Cost > 0 || output.CostUnknown {
					c.Spent += output.Cost
					c.CostUnknown = c.CostUnknown || output.CostUnknown
					spend(spending)
				}
				if c.afterTool(output, id) {
					revised = true
				}
			}
		}
	}
	for {
		runLoop()
		if continuing || err == nil {
			return
		}
		var failure, publishErr error
		result, failure, publishErr = c.publishFailure(ctx, stopCtx, id, err, wait, result)
		if superseded(publishErr) && !c.CostUnknown && ctx.Err() == nil {
			err = nil
			continue
		}
		if publishErr != nil {
			err = fmt.Errorf("%w; terminal persistence failed: %v", failure, publishErr)
		} else {
			err = failure
		}
		return
	}
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

// afterTool asks the user when a tool's result needs a decision, and reports whether it did.
func (c *Checkpoint) afterTool(output RefToolOutcome, id string) bool {
	switch {
	case output.Outcome == "needs_input":
		c.Question = &state.Question{
			ID:   fmt.Sprintf("%s-correction-q%d", id, c.Tools),
			Kind: "input",
			Text: "Completing this task appears to require another action beyond what it may do, because it can only read right now. " +
				"Please clarify or authorize what should happen next.",
			Revision: c.Revision,
		}
	case output.Outcome == "uncertain" && output.EffectClass != "read":
		c.Question = &state.Question{
			ID:   fmt.Sprintf("%s-operation-q%d", id, c.Tools),
			Kind: "input",
			Text: "An attempted action has an uncertain outcome. Nori will not repeat it automatically. " +
				"Please confirm what happened or clarify how to proceed.",
			Revision: c.Revision,
		}
	default:
		return false
	}
	c.QuestionPublished = false
	return true
}

// applySync updates only checkpoint state; the workflow retains activity order.
func (c *Checkpoint) applySync(synced HistorySync) {
	oldRevision := c.Revision
	c.Ref, c.Revision, c.Position, c.Request = synced.Ref, synced.Revision, synced.Position, synced.Request
	if synced.QuestionResolved {
		c.Question = nil
		c.QuestionPublished = false
	}
	if c.Revision != oldRevision {
		c.Corrections = 0
		c.ReadOnly = false
	}
	// Accepting a continue adds the grant to the store, so a subtask's grant reaches its parent.
	c.Budget = max(c.Budget, synced.Budget)
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
		if !errors.As(err, &application) {
			c.CostUnknown = true
		}
		return model, err
	}
	c.Models++
	c.Spent += model.Cost
	c.CostUnknown = c.CostUnknown || !model.CostKnown
	c.Ref = model.Ref
	c.projectSpend(spending, id)
	if c.CostUnknown {
		err = state.ErrCostUnknown
		return model, err
	}
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
	if c.Stopped && (temporal.IsCanceledError(failure) || applicationKind(failure, "cancelled")) {
		status = "cancelled"
	}
	p := RefPublication{Publication: Publication{
		Step: Step{id, c.Revision}, Status: status, Detail: failure.Error(), StopConfirmed: c.Stopped,
		CostUnknown: c.CostUnknown, Spent: c.Spent, BypassRouting: c.CostUnknown || !c.Stopped || status == "cancelled",
	}}
	var publishErr error
	for {
		publishErr = workflow.ExecuteActivity(cleanup, a.PublishRef, p).Get(cleanup, &result)
		if !applicationKind(publishErr, "routing_pending") {
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
