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
}

func Workflow(ctx workflow.Context, id string, resume Checkpoint) (result state.Run, err error) {
	options := workflow.ActivityOptions{StartToCloseTimeout: 240 * time.Second, HeartbeatTimeout: 10 * time.Second, WaitForCancellation: true, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}}
	ctx = workflow.WithActivityOptions(ctx, options)
	quick := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	stopCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 200 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	spending := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 5 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3}})
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
		if e := stop(cleanup); e != nil {
			err = fmt.Errorf("computer stop could not be confirmed: %w", e)
		}
		if c.Stopped && result.Status != "" && len(c.ComputerRevisions) > 0 {
			if e := workflow.ExecuteActivity(cleanup, a.CloseBrowser, BrowserCleanup{RunID: id, Scope: computerRun(Step{id, c.ComputerRevisions[0]})}).Get(cleanup, nil); e != nil {
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
			err = workflow.ExecuteActivity(quick, a.SyncHistory, HistoryInput{Step: Step{id, c.Revision}, Ref: c.Ref, Position: c.Position, Question: c.Question}).Get(ctx, &synced)
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
			if c.Question == nil && c.Spent >= c.Budget {
				c.Question = &state.Question{ID: fmt.Sprintf("%s-budget-p%d", id, c.Position), Kind: "budget", Text: fmt.Sprintf("I reached this task’s $%.2f model allowance ($%.4f spent across %d model calls and %d tool attempts). The task is not finished. Request: %s\n\nReply continue to grant another $%.2f, or Stop to end the task.", c.Budget, c.Spent, c.Models, c.Tools, c.Request, agent.BudgetUSD), Revision: c.Revision}
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
			err = workflow.ExecuteActivity(quick, a.Project, Projection{Step: Step{id, c.Revision}, Kind: "model", Text: "Thinking"}).Get(ctx, nil)
			if boundary(err) {
				continue
			}
			if err != nil {
				return
			}
			var model RefModelOutcome
			err = workflow.ExecuteActivity(ctx, a.ModelRef, RefModelInput{Step: Step{id, c.Revision}, Ref: c.Ref}).Get(ctx, &model)
			if err != nil {
				var application *temporal.ApplicationError
				if !errors.As(err, &application) {
					c.CostUnknown = true
				}
				if boundary(err) {
					continue
				}
				return
			}
			c.Models++
			c.Spent += model.Cost
			c.CostUnknown = c.CostUnknown || !model.CostKnown
			c.Ref = model.Ref
			spend(spending)
			if c.CostUnknown {
				err = state.ErrCostUnknown
				return
			}
			if model.Failure != "" {
				err = errors.New(model.Failure)
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
						e := workflow.ExecuteActivity(quick, a.AppendToolResult, AppendResult{Step: Step{id, c.Revision}, Ref: c.Ref, CallID: call.ID, Name: call.Name, Response: map[string]any{"skipped": true, "reason": "Execution admission changed before this call ran."}}).Get(ctx, &ref)
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
					err = workflow.ExecuteActivity(quick, a.AppendToolResult, AppendResult{Step: Step{id, c.Revision}, Ref: c.Ref, CallID: call.ID, Name: call.Name, Response: map[string]any{"skipped": true, "reason": "Accepted input changed before this call ran."}}).Get(ctx, &ref)
					if err != nil {
						return
					}
					c.Ref = ref
					continue
				}
				if call.Name == "request_input" {
					var q state.Question
					err = workflow.ExecuteActivity(quick, a.InputRef, RefInput{Step: Step{id, c.Revision}, Ref: c.Ref, Index: call.Index}).Get(ctx, &q)
					if err != nil {
						return
					}
					q.ID = fmt.Sprintf("%s-question-m%d", id, c.Models)
					c.Question = &q
					c.QuestionPublished = false
					break
				}
				if call.Name == "complete" {
					c.Checks++
					var check CompletionOutcome
					err = workflow.ExecuteActivity(quick, a.CheckCompletion, CompletionInput{Step: Step{id, c.Revision}, Ref: c.Ref, ID: fmt.Sprintf("%s-check-%d", id, c.Checks), Position: c.Position, CorrectionAttempt: c.Corrections}).Get(ctx, &check)
					if boundary(err) {
						var ref string
						err = workflow.ExecuteActivity(quick, a.AppendToolResult, AppendResult{Step: Step{id, c.Revision}, Ref: c.Ref, CallID: call.ID, Name: call.Name, Response: map[string]any{"accepted": false, "reason": "Execution admission changed during completion checking. Reconcile before proposing again."}}).Get(ctx, &ref)
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
					case "publish", "unverified", "partial":
						if err = stop(stopCtx); err != nil {
							return
						}
						status := state.RunCompleted
						if check.Action != "publish" {
							status = "failed"
						}
						err = publish(RefPublication{Publication: Publication{Step: Step{id, c.Revision}, Status: status, Detail: check.Detail, StopConfirmed: true}, Ref: c.Ref, Unverified: check.Action == "unverified"})
						if boundary(err) {
							var ref string
							err = workflow.ExecuteActivity(quick, a.AppendToolResult, AppendResult{Step: Step{id, c.Revision}, Ref: c.Ref, CallID: call.ID, Name: call.Name, Response: map[string]any{"accepted": false, "reason": "Accepted instructions changed before publication. Reconcile them before proposing completion again."}}).Get(ctx, &ref)
							if err != nil {
								return
							}
							c.Ref = ref
							revised = true
							break
						}
						return
					case "ask":
						c.Question = &state.Question{ID: fmt.Sprintf("%s-completion-q%d", id, c.Checks), Kind: "input", Text: check.Detail, Revision: c.Revision}
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
				c.Tools++
				operation := fmt.Sprintf("%s-m%d-t%d", id, c.Models, c.Tools)
				err = workflow.ExecuteActivity(quick, a.Project, Projection{Step: Step{id, c.Revision}, Kind: "tool", Text: call.Name}).Get(ctx, nil)
				if boundary(err) {
					revised = true
					var ref string
					err = workflow.ExecuteActivity(quick, a.AppendToolResult, AppendResult{Step: Step{id, c.Revision}, Ref: c.Ref, CallID: call.ID, Name: call.Name, Response: map[string]any{"skipped": true, "reason": "Execution admission changed before this call ran."}}).Get(ctx, &ref)
					if err != nil {
						return
					}
					c.Ref = ref
					continue
				}
				if err != nil {
					return
				}
				if len(c.ComputerRevisions) == 0 || c.ComputerRevisions[len(c.ComputerRevisions)-1] != c.Revision {
					c.ComputerRevisions = append(c.ComputerRevisions, c.Revision)
				}
				c.Stopped = false
				var output RefToolOutcome
				err = workflow.ExecuteActivity(ctx, a.ToolRef, RefToolInput{Step: Step{id, c.Revision}, Ref: c.Ref, OperationID: operation, Index: call.Index, ReadOnly: c.ReadOnly}).Get(ctx, &output)
				if boundary(err) {
					revised = true
					var ref string
					err = workflow.ExecuteActivity(quick, a.AppendToolResult, AppendResult{Step: Step{id, c.Revision}, Ref: c.Ref, CallID: call.ID, Name: call.Name, Response: map[string]any{"outcome": "uncertain", "operation_id": operation, "replayed": false}}).Get(ctx, &ref)
					if err != nil {
						return
					}
					c.Ref = ref
					continue
				}
				if err != nil {
					return
				}
				c.Ref = output.Ref
				if output.Outcome == "needs_input" {
					revised = true
					c.Question = &state.Question{ID: fmt.Sprintf("%s-correction-q%d", id, c.Tools), Kind: "input", Text: "Completing this task appears to require another action beyond the permitted read-only correction. Please clarify or authorize what should happen next.", Revision: c.Revision}
					c.QuestionPublished = false
				}
				if output.Outcome == "uncertain" && output.EffectClass != "read" {
					revised = true
					c.Question = &state.Question{ID: fmt.Sprintf("%s-operation-q%d", id, c.Tools), Kind: "input", Text: "An attempted action has an uncertain outcome. Nori will not repeat it automatically. Please confirm what happened or clarify how to proceed.", Revision: c.Revision}
					c.QuestionPublished = false
				}
			}
		}
	}
	for {
		runLoop()
		if continuing || err == nil {
			return
		}
		failure := err
		cleanup, _ := workflow.NewDisconnectedContext(ctx)
		cleanup = workflow.WithActivityOptions(cleanup, workflow.GetActivityOptions(stopCtx))
		if e := stop(cleanup); e != nil {
			failure = fmt.Errorf("computer stop could not be confirmed: %w", e)
		}
		status := state.RunFailed
		if c.Stopped && (temporal.IsCanceledError(failure) || applicationKind(failure, "cancelled")) {
			status = "cancelled"
		}
		p := RefPublication{Publication: Publication{Step: Step{id, c.Revision}, Status: status, Detail: failure.Error(), StopConfirmed: c.Stopped, CostUnknown: c.CostUnknown, BypassRouting: c.CostUnknown || !c.Stopped || status == "cancelled"}}
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
	if synced.Grants > 0 {
		c.Budget += float64(synced.Grants) * agent.BudgetUSD
	}
}
