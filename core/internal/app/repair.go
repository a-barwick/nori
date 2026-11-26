package app

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/task"
)

// RepairLeases reconciles durable run/conversation/scheduler state against the
// lease table so interrupted work is re-queued and finished work is pruned.
func (a *App) RepairLeases(ctx context.Context) error {
	var failures []error
	if a.Tasks != nil {
		if err := a.Tasks.CleanupUploads(ctx); err != nil {
			failures = append(failures, err)
		}
	}
	control, err := a.Store.Control(ctx)
	if err != nil {
		return err
	}
	needLease, loadErr := a.loadActiveRuns(ctx)
	if loadErr != nil {
		failures = append(failures, loadErr)
	}
	conv, _, convErr := a.Store.ConversationStatus(ctx)
	if convErr != nil {
		failures = append(failures, convErr)
	}

	existingLeases, leaseErr := a.listExistingTaskLeases(ctx)
	if leaseErr != nil {
		failures = append(failures, leaseErr)
	}

	failures = append(failures, a.repairActiveTaskLeases(ctx, control, needLease, existingLeases, conv, convErr)...)
	if a.Leases != nil {
		failures = append(failures, a.pruneOrphanTaskLeases(ctx, needLease, existingLeases)...)
	}
	failures = append(failures, a.reconcilePendingCommands(ctx, needLease)...)
	if a.Leases != nil {
		failures = append(failures, a.repairUserWakes(ctx, conv, convErr)...)
	}
	return errors.Join(failures...)
}

func (a *App) loadActiveRuns(ctx context.Context) (map[string]state.Run, error) {
	var failures []error
	openRuns, err := a.Store.OpenTasks(ctx)
	if err != nil {
		failures = append(failures, err)
	}
	cleanupRuns, err := a.Store.CleanupTasks(ctx)
	if err != nil {
		failures = append(failures, err)
	}
	needLease := make(map[string]state.Run, len(openRuns)+len(cleanupRuns))
	for _, r := range openRuns {
		needLease[r.ID] = r
	}
	for _, r := range cleanupRuns {
		needLease[r.ID] = r
	}
	return needLease, errors.Join(failures...)
}

func (a *App) listExistingTaskLeases(ctx context.Context) (map[string]docstore.TaskLease, error) {
	if a.Leases == nil {
		return nil, nil
	}
	list, err := a.Leases.ListTaskLeases(ctx, a.UserID)
	if err != nil {
		return nil, err
	}
	existing := make(map[string]docstore.TaskLease, len(list))
	for _, l := range list {
		existing[l.RunID] = l
	}
	return existing, nil
}

