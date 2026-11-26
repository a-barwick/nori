package invariants

import (
	"context"
	"fmt"
	"strings"
	"time"

	"nori/internal/scheduler"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

func (c *Checker) CheckQuiescence(ctx context.Context, users []UserTarget, now time.Time) error {
	now = now.UTC()
	sorted := sortUsers(users)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.quiescenceChecks++

	var vs Violations

	for _, t := range sorted {
		userID := t.User.ID
		if t.Store == nil {
			vs = append(vs, Violation{
				Invariant: "quiescence.store_required",
				UserID:    userID,
				Message:   "UserTarget.Store is nil",
			})
			continue
		}

		control, err := t.Store.Control(ctx)
		if err != nil {
			vs = append(vs, Violation{
				Invariant: "quiescence.control_read",
				UserID:    userID,
				Message:   fmt.Sprintf("Control failed: %v", err),
			})
			continue
		}
		if control.Paused {
			vs = append(vs, Violation{
				Invariant: "quiescence.not_paused",
				UserID:    userID,
				Message:   "Control.Paused is true; quiescence check expects unpaused drain",
			})
		}

		pendingPage, err := t.Store.Pending(ctx, "")
		if err != nil {
			vs = append(vs, Violation{
				Invariant: "quiescence.pending_read",
				UserID:    userID,
				Message:   fmt.Sprintf("Pending failed: %v", err),
			})
		} else {
			for _, cmd := range pendingPage.Commands {
				vs = append(vs, c.checkOwnedByLocked(c.runOwners, "run", cmd.RunID, userID, "pending command "+cmd.ID)...)
			}
			if len(pendingPage.Commands) > 0 {
				ids := make([]string, 0, len(pendingPage.Commands))
				for _, cmd := range pendingPage.Commands {
					ids = append(ids, cmd.ID)
				}
				vs = append(vs, Violation{
					Invariant: "quiescence.pending_commands",
					UserID:    userID,
					EntityID:  strings.Join(ids, ","),
					Message:   fmt.Sprintf("%d undelivered command(s) remain pending at quiescence", len(pendingPage.Commands)),
				})
			}
		}

		cleanupRuns, err := t.Store.CleanupTasks(ctx)
		if err != nil {
			vs = append(vs, Violation{
				Invariant: "quiescence.cleanup_read",
				UserID:    userID,
				Message:   fmt.Sprintf("CleanupTasks failed: %v", err),
			})
		} else {
			for _, run := range cleanupRuns {
				vs = append(vs, c.checkOwnedByLocked(c.runOwners, "run", run.ID, userID, "CleanupTasks")...)
				if run.Status != state.RunRunning || run.Finishing != nil || run.CleanupError != "" {
					vs = append(vs, Violation{
						Invariant: "quiescence.cleanup_stuck",
						UserID:    userID,
						EntityID:  run.ID,
						Message:   fmt.Sprintf("cleanup remains pending (status=%s finishing=%v cleanup_error=%q)", run.Status, run.Finishing != nil, run.CleanupError),
					})
				}
			}
		}

		openRuns, err := t.Store.OpenTasks(ctx)
		if err != nil {
			vs = append(vs, Violation{
				Invariant: "quiescence.open_tasks_read",
				UserID:    userID,
				Message:   fmt.Sprintf("OpenTasks failed: %v", err),
			})
		} else {
			for _, run := range openRuns {
				vs = append(vs, c.checkOwnedByLocked(c.runOwners, "run", run.ID, userID, "OpenTasks")...)
				if run.Finishing != nil {
					vs = append(vs, Violation{
						Invariant: "quiescence.finishing_stuck",
						UserID:    userID,
						EntityID:  run.ID,
						Message:   fmt.Sprintf("open run still has Finishing=%s at quiescence", run.Finishing.Status),
					})
				}
			}
		}

		routingPending, err := t.Store.UserRoutingPending(ctx)
		if err != nil {
			vs = append(vs, Violation{
				Invariant: "quiescence.routing_pending_read",
				UserID:    userID,
				Message:   fmt.Sprintf("UserRoutingPending failed: %v", err),
			})
		}
		pubRuns, err := t.Store.PublicationTasks(ctx)
		if err != nil {
			vs = append(vs, Violation{
				Invariant: "quiescence.publication_tasks_read",
				UserID:    userID,
				Message:   fmt.Sprintf("PublicationTasks failed: %v", err),
			})
		} else if !routingPending && len(pubRuns) > 0 {
			ids := make([]string, 0, len(pubRuns))
			for _, r := range pubRuns {
				ids = append(ids, r.ID)
			}
			vs = append(vs, Violation{
				Invariant: "quiescence.publication_stuck",
				UserID:    userID,
				EntityID:  strings.Join(ids, ","),
				Message:   fmt.Sprintf("%d held publication(s) remain stuck with UserRoutingPending=false", len(pubRuns)),
			})
		}

		conv, _, err := t.Store.ConversationStatus(ctx)
		if err != nil {
			vs = append(vs, Violation{
				Invariant: "quiescence.conversation_status_read",
				UserID:    userID,
				Message:   fmt.Sprintf("ConversationStatus failed: %v", err),
			})
		}
		wv, err := t.Store.WorkVersion(ctx, true)
		if err != nil {
			vs = append(vs, Violation{
				Invariant: "quiescence.work_version_read",
				UserID:    userID,
				Message:   fmt.Sprintf("WorkVersion failed: %v", err),
			})
		} else if wv != "" {
			if !conv.Pending() || !strings.HasPrefix(wv, "assistant:") {
				vs = append(vs, Violation{
					Invariant: "quiescence.work_version_non_empty",
					UserID:    userID,
					EntityID:  wv,
					Message:   fmt.Sprintf("WorkVersion(true) returned %q at quiescence (conversation_pending=%v)", wv, conv.Pending()),
				})
			}
		}

		if sess, err := t.Store.Session(ctx); err != nil {
			vs = append(vs, Violation{
				Invariant: "quiescence.session_read",
				UserID:    userID,
				Message:   fmt.Sprintf("Session failed: %v", err),
			})
		} else {
			for _, run := range sess.Runs {
				vs = append(vs, c.registerOwnerLocked(c.runOwners, "run", run.ID, userID)...)
			}
			for _, msg := range sess.Messages {
				if msg.RunID != "" {
					vs = append(vs, c.checkOwnedByLocked(c.runOwners, "run", msg.RunID, userID, "Session.Messages "+msg.ID)...)
				}
			}
		}

		vs = append(vs, c.checkSchedulerQuiescenceLocked(ctx, t, now)...)
	}

	if c.checkLeases && c.backend != nil {
		vs = append(vs, c.checkLeaseQuiescenceLocked(ctx, sorted, now)...)
	}

	vs = append(vs, c.checkMemoryQuiescenceLocked(ctx, sorted, now)...)

	if len(vs) > 0 {
		return vs
	}
	return nil
}

func (c *Checker) CheckLeaseQuiescence(ctx context.Context, users []UserTarget, now time.Time) error {
	now = now.UTC()
	sorted := sortUsers(users)

	c.mu.Lock()
	defer c.mu.Unlock()

	vs := c.checkLeaseQuiescenceLocked(ctx, sorted, now)
	if len(vs) > 0 {
		return Violations(vs)
	}
	return nil
}

func (c *Checker) checkLeaseQuiescenceLocked(_ context.Context, sorted []UserTarget, now time.Time) []Violation {
	var vs []Violation
	if c.backend == nil {
		return []Violation{{
			Invariant: "quiescence.lease_backend_required",
			Message:   "Checker.Backend is nil; cannot verify lease/wake quiescence",
		}}
	}

	snap := c.backend.Snapshot()
	control, ctrlVs := decodeControlFromSnapshot(snap)
	vs = append(vs, ctrlVs...)

	usersByID := make(map[string]UserTarget, len(sorted))
	runsByUser := make(map[string]map[string]state.Run, len(sorted))
	convByUser := make(map[string]state.ConversationControl, len(sorted))
	autosByUser := make(map[string]map[string]storedAutomation, len(sorted))

	for _, t := range sorted {
		userID := t.User.ID
		owner := t.User.DataOwner()
		usersByID[userID] = t

		runs, runVs := decodeRunsFromSnapshot(snap, owner)
		vs = append(vs, tagUser(userID, runVs)...)
		runsByUser[userID] = runs

		taskControls, tcVs := decodeTaskControlsFromSnapshot(snap, owner)
		vs = append(vs, tagUser(userID, tcVs)...)
		for _, runID := range sortedKeys(taskControls) {
			tc := taskControls[runID]
			run, hasRun := runs[runID]
			activeRun := !hasRun || run.Status == state.RunRunning || run.CleanupPending
			liveLease := tc.Owner != "" && tc.LeaseUntil.After(now)
			staleActiveOwner := tc.Owner != "" && activeRun
			orphanUntil := tc.Owner == "" && !tc.LeaseUntil.IsZero()
			if liveLease || staleActiveOwner || orphanUntil {
				vs = append(vs, Violation{
					Invariant: "quiescence.task_control_leaked_owner",
					UserID:    userID,
					EntityID:  runID,
					Message:   fmt.Sprintf("TaskControl retains lease at quiescence (owner=%q fence=%d lease_until=%s run_active=%v)", tc.Owner, tc.Fence, tc.LeaseUntil.UTC().Format(time.RFC3339Nano), activeRun),
				})
			}
		}

		conv, hasConv, err := decodeConversationFromSnapshot(snap, owner)
		if err != nil {
			vs = append(vs, Violation{
				Invariant: "conversation.decode",
				UserID:    userID,
				Message:   fmt.Sprintf("failed to decode conversation control: %v", err),
			})
		} else if hasConv {
			convByUser[userID] = conv
		}

		autos, autoVs := decodeAutomationsFromSnapshot(snap, owner)
		vs = append(vs, tagUser(userID, autoVs)...)
		autosByUser[userID] = autos
	}

	taskLeases := make(map[docstore.TaskKey]docstore.TaskLease, len(snap.TaskLeases))
	for _, lease := range snap.TaskLeases {
		taskLeases[docstore.TaskKey{UserID: lease.UserID, RunID: lease.RunID}] = lease
	}

	userWakes := make(map[string]map[string]docstore.UserWake, len(sorted))
	for _, w := range snap.UserWakes {
		lanes, ok := userWakes[w.UserID]
		if !ok {
			lanes = map[string]docstore.UserWake{}
			userWakes[w.UserID] = lanes
		}
		lanes[w.Lane] = w
	}

	for _, t := range sorted {
		userID := t.User.ID
		runs := runsByUser[userID]
		for _, runID := range sortedKeys(runs) {
			run := runs[runID]
			if run.Status == state.RunRunning || run.CleanupPending {
				if _, ok := taskLeases[docstore.TaskKey{UserID: userID, RunID: runID}]; !ok {
					vs = append(vs, Violation{
						Invariant: "quiescence.lease_missing_for_run",
						UserID:    userID,
						EntityID:  runID,
						Message:   fmt.Sprintf("run requires a task lease row (status=%s cleanup_pending=%v)", run.Status, run.CleanupPending),
					})
				}
			}
		}
	}

	for _, lease := range snap.TaskLeases {
		if _, known := usersByID[lease.UserID]; !known {
			vs = append(vs, Violation{
				Invariant: "quiescence.lease_unknown_user",
				UserID:    lease.UserID,
				EntityID:  lease.RunID,
				Message:   fmt.Sprintf("task lease references unknown user %q", lease.UserID),
			})
			continue
		}

		if lease.Owner != "" || lease.LeaseUntil != nil {
			until := "<nil>"
			if lease.LeaseUntil != nil {
				until = lease.LeaseUntil.UTC().Format(time.RFC3339Nano)
			}
			vs = append(vs, Violation{
				Invariant: "quiescence.task_lease_leaked_owner",
				UserID:    lease.UserID,
				EntityID:  lease.RunID,
				Message:   fmt.Sprintf("task lease retains live lease at quiescence (owner=%q lease_until=%s)", lease.Owner, until),
			})
		}

		if lease.Runnable && !lease.WakeAt.After(now) {
			vs = append(vs, Violation{
				Invariant: "quiescence.task_lease_undrained_runnable",
				UserID:    lease.UserID,
				EntityID:  lease.RunID,
				Message:   fmt.Sprintf("task lease remains runnable with wake_at=%s <= now=%s at quiescence", lease.WakeAt.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)),
			})
		}

		runs := runsByUser[lease.UserID]
		run, exists := runs[lease.RunID]
		if !exists {
			vs = append(vs, Violation{
				Invariant: "quiescence.lease_orphan_run",
				UserID:    lease.UserID,
				EntityID:  lease.RunID,
				Message:   "task lease exists for non-existent run",
			})
			continue
		}
		if run.Status != state.RunRunning && !run.CleanupPending {
			vs = append(vs, Violation{
				Invariant: "quiescence.lease_stale_for_terminal_run",
				UserID:    lease.UserID,
				EntityID:  lease.RunID,
				Message:   fmt.Sprintf("stale task lease exists for terminal clean run (status=%s)", run.Status),
			})
			continue
		}

		if lease.Runnable && lease.WaitingReason != docstore.WaitNone {
			vs = append(vs, Violation{
				Invariant: "quiescence.task_lease_runnable_has_waiting_reason",
				UserID:    lease.UserID,
				EntityID:  lease.RunID,
				Message:   fmt.Sprintf("runnable task lease has non-empty waiting_reason=%q", lease.WaitingReason),
			})
		}

		if !control.Paused && !lease.Runnable {
			switch lease.WaitingReason {
			case docstore.WaitPaused:
				vs = append(vs, Violation{
					Invariant: "quiescence.task_lease_stuck_paused",
					UserID:    lease.UserID,
					EntityID:  lease.RunID,
					Message:   "task lease remains parked with waiting_reason=paused while Control.Paused=false",
				})
			case docstore.WaitRoutingPending:
				if !convByUser[lease.UserID].RoutingPending() {
					vs = append(vs, Violation{
						Invariant: "quiescence.task_lease_stuck_routing_pending",
						UserID:    lease.UserID,
						EntityID:  lease.RunID,
						Message:   "task lease remains parked with waiting_reason=routing_pending while UserRoutingPending=false",
					})
				}
			case docstore.WaitQuestion:
				waitingOnSubtasks := run.Question != nil && run.Question.Kind == state.QuestionWork && len(state.RunningSubtasks(run)) > 0
				if run.Question == nil || run.CancelRequested || (run.Question.ResolvedBy != "" && !waitingOnSubtasks) {
					vs = append(vs, Violation{
						Invariant: "quiescence.task_lease_stuck_question",
						UserID:    lease.UserID,
						EntityID:  lease.RunID,
						Message:   fmt.Sprintf("task lease parked with waiting_reason=question but run cannot wait on question (has_question=%v resolved=%v waiting_subtasks=%v cancel_requested=%v)", run.Question != nil, run.Question != nil && run.Question.ResolvedBy != "", waitingOnSubtasks, run.CancelRequested),
					})
				}
			case docstore.WaitFinishing:
				if !hasActiveOrUncleanChild(run, runs) {
					vs = append(vs, Violation{
						Invariant: "quiescence.task_lease_stuck_finishing",
						UserID:    lease.UserID,
						EntityID:  lease.RunID,
						Message:   "task lease remains parked with waiting_reason=finishing when all children are terminal and clean",
					})
				}
			default:
				vs = append(vs, Violation{
					Invariant: "quiescence.task_lease_invalid_waiting_reason",
					UserID:    lease.UserID,
					EntityID:  lease.RunID,
					Message:   fmt.Sprintf("task lease parked with invalid or empty waiting_reason=%q", lease.WaitingReason),
				})
			}
		}
	}

	for _, w := range snap.UserWakes {
		if _, known := usersByID[w.UserID]; !known {
			vs = append(vs, Violation{
				Invariant: "quiescence.user_wake_unknown_user",
				UserID:    w.UserID,
				EntityID:  w.Lane,
				Message:   fmt.Sprintf("user wake references unknown user %q", w.UserID),
			})
			continue
		}
		if w.Owner != "" || w.LeaseUntil != nil {
			until := "<nil>"
			if w.LeaseUntil != nil {
				until = w.LeaseUntil.UTC().Format(time.RFC3339Nano)
			}
			vs = append(vs, Violation{
				Invariant: "quiescence.user_wake_leaked_owner",
				UserID:    w.UserID,
				EntityID:  w.Lane,
				Message:   fmt.Sprintf("user wake retains live lease at quiescence (owner=%q lease_until=%s)", w.Owner, until),
			})
		}
		if w.Runnable && !w.WakeAt.After(now) {
			vs = append(vs, Violation{
				Invariant: "quiescence.user_wake_undrained_runnable",
				UserID:    w.UserID,
				EntityID:  w.Lane,
				Message:   fmt.Sprintf("user wake remains runnable with wake_at=%s <= now=%s at quiescence", w.WakeAt.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)),
			})
		}
	}

	for _, t := range sorted {
		userID := t.User.ID
		lanes := userWakes[userID]

		if convByUser[userID].Pending() {
			w, ok := lanes[docstore.LaneConversation]
			if !ok || !w.Runnable || w.WakeAt.After(now) {
				vs = append(vs, Violation{
					Invariant: "quiescence.user_wake_missing_conversation",
					UserID:    userID,
					EntityID:  docstore.LaneConversation,
					Message:   "ConversationControl.Pending() is true but no due runnable conversation wake exists",
				})
			}
		}

		var dueAutoID string
		for _, autoID := range sortedKeys(autosByUser[userID]) {
			a := autosByUser[userID][autoID]
			if a.Status == "enabled" && a.NextAt != nil && !a.NextAt.After(now) {
				dueAutoID = autoID
				break
			}
		}
		if dueAutoID != "" {
			w, ok := lanes[docstore.LaneScheduler]
			if !ok || !w.Runnable || w.WakeAt.After(now) {
				vs = append(vs, Violation{
					Invariant: "quiescence.user_wake_missing_scheduler",
					UserID:    userID,
					EntityID:  dueAutoID,
					Message:   fmt.Sprintf("due enabled automation %q exists but no due runnable scheduler wake exists", dueAutoID),
				})
			}
		}
	}

	return vs
}

