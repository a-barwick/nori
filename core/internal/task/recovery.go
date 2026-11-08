package task

import (
	"context"
	"errors"

	"nori/internal/agent"
	"nori/internal/computer"
	"nori/internal/dispatch"
	"nori/internal/prompts"
	"nori/internal/state"
)

type Recovery struct {
	Store    state.TaskStore
	Engine   dispatch.Orchestrator
	Computer computer.Provider
}

var _ dispatch.Recovery = Recovery{}

func (r Recovery) RepairClosed(ctx context.Context) error {
	return RepairClosed(ctx, r.Store, r.Engine, r.Computer)
}

func (r Recovery) StopPaused(ctx context.Context, runID string) (state.Run, error) {
	return StopPaused(ctx, r.Store, r.Computer, runID)
}

// RepairClosed handles the projection crash window only after Temporal proves
// execution is closed (or cancellation was requested before it started). It never
// retries execution. Known computer scopes must report their real stop outcome.
func RepairClosed(ctx context.Context, store state.TaskStore, engine dispatch.Orchestrator, config computer.Provider) error {
	runs, e := store.OpenTasks(ctx)
	if e != nil {
		return e
	}
	pendingCleanup, e := store.CleanupTasks(ctx)
	if e != nil {
		return e
	}
	seen := map[string]bool{}
	for _, run := range runs {
		seen[run.ID] = true
	}
	for _, run := range pendingCleanup {
		if !seen[run.ID] {
			runs = append(runs, run)
		}
	}
	var failures []error
	for _, run := range runs {
		if run.Status != state.RunRunning {
			if e := repairCleanup(ctx, store, config, run); e != nil {
				failures = append(failures, e)
			}
			continue
		}
		execution, e := engine.Inspect(ctx, run.ID)
		missing := errors.Is(e, dispatch.ErrNoExecution)
		neverStarted := missing && run.CancelRequested
		finishing := missing && run.Finishing != nil
		if e != nil && !neverStarted && !finishing {
			if !missing {
				failures = append(failures, e)
			}
			continue
		}
		if !neverStarted && !finishing && !execution.Closed {
			continue
		}
		if run.Finishing != nil {
			if e := repairCleanup(ctx, store, config, run); e != nil {
				failures = append(failures, e)
			}
			continue
		}
		status, detail := state.RunFailed, prompts.Runtime("recovery-interrupted", nil)
		if run.CancelRequested {
			status, detail = state.RunCancelled, prompts.Runtime("cancelled", nil)
		}
		detail += attemptedWrites(ctx, store, run.ID)
		if run, e = store.Publish(ctx, state.PublishRequest{
			RunID: run.ID, Revision: run.Revision, Status: status, Detail: detail, CostUnknown: run.CostUnknown, BypassRouting: true,
		}); e != nil {
			failures = append(failures, e)
		} else {
			if e = repairCleanup(ctx, store, config, run); e != nil {
				failures = append(failures, e)
			}
		}
	}
	return errors.Join(failures...)
}

func attemptedWrites(ctx context.Context, store state.TaskStore, id string) string {
	ops, err := store.Operations(ctx, id)
	if err != nil {
		return prompts.Runtime("recovery-writes-unreadable", nil)
	}
	text := ""
	for _, op := range ops {
		if op.Outcome == "attempted" {
			text += prompts.Runtime("recovery-write-unchecked", nil) + op.Name + "."
		}
	}
	return text
}

func repairCleanup(ctx context.Context, store state.TaskStore, config computer.Provider, run state.Run) (err error) {
	defer func() {
		if err != nil {
			receipt, cancel := receiptContext(ctx)
			defer cancel()
			err = errors.Join(err, store.FinishCleanup(receipt, run.ID, prompts.Runtime("task-cleanup-unconfirmed", nil)))
		}
	}()
	scopes, err := operationScopes(ctx, store, run.ID)
	if err != nil {
		return err
	}
	if err = errors.Join(StopComputer(ctx, config, scopes), stopRecoveredJobs(ctx, store, config, run.ID, true)); err != nil {
		return err
	}
	return closeTaskBrowser(ctx, store, config, run.ID, scopes)
}

