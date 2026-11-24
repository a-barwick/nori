package records

import (
	"context"
	"errors"

	"nori/internal/state"
	"nori/internal/state/docstore"
)

func (d *Database) taskControls() *docstore.CollectionRef {
	return d.root().Collection("task_controls")
}

func decodeTaskControl(doc *docstore.DocumentSnapshot, err error) (state.TaskControl, error) {
	if notFound(err) {
		return state.TaskControl{}, state.ErrNotFound
	}
	if err != nil {
		return state.TaskControl{}, err
	}
	var ctrl state.TaskControl
	err = doc.DataTo(&ctrl)
	return ctrl, err
}

func (d *Database) GetTaskControl(ctx context.Context, runID string) (state.TaskControl, error) {
	return decodeTaskControl(d.taskControls().Doc(runID).Get(ctx))
}

func (d *Database) AcquireTaskControl(ctx context.Context, runID, owner string) (state.TaskControl, state.TaskFence, error) {
	if runID == "" || owner == "" {
		return state.TaskControl{}, state.TaskFence{}, errors.New("task control acquire requires run_id and owner")
	}
	var ctrl state.TaskControl
	var fence state.TaskFence
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		ref := d.taskControls().Doc(runID)
		existing, err := decodeTaskControl(tx.Get(ref))
		if err != nil && !errors.Is(err, state.ErrNotFound) {
			return err
		}
		now := d.now()
		if err := state.CanAcquireTask(existing, now); err != nil {
			return err
		}
		ctrl, fence = state.LeaseTask(existing, runID, owner, now)
		return tx.Set(ref, ctrl)
	})
	if err != nil {
		return state.TaskControl{}, state.TaskFence{}, err
	}
	return ctrl, fence, nil
}

func (d *Database) RenewTaskControl(ctx context.Context, runID string, f state.TaskFence) error {
	return d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		ref := d.taskControls().Doc(runID)
		ctrl, err := decodeTaskControl(tx.Get(ref))
		if errors.Is(err, state.ErrNotFound) {
			return state.ErrTaskBusy
		}
		if err != nil {
			return err
		}
		renewed, err := state.RenewTask(ctrl, f, d.now())
		if err != nil {
			return err
		}
		return tx.Set(ref, renewed)
	})
}

func (d *Database) SaveTaskControl(ctx context.Context, f state.TaskFence, ctrl state.TaskControl) (state.TaskControl, error) {
	var saved state.TaskControl
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		ref := d.taskControls().Doc(ctrl.RunID)
		stored, err := decodeTaskControl(tx.Get(ref))
		if errors.Is(err, state.ErrNotFound) {
			return state.ErrTaskBusy
		}
		if err != nil {
			return err
		}
		updated, err := state.ApplyTaskControlUpdate(stored, ctrl, f, d.now())
		if err != nil {
			return err
		}
		if err := tx.Set(ref, updated); err != nil {
			return err
		}
		saved = updated
		return nil
	})
	if err != nil {
		return state.TaskControl{}, err
	}
	return saved, nil
}

func (d *Database) AdvanceTaskStep(ctx context.Context, f state.TaskFence, ctrl state.TaskControl, spend *state.SpendUpdate) (state.TaskControl, state.Run, error) {
	runID := ctrl.RunID
	if spend != nil && spend.RunID != "" {
		if runID == "" {
			runID = spend.RunID
		} else if spend.RunID != runID {
			return state.TaskControl{}, state.Run{}, errors.New("spend run_id does not match task control run_id")
		}
	}
	var savedCtrl state.TaskControl
	var updatedRun state.Run
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		// All reads before any writes.
		ctrlRef := d.taskControls().Doc(runID)
		stored, err := decodeTaskControl(tx.Get(ctrlRef))
		if errors.Is(err, state.ErrNotFound) {
			return state.ErrTaskBusy
		}
		if err != nil {
			return err
		}
		nextCtrl, err := state.ApplyTaskControlUpdate(stored, ctrl, f, d.now())
		if err != nil {
			return err
		}

		runRef := d.runs().Doc(runID)
		run, err := decodeRun(tx.Get(runRef))
		if err != nil {
			return err
		}

		var parentRef *docstore.DocumentRef
		var parent state.Run
		if spend != nil && run.ParentID != "" {
			parentRef = d.runs().Doc(run.ParentID)
			parent, err = decodeRun(tx.Get(parentRef))
			if err != nil {
				return err
			}
		}

		// Writes.
		if spend != nil {
			spent, budget, costUnknown := state.MergeSpend(run, *spend)
			if parentRef != nil {
				rolled := state.RollUpSpend(parent, run, *spend)
				if err := tx.Update(parentRef, []docstore.Update{
					{Path: "subtask_spent_usd", Value: rolled.SubtaskSpent},
					{Path: "cost_unknown", Value: rolled.CostUnknown},
				}); err != nil {
					return err
				}
			}
			if err := tx.Update(runRef, []docstore.Update{
				{Path: "spent_usd", Value: spent},
				{Path: "budget_usd", Value: budget},
				{Path: "cost_unknown", Value: costUnknown},
			}); err != nil {
				return err
			}
			run.Spent = spent
			run.Budget = budget
			run.CostUnknown = costUnknown
		}
		if err := tx.Set(ctrlRef, nextCtrl); err != nil {
			return err
		}
		savedCtrl = nextCtrl
		updatedRun = run
		return nil
	})
	if err != nil {
		return state.TaskControl{}, state.Run{}, err
	}
	return savedCtrl, updatedRun, nil
}

func (d *Database) ReleaseTaskControl(ctx context.Context, runID string, f state.TaskFence) error {
	return d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		ref := d.taskControls().Doc(runID)
		ctrl, err := decodeTaskControl(tx.Get(ref))
		if errors.Is(err, state.ErrNotFound) {
			return state.ErrTaskBusy
		}
		if err != nil {
			return err
		}
		released, err := state.ReleaseTask(ctrl, f, d.now())
		if err != nil {
			return err
		}
		return tx.Set(ref, released)
	})
}
