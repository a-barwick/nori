package task

import (
	"context"
	"errors"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"nori/internal/computer"
	"nori/internal/state"
)

// RepairClosed handles the projection crash window only after Temporal proves
// execution is closed (or cancellation was requested before it started). It never
// retries execution. Known computer scopes must report their real stop outcome.
func RepairClosed(ctx context.Context, store state.TaskStore, c client.Client, config computer.Config) error {
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
		if !run.Temporal {
			continue
		}
		if run.Status != state.RunRunning {
			if e := repairCleanup(ctx, store, config, run); e != nil {
				failures = append(failures, e)
			}
			continue
		}
		execution, e := c.DescribeWorkflowExecution(ctx, WorkflowID(run.ID), "")
		var missing *serviceerror.NotFound
		neverStarted := errors.As(e, &missing) && run.CancelRequested
		if e != nil && !neverStarted {
			if !errors.As(e, &missing) {
				failures = append(failures, e)
			}
			continue
		}
		if !neverStarted && execution.WorkflowExecutionInfo.Status == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
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
		status, detail := state.RunFailed, "Interrupted: Temporal execution ended before its terminal result was persisted; work was not retried."
		if neverStarted {
			detail = "Cancelled before Temporal execution started."
		}
		if run.CancelRequested && stopErr == nil {
			status = "cancelled"
			detail = "Cancelled by Austin."
		}
		if stopErr != nil {
			status = "failed"
			detail = "Interrupted: computer stop could not be confirmed during terminal recovery."
			failures = append(failures, stopErr)
		}
		if _, e = store.Publish(ctx, run.ID, run.Revision, status, detail, "", run.CostUnknown, true); e != nil {
			failures = append(failures, e)
		} else if stopErr == nil && run.CleanupPending {
			if e = closeTaskBrowser(ctx, store, config, run.ID, scopes); e != nil {
				failures = append(failures, e)
			}
		}
	}
	return errors.Join(failures...)
}

func repairCleanup(ctx context.Context, store state.TaskStore, config computer.Config, run state.Run) error {
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
func stopRecoveredJobs(ctx context.Context, store state.TaskStore, config computer.Config, id string) error {
	reader, ok := store.(interface {
		Operations(context.Context, string) ([]state.OperationRecord, error)
	})
	if !ok {
		return nil
	}
	ops, err := reader.Operations(ctx, id)
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
func closeTaskBrowser(ctx context.Context, store state.TaskStore, config computer.Config, id string, scopes []string) error {
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
func StopPaused(ctx context.Context, store state.TaskStore, config computer.Config, id string) (state.Run, error) {
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
	run, err = store.Publish(ctx, id, run.Revision, "cancelled", "Stopped by Austin.", "", run.CostUnknown, true)
	if err != nil {
		return run, err
	}
	if run.CleanupPending {
		err = closeTaskBrowser(ctx, store, config, id, scopes)
	}
	return run, err
}
