package task

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"nori/internal/observability"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

func receiptDeadline(ctx context.Context) time.Time {
	deadline, _ := ctx.Deadline()
	return deadline.UTC().Truncate(time.Microsecond)
}

// A stopped or timed-out worker can still have a result to save. Give that
// write its own context within the activity's original execution deadline.
func receiptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	until := time.Now().Add(10 * time.Second)
	if deadline := receiptDeadline(ctx); !deadline.IsZero() && deadline.Before(until) {
		until = deadline
	}
	return context.WithDeadline(context.WithoutCancel(ctx), until)
}

// A retry waits while the first activity still has time to settle. lookup
// returns zero when there is a result or no recorded owner to wait for.
func (a *Activities) waitForReceipt(ctx context.Context, runID string, lookup func() (time.Time, error)) error {
	for delay := 250 * time.Millisecond; ; delay = min(delay*2, 2*time.Second) {
		until, err := lookup()
		if err != nil || !time.Now().Before(until) {
			return err
		}
		run, err := a.Store.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if run.CancelRequested || run.Status != state.RunRunning {
			return state.ErrCancelled
		}
		select {
		case <-time.After(min(delay, time.Until(until))):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (a *Activities) recoverModelRef(ctx context.Context, runID string, intent state.TaskModelCheckpoint) (RefModelOutcome, error) {
	interrupted := RefModelOutcome{Ref: intent.Parent, Failure: "The model request was interrupted before its result was saved.", Interrupted: true}
	data, err := json.Marshal(interrupted)
	if err != nil {
		return RefModelOutcome{}, classified(err)
	}
	for {
		var saved state.TaskModelCheckpoint
		err := a.waitForReceipt(ctx, runID, func() (time.Time, error) {
			checkpoint, err := a.Store.TaskModelCheckpoint(ctx, runID, intent.ID)
			if errors.Is(err, state.ErrNotFound) {
				return time.Time{}, nil
			}
			if err != nil {
				return time.Time{}, err
			}
			if checkpoint.Revision != intent.Revision || checkpoint.Parent != intent.Parent || checkpoint.Subtask != intent.Subtask {
				return time.Time{}, classified(errors.New("task model checkpoint input conflict"))
			}
			saved = checkpoint
			if len(saved.Outcome) != 0 {
				return time.Time{}, nil
			}
			return saved.RecoveryUntil, nil
		})
		if errors.Is(err, state.ErrCancelled) {
			// Stop can finish promptly. The task still accounts for the unknown request.
			return interrupted, nil
		}
		if err != nil {
			return RefModelOutcome{}, err
		}
		if len(saved.Outcome) != 0 {
			var out RefModelOutcome
			if err := json.Unmarshal(saved.Outcome, &out); err != nil {
				return RefModelOutcome{}, classified(errors.New("invalid task model checkpoint outcome"))
			}
			out.Ref = saved.Ref
			observability.Event(ctx, "task.model.checkpoint_reused")
			return out, nil
		}
		intent.RecoveryUntil, intent.Outcome = saved.RecoveryUntil, data
		// If a receipt or an intent landed since our last read, observe it
		// before deciding that the model step ended without a result.
		_, err = a.Store.SaveTaskModelCheckpoint(ctx, runID, intent, nil)
		if !errors.Is(err, docstore.ErrExists) {
			return interrupted, err
		}
	}
}