func operationScopes(ctx context.Context, store state.TaskStore, runID string) ([]string, error) {
	ops, err := store.Operations(ctx, runID)
	if err != nil {
		return nil, err
	}
	var revisions []int64
	for _, op := range ops {
		if op.Name == "request_work" || agent.TaskToolPolicy(op.Name).Runtime {
			continue
		}
		revisions = append(revisions, op.Revision)
	}
	scopes := make([]string, 0, len(revisions))
	for _, revision := range state.UniqueRevisions(revisions) {
		scopes = append(scopes, computerRun(Step{runID, revision}))
	}
	return scopes, nil
}

// stopRecoveredJobs ends jobs a closed workflow left running. A guest /cancel doesn't
// reach them, because a job belongs to its task. Tasks that never started a job skip
// this, so recovering an older task doesn't depend on the guest's job routes.
func stopRecoveredJobs(ctx context.Context, store state.TaskStore, config computer.Provider, id string, close bool) error {
	ops, err := store.Operations(ctx, id)
	if err != nil {
		return err
	}
	for _, op := range ops {
		if op.Name == "job_start" {
			return stopTaskJobs(ctx, config, id, close)
		}
	}
	return nil
}
func closeTaskBrowser(ctx context.Context, store state.TaskStore, config computer.Provider, id string, scopes []string) error {
	if len(scopes) == 0 {
		return store.FinishCleanup(ctx, id, "")
	}
	c, err := computer.Attach(ctx, config, scopes[0])
	if errors.Is(err, computer.ErrStopped) {
		return store.FinishCleanup(ctx, id, "")
	}
	if err != nil {
		return err
	}
	defer c.Close()
	if err = c.CloseTaskBrowser(ctx, id); err != nil {
		return err
	}
	return store.FinishCleanup(ctx, id, "")
}

// StopPaused completes an accepted Stop without waking paid workers or starting
// the retained computer. CancelRequested fences new work even if pause changes.
func StopPaused(ctx context.Context, store state.TaskStore, config computer.Provider, id string) (state.Run, error) {
	control, err := store.Control(ctx)
	if err != nil {
		return state.Run{}, err
	}
	if !control.Paused {
		return store.GetRun(ctx, id)
	}
	run, err := store.GetRun(ctx, id)
	if err != nil {
		return run, err
	}
	if run.Status != state.RunRunning {
		if run.CleanupPending {
			err = repairCleanup(ctx, store, config, run)
		}
		return run, err
	}
	if !run.CancelRequested && run.Finishing == nil {
		return run, nil
	}
	// Persist the finishing intent first. Cleanup uses the same path as a worker or crash repair,
	// including task-owned jobs. No paid execution or computer wake is needed.
	run, err = store.Publish(ctx, state.PublishRequest{
		RunID: id, Revision: run.Revision, Status: state.RunCancelled, Detail: prompts.Runtime("stopped", nil) + attemptedWrites(ctx, store, id), CostUnknown: run.CostUnknown, BypassRouting: true,
	})
	if err != nil {
		return run, err
	}
	var failures []error
	if err = repairCleanup(ctx, store, config, run); err != nil {
		failures = append(failures, err)
	}
	for _, child := range run.Subtasks {
		if _, err = StopPaused(ctx, store, config, child.ID); err != nil {
			failures = append(failures, err)
		}
	}
	run, err = store.GetRun(ctx, id)
	return run, errors.Join(append(failures, err)...)
}

// Cleanup completes the same durable cleanup plan used after a crash and during a paused Stop.
func (a *Activities) Cleanup(ctx context.Context, id string) error {
	run, err := a.Store.GetRun(ctx, id)
	if err == nil {
		err = repairCleanup(ctx, a.Store, a.Agent.Computer, run)
	}
	a.notify(ctx)
	return classified(err)
}