func hasActiveOrUncleanChild(parent state.Run, runs map[string]state.Run) bool {
	for _, link := range parent.Subtasks {
		if child, ok := runs[link.ID]; ok {
			if child.Status == state.RunRunning || child.CleanupPending {
				return true
			}
		} else if link.Status == state.RunRunning {
			return true
		}
	}
	for _, other := range runs {
		if other.ParentID == parent.ID && (other.Status == state.RunRunning || other.CleanupPending) {
			return true
		}
	}
	return false
}

func (c *Checker) checkSchedulerQuiescenceLocked(ctx context.Context, t UserTarget, now time.Time) []Violation {
	var vs []Violation
	userID := t.User.ID

	automations := map[string]storedAutomation{}
	after := ""
	for {
		page, next, err := t.Store.Resources(ctx, scheduler.CollectionAutomations, nil, after, 200)
		if err != nil {
			vs = append(vs, Violation{
				Invariant: "quiescence.automations_read",
				UserID:    userID,
				Message:   fmt.Sprintf("Resources(automations) failed: %v", err),
			})
			return vs
		}
		for _, r := range page {
			var a storedAutomation
			if err := r.Decode(&a); err != nil {
				vs = append(vs, Violation{
					Invariant: "automation.decode",
					UserID:    userID,
					EntityID:  r.ID,
					Message:   fmt.Sprintf("failed to decode automation: %v", err),
				})
				continue
			}
			a.Revision = r.Revision
			automations[r.ID] = a
		}
		if next == "" {
			break
		}
		after = next
	}

	var occurrences []storedOccurrence
	after = ""
	for {
		page, next, err := t.Store.Resources(ctx, scheduler.CollectionOccurrences, nil, after, 200)
		if err != nil {
			vs = append(vs, Violation{
				Invariant: "quiescence.occurrences_read",
				UserID:    userID,
				Message:   fmt.Sprintf("Resources(occurrences) failed: %v", err),
			})
			return vs
		}
		for _, r := range page {
			var o storedOccurrence
			if err := r.Decode(&o); err != nil {
				vs = append(vs, Violation{
					Invariant: "occurrence.decode",
					UserID:    userID,
					EntityID:  r.ID,
					Message:   fmt.Sprintf("failed to decode occurrence: %v", err),
				})
				continue
			}
			occurrences = append(occurrences, o)
		}
		if next == "" {
			break
		}
		after = next
	}

	for _, autoID := range sortedKeys(automations) {
		a := automations[autoID]
		if a.Status == "enabled" && a.NextAt != nil && !a.NextAt.After(now) {
			vs = append(vs, Violation{
				Invariant: "quiescence.due_automation_not_advanced",
				UserID:    userID,
				EntityID:  autoID,
				Message:   fmt.Sprintf("enabled automation has NextAt=%s <= now=%s after scheduler drain", a.NextAt.UTC().Format(time.RFC3339), now.Format(time.RFC3339)),
			})
		}
	}

	vs = append(vs, c.checkOccurrenceUniquenessAndOwnershipLocked(userID, automations, occurrences)...)

	for _, occ := range occurrences {
		if occ.Status == "pending" {
			vs = append(vs, Violation{
				Invariant: "quiescence.pending_occurrence_stuck",
				UserID:    userID,
				EntityID:  occ.ID,
				Message:   fmt.Sprintf("occurrence for automation %q remains pending at quiescence", occ.AutomationID),
			})
			continue
		}
		if (occ.Status == "accepted" || occ.Status == "running") && len(occ.TaskIDs) > 0 {
			run, err := t.Store.GetRun(ctx, occ.TaskIDs[0])
			if err == nil && run.Status != state.RunRunning {
				vs = append(vs, Violation{
					Invariant: "quiescence.unsettled_occurrence",
					UserID:    userID,
					EntityID:  occ.ID,
					Message:   fmt.Sprintf("occurrence status is %q but its task %q is already terminal (%s)", occ.Status, run.ID, run.Status),
				})
			}
		}
	}

	return vs
}