func (a *App) repairActiveTaskLeases(
	ctx context.Context,
	control state.Control,
	needLease map[string]state.Run,
	existingLeases map[string]docstore.TaskLease,
	conv state.ConversationControl,
	convErr error,
) []error {
	var failures []error
	needIDs := make([]string, 0, len(needLease))
	for runID := range needLease {
		needIDs = append(needIDs, runID)
	}
	slices.Sort(needIDs)
	for _, runID := range needIDs {
		run := needLease[runID]
		if control.Paused && (run.CancelRequested || run.Finishing != nil || (run.Status != state.RunRunning && run.CleanupPending)) {
			stopped, err := task.StopPaused(ctx, a.Store, runID)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if stopped.Status != state.RunRunning && !stopped.CleanupPending {
				if a.Leases != nil {
					if _, err := a.Leases.DeleteTaskLease(ctx, a.UserID, runID, 0); err != nil {
						failures = append(failures, err)
					}
				}
				continue
			}
		}
		if a.Leases == nil {
			continue
		}
		lease, exists := existingLeases[runID]
		if !exists {
			if _, err := a.Leases.EnsureTaskLease(ctx, a.UserID, runID, time.Time{}); err != nil {
				failures = append(failures, err)
			}
			continue
		}
		if !lease.Runnable && !control.Paused && shouldWakeTaskLease(lease, run, needLease, conv, convErr) {
			if err := a.Leases.WakeTask(ctx, a.UserID, runID, time.Time{}); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return failures
}

func shouldWakeTaskLease(lease docstore.TaskLease, run state.Run, needLease map[string]state.Run, conv state.ConversationControl, convErr error) bool {
	switch lease.WaitingReason {
	case docstore.WaitPaused:
		return true
	case docstore.WaitRoutingPending:
		return convErr == nil && !conv.RoutingPending()
	case docstore.WaitQuestion:
		waitingOnSubtasks := run.Question != nil && run.Question.Kind == state.QuestionWork && len(state.RunningSubtasks(run)) > 0
		return run.Question == nil || run.CancelRequested || (run.Question.ResolvedBy != "" && !waitingOnSubtasks)
	case docstore.WaitFinishing:
		return !hasActiveOrUncleanChild(run, needLease)
	default:
		return true
	}
}

func (a *App) pruneOrphanTaskLeases(ctx context.Context, needLease map[string]state.Run, existingLeases map[string]docstore.TaskLease) []error {
	var failures []error
	existingIDs := make([]string, 0, len(existingLeases))
	for runID := range existingLeases {
		existingIDs = append(existingIDs, runID)
	}
	slices.Sort(existingIDs)
	for _, runID := range existingIDs {
		if _, needed := needLease[runID]; needed {
			continue
		}
		run, err := a.Store.GetRun(ctx, runID)
		if errors.Is(err, state.ErrNotFound) {
			if _, delErr := a.Leases.DeleteTaskLease(ctx, a.UserID, runID, 0); delErr != nil {
				failures = append(failures, delErr)
			}
			continue
		}
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if run.Status != state.RunRunning && !run.CleanupPending {
			snap, sErr := a.Store.Reconcile(ctx, runID)
			if sErr != nil {
				failures = append(failures, sErr)
				continue
			}
			var delCmdErr error
			for _, cmd := range snap.Commands {
				if !cmd.Delivered {
					if dErr := a.Store.Delivered(ctx, cmd.ID); dErr != nil {
						delCmdErr = errors.Join(delCmdErr, dErr)
					}
				}
			}
			if delCmdErr != nil {
				failures = append(failures, delCmdErr)
				continue
			}
			if _, delErr := a.Leases.DeleteTaskLease(ctx, a.UserID, runID, 0); delErr != nil {
				failures = append(failures, delErr)
			}
		}
	}
	return failures
}

func (a *App) reconcilePendingCommands(ctx context.Context, needLease map[string]state.Run) []error {
	var failures []error
	after := ""
	for {
		page, err := a.Store.Pending(ctx, after)
		if err != nil {
			failures = append(failures, err)
			break
		}
		for _, cmd := range page.Commands {
			if _, active := needLease[cmd.RunID]; active {
				continue
			}
			run, err := a.Store.GetRun(ctx, cmd.RunID)
			if errors.Is(err, state.ErrNotFound) || (err == nil && run.Status != state.RunRunning && !run.CleanupPending) {
				if dErr := a.Store.Delivered(ctx, cmd.ID); dErr != nil {
					failures = append(failures, dErr)
				}
			} else if err != nil {
				failures = append(failures, err)
			} else if a.Leases != nil {
				if _, eErr := a.Leases.EnsureTaskLease(ctx, a.UserID, cmd.RunID, time.Time{}); eErr != nil {
					failures = append(failures, eErr)
				}
			}
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	return failures
}

func (a *App) repairUserWakes(ctx context.Context, conv state.ConversationControl, convErr error) []error {
	var failures []error
	wakesByLane := map[string]docstore.UserWake{}
	if wakes, err := a.Leases.ListUserWakes(ctx, a.UserID); err != nil {
		failures = append(failures, err)
	} else {
		for _, w := range wakes {
			wakesByLane[w.Lane] = w
		}
	}
	if convErr == nil && (conv.Pending() || conv.Active != "") {
		w, ok := wakesByLane[docstore.LaneConversation]
		if !ok || !w.Runnable {
			if err := a.Leases.WakeUser(ctx, a.UserID, docstore.LaneConversation, time.Time{}); err != nil {
				failures = append(failures, err)
			}
		}
	} else if convErr == nil {
		if wakeAt, err := a.NextConversationWake(ctx); err == nil && !wakeAt.IsZero() {
			w, ok := wakesByLane[docstore.LaneConversation]
			if !ok || !w.Runnable || w.WakeAt.After(wakeAt) {
				if err := a.Leases.WakeUser(ctx, a.UserID, docstore.LaneConversation, wakeAt); err != nil {
					failures = append(failures, err)
				}
			}
		}
	}
	if nextDue := a.NextSchedulerWake(ctx); nextDue != nil {
		w, ok := wakesByLane[docstore.LaneScheduler]
		if !ok || !w.Runnable || w.WakeAt.After(*nextDue) {
			if err := a.Leases.WakeUser(ctx, a.UserID, docstore.LaneScheduler, *nextDue); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return failures
}

func (a *App) NextSchedulerWake(ctx context.Context) *time.Time {
	var earliest *time.Time
	if a.Scheduler != nil {
		earliest = a.Scheduler.NextWake(ctx)
	}
	if a.Assistant != nil && a.Assistant.Permissions != nil {
		if answers, _, err := a.Assistant.Permissions.UndeliveredAnswers(ctx); err == nil {
			for _, ans := range answers {
				if ans.Recheck {
					continue
				}
				if run, rErr := a.Store.GetRun(ctx, ans.TaskID); rErr == nil && run.Status == state.RunRunning {
					retryAt := time.Now().UTC().Add(2 * time.Second)
					if a.Scheduler != nil && a.Scheduler.Now != nil {
						retryAt = a.Scheduler.Now().UTC().Add(2 * time.Second)
					}
					if earliest == nil || retryAt.Before(*earliest) {
						earliest = &retryAt
					}
					break
				}
			}
		}
	}
	return earliest
}

func hasActiveOrUncleanChild(parent state.Run, activeRuns map[string]state.Run) bool {
	for _, link := range parent.Subtasks {
		if child, ok := activeRuns[link.ID]; ok {
			if child.Status == state.RunRunning || child.CleanupPending {
				return true
			}
		} else if link.Status == state.RunRunning {
			return true
		}
	}
	for _, other := range activeRuns {
		if other.ParentID == parent.ID && (other.Status == state.RunRunning || other.CleanupPending) {
			return true
		}
	}
	return false
}

func (a *App) deliverAnswers(ctx context.Context) error {
	if a.Assistant == nil || a.Assistant.Permissions == nil || a.Assistant.Answer == nil {
		return nil
	}
	answers, ids, err := a.Assistant.Permissions.UndeliveredAnswers(ctx)
	if err != nil {
		return err
	}
	for i, answer := range answers {
		run, runErr := a.Store.GetRun(ctx, answer.TaskID)
		waiting := runErr == nil && run.Status == state.RunRunning && run.Question != nil && run.Question.ID == answer.Question && run.Question.ResolvedBy == ""
		if answer.Recheck && !waiting {
			continue
		}
		err := a.Assistant.Answer(ctx, answer.CommandID, answer.TaskID, answer.Question, answer.Text)
		if errors.Is(err, state.ErrQuestionStale) || errors.Is(err, state.ErrBusy) {
			if current, e := a.Store.GetRun(ctx, answer.TaskID); e == nil && current.Status == state.RunRunning && (current.Question == nil || current.Question.ID == answer.Question) {
				continue
			}
		}
		if (err == nil || errors.Is(err, state.ErrDuplicate) || errors.Is(err, state.ErrQuestionStale) || errors.Is(err, state.ErrBusy)) && !answer.Recheck {
			if markErr := a.Assistant.Permissions.MarkDelivered(ctx, ids[i]); markErr != nil {
				slog.WarnContext(ctx, "failed to mark interaction answer delivered", "interaction_id", ids[i], "err", markErr)
			}
		}
	}
	return nil
}
