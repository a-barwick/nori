package task

import (
	"context"
	"errors"

	"nori/internal/computer"
	"nori/internal/dispatch"
	"nori/internal/prompts"
	"nori/internal/state"
)

// Recovery is the dispatcher's repair of closed and paused tasks. It needs the
// engine to prove an execution closed and the computer to stop and clean up.
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
		if e != nil && !neverStarted {
			if !missing {
				failures = append(failures, e)
			}
			continue
		}
		if !neverStarted && !execution.Closed {
			continue
		}
		revisions, e := store.OperationRevisions(ctx, run.ID)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		scopes := []string{}
		for _, revision := range revisions {
			scopes = append(scopes, computerRun(Step{run.ID, revision}))
		}
		stopErr := errors.Join(StopComputer(ctx, config, scopes), stopRecoveredJobs(ctx, store, config, run.ID))
		status, detail := state.RunFailed, prompts.Runtime("recovery-interrupted", nil)
		if neverStarted {
			detail = prompts.Runtime("recovery-cancelled", nil)
		}
		if run.CancelRequested && stopErr == nil {
			status = "cancelled"
			detail = prompts.Runtime("cancelled", nil)
		}
		if stopErr != nil {
			status = "failed"
			detail = prompts.Runtime("recovery-computer-unconfirmed", nil)
			failures = append(failures, stopErr)
		}
		detail += attemptedWrites(ctx, store, run.ID)
		if _, e = store.Publish(ctx, state.PublishRequest{
			RunID: run.ID, Revision: run.Revision, Status: status, Detail: detail, CostUnknown: run.CostUnknown, BypassRouting: true,
		}); e != nil {
			failures = append(failures, e)
		} else if stopErr == nil && run.CleanupPending {
			if e = closeTaskBrowser(ctx, store, config, run.ID, scopes); e != nil {
				failures = append(failures, e)
			}
		}
	}
	return errors.Join(failures...)
}

// attemptedWrites says which writes may have happened when a task ends without checking them.
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

func repairCleanup(ctx context.Context, store state.TaskStore, config computer.Provider, run state.Run) error {
	revisions, err := store.OperationRevisions(ctx, run.ID)
	if err != nil {
		return err
	}
	scopes := []string{}
	for _, revision := range revisions {
		scopes = append(scopes, computerRun(Step{run.ID, revision}))
	}
	if err = errors.Join(StopComputer(ctx, config, scopes), stopRecoveredJobs(ctx, store, config, run.ID)); err != nil {
		return err
	}
	return closeTaskBrowser(ctx, store, config, run.ID, scopes)
}

// stopRecoveredJobs ends jobs a closed workflow left running. A guest /cancel doesn't
// reach them, because a job belongs to its task. Tasks that never started a job skip
// this, so recovering an older task doesn't depend on the guest's job routes.
func stopRecoveredJobs(ctx context.Context, store state.TaskStore, config computer.Provider, id string) error {
	ops, err := store.Operations(ctx, id)
	if err != nil {
		return err
	}
	for _, op := range ops {
		if op.Name == "job_start" {
			return stopTaskJobs(ctx, config, id)
		}
	}
	return nil
}
func closeTaskBrowser(ctx context.Context, store state.TaskStore, config computer.Provider, id string, scopes []string) error {
	if len(scopes) == 0 {
		return store.FinishCleanup(ctx, id)
	}
	c, err := computer.Attach(ctx, config, scopes[0])
	if errors.Is(err, computer.ErrStopped) {
		return store.FinishCleanup(ctx, id)
	}
	if err != nil {
		return err
	}
	defer c.Close()
	if err = c.CloseTaskBrowser(ctx, id); err != nil {
		return err
	}
	return store.FinishCleanup(ctx, id)
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
	if err != nil || !run.CancelRequested || run.Status != state.RunRunning {
		return run, err
	}
	revisions, err := store.OperationRevisions(ctx, id)
	if err != nil {
		return run, err
	}
	scopes := []string{}
	for _, revision := range revisions {
		scopes = append(scopes, computerRun(Step{id, revision}))
	}
	if err = StopComputer(ctx, config, scopes); err != nil {
		return run, err
	}
	run, err = store.GetRun(ctx, id)
	if err != nil {
		return run, err
	}
	run, err = store.Publish(ctx, state.PublishRequest{
		RunID: id, Revision: run.Revision, Status: state.RunCancelled, Detail: prompts.Runtime("stopped", nil) + attemptedWrites(ctx, store, id), CostUnknown: run.CostUnknown, BypassRouting: true,
	})
	if err != nil {
		return run, err
	}
	if run.CleanupPending {
		err = closeTaskBrowser(ctx, store, config, id, scopes)
	}
	return run, err
}
