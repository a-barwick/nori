package task

import (
	"context"
	"errors"

	"nori/internal/prompts"
	"nori/internal/state"
)

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

func repairCleanup(ctx context.Context, store state.TaskStore, run state.Run) error {
	return store.FinishCleanup(ctx, run.ID, "")
}

func markRunCommandsDelivered(ctx context.Context, store state.TaskStore, runID string) error {
	snap, err := store.Reconcile(ctx, runID)
	if err != nil {
		return err
	}
	for _, cmd := range snap.Commands {
		if !cmd.Delivered {
			if err := store.Delivered(ctx, cmd.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// StopPaused completes an accepted Stop without waking paid workers.
// CancelRequested fences new work even if pause changes.
func StopPaused(ctx context.Context, store state.TaskStore, id string) (state.Run, error) {
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
			err = repairCleanup(ctx, store, run)
		}
		if err == nil {
			err = markRunCommandsDelivered(ctx, store, id)
		}
		return run, err
	}
	if !run.CancelRequested && run.Finishing == nil {
		return run, nil
	}
	// Persist the finishing intent first. No paid execution is needed.
	run, err = store.Publish(ctx, state.PublishRequest{
		RunID: id, Revision: run.Revision, Status: state.RunCancelled, Detail: prompts.Runtime("stopped", nil) + attemptedWrites(ctx, store, id), CostUnknown: run.CostUnknown, BypassRouting: true,
	})
	if err != nil {
		return run, err
	}
	var failures []error
	if err = repairCleanup(ctx, store, run); err != nil {
		failures = append(failures, err)
	}
	for _, child := range run.Subtasks {
		if _, err = StopPaused(ctx, store, child.ID); err != nil {
			failures = append(failures, err)
		}
	}
	if err = markRunCommandsDelivered(ctx, store, id); err != nil {
		failures = append(failures, err)
	}
	run, err = store.GetRun(ctx, id)
	return run, errors.Join(append(failures, err)...)
}

// Cleanup completes the same durable cleanup plan used after a crash and during a paused Stop.
func (a *Activities) Cleanup(ctx context.Context, id string) error {
	run, err := a.Store.GetRun(ctx, id)
	if err == nil {
		err = repairCleanup(ctx, a.Store, run)
	}
	return err
}
