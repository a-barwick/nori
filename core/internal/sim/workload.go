package sim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"nori/internal/permissions"
	"nori/internal/scheduler"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/task"
)

const (
	drainMaxRounds = 40
	drainMaxTicks  = 128
)

// SimError wraps an invariant or drain failure with seed, step, phase, and
// recent trace context for instant deterministic reproduction.
type SimError struct {
	Seed  int64
	Step  int
	Phase string
	Err   error
	Trace string
}

func (e *SimError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "sim failure [seed=%d step=%d phase=%s]: %v", e.Seed, e.Step, e.Phase, e.Err)
	if e.Trace != "" {
		b.WriteString("\n--- last 40 trace entries ---\n")
		b.WriteString(e.Trace)
	}
	return b.String()
}

func (e *SimError) Unwrap() error { return e.Err }

// RunSimulation executes steps adversarial chaos operations, checking continuous
// safety invariants after every single step, and then drains to quiescence and
// verifies all liveness invariants.
func (w *World) RunSimulation(ctx context.Context, steps int) error {
	targets := w.UserTargets()

	// Initial safety check on the fresh world.
	if err := w.Checker.CheckSafetyAt(ctx, targets, w.Clock.Now()); err != nil {
		return &SimError{
			Seed:  w.Seed,
			Step:  0,
			Phase: "safety.initial",
			Err:   err,
			Trace: w.Trace.FormatLast(40),
		}
	}

	for step := 1; step <= steps; step++ {
		w.step = step
		w.runChaosStep(ctx)

		if err := w.Checker.CheckSafetyAt(ctx, targets, w.Clock.Now()); err != nil {
			return &SimError{
				Seed:  w.Seed,
				Step:  step,
				Phase: "safety",
				Err:   err,
				Trace: w.Trace.FormatLast(40),
			}
		}
	}

	if err := w.DrainToQuiescence(ctx); err != nil {
		return &SimError{
			Seed:  w.Seed,
			Step:  steps,
			Phase: "quiescence",
			Err:   err,
			Trace: w.Trace.FormatLast(40),
		}
	}
	return nil
}

// runChaosStep picks one weighted adversarial action purely from w.RNG.
func (w *World) runChaosStep(ctx context.Context) {
	roll := w.RNG.IntN(100)
	switch {
	case roll < 18:
		w.stepCommandBatch(ctx, w.pickUser())
	case roll < 42:
		w.stepWorkerExec(ctx, w.pickUser())
	case roll < 56:
		w.stepPermissions(ctx, w.pickUser())
	case roll < 68:
		w.stepScheduler(ctx, w.pickUser())
	case roll < 78:
		w.stepConversation(ctx, w.pickUser())
	case roll < 92:
		w.stepReconcileAndWakes(ctx, w.pickUser())
	default:
		w.stepTimeAndPause(ctx)
	}
}

func (w *World) pickUser() *UserWorld {
	return w.Users[w.RNG.IntN(len(w.Users))]
}

func (w *World) pickOtherUser(uw *UserWorld) *UserWorld {
	if len(w.Users) < 2 {
		return uw
	}
	for range 4 {
		other := w.Users[w.RNG.IntN(len(w.Users))]
		if other != uw {
			return other
		}
	}
	for _, other := range w.Users {
		if other != uw {
			return other
		}
	}
	return uw
}

// -----------------------------------------------------------------------------
// 1. Command Batches
// -----------------------------------------------------------------------------

func (w *World) stepCommandBatch(ctx context.Context, uw *UserWorld) {
	choice := w.RNG.IntN(100)

	// Duplicate retry of a previously submitted command to verify idempotent receipt.
	if choice < 15 && len(uw.submittedCmds) > 0 {
		cmd := uw.submittedCmds[w.RNG.IntN(len(uw.submittedCmds))]
		receipts, err := uw.Store.AcceptCommandBatch(ctx, []state.Command{cmd}, nil)
		if err == nil {
			_ = w.Checker.RecordCommandReceipts(uw.User.ID, receipts)
		}
		w.Record(uw.User.ID, "client", "cmd.retry", fmt.Sprintf("cmd=%s run=%s kind=%s err=%v", cmd.ID, cmd.RunID, cmd.Kind, err))
		return
	}

	open, err := uw.Store.OpenTasks(ctx)
	if err != nil {
		w.Record(uw.User.ID, "client", "cmd.open_err", err.Error())
		return
	}

	var batch []state.Command
	actionName := "cmd.start"

	switch {
	case choice < 38 || len(open) == 0:
		// Start 1 or 2 new top-level tasks (or subtask if a parent has an open work question).
		if parent, ok := pickParentWithWorkQuestion(open, w.RNG); ok && w.RNG.IntN(2) == 0 {
			actionName = "cmd.start_subtask"
			nSub := 1 + w.RNG.IntN(2)
			for i := range nSub {
				subID := w.IDs.Next()
				batch = append(batch, state.Command{
					ID:         w.IDs.Next(),
					RunID:      subID,
					ParentID:   parent.ID,
					QuestionID: parent.Question.ID,
					Kind:       state.CommandStart,
					Text:       fmt.Sprintf("subtask %d for %s at step %d", i+1, parent.ID, w.step),
				})
			}
		} else {
			nStart := 1
			if w.RNG.IntN(4) == 0 {
				nStart = 2
			}
			for i := range nStart {
				runID := w.IDs.Next()
				cmd := state.Command{
					ID:    w.IDs.Next(),
					RunID: runID,
					Kind:  state.CommandStart,
					Text:  fmt.Sprintf("task %d at step %d", i+1, w.step),
				}
				if w.RNG.IntN(3) == 0 {
					cmd.BudgetUSD = 0.25 + float64(w.RNG.IntN(4))*0.25
				}
				if len(uw.goalIDs) > 0 && w.RNG.IntN(3) == 0 {
					cmd.Origin = &state.TaskOrigin{
						ConversationID: "assistant",
						GoalID:         uw.goalIDs[w.RNG.IntN(len(uw.goalIDs))],
					}
				}
				batch = append(batch, cmd)
			}
		}

	case choice < 58:
		// Steer an existing open task (supersedes revision & interactions!).
		actionName = "cmd.steer"
		target := open[w.RNG.IntN(len(open))]
		batch = append(batch, state.Command{
			ID:        w.IDs.Next(),
			RunID:     target.ID,
			Kind:      state.CommandSteer,
			Text:      fmt.Sprintf("steer rev=%d step=%d", target.Revision+1, w.step),
			Interrupt: w.RNG.IntN(2) == 0,
		})

	case choice < 76:
		// Stop an existing open task (works even while paused!).
		actionName = "cmd.stop"
		target := open[w.RNG.IntN(len(open))]
		batch = append(batch, state.Command{
			ID:    w.IDs.Next(),
			RunID: target.ID,
			Kind:  state.CommandStop,
		})

	case choice < 90:
		// Answer an open question on a running task if one exists; otherwise start a subtask or steer.
		if qRun, ok := pickRunWithUnresolvedQuestion(open, w.RNG); ok {
			if qRun.Question.Kind == state.QuestionWork && qRun.ParentID == "" && w.RNG.IntN(2) == 0 {
				actionName = "cmd.start_subtask"
				subID := w.IDs.Next()
				batch = append(batch, state.Command{
					ID:         w.IDs.Next(),
					RunID:      subID,
					ParentID:   qRun.ID,
					QuestionID: qRun.Question.ID,
					Kind:       state.CommandStart,
					Text:       fmt.Sprintf("subtask for work question %s", qRun.Question.ID),
				})
			} else {
				actionName = "cmd.answer"
				batch = append(batch, state.Command{
					ID:         w.IDs.Next(),
					RunID:      qRun.ID,
					Kind:       state.CommandAnswer,
					QuestionID: qRun.Question.ID,
					Text:       fmt.Sprintf("answer at step %d", w.step),
				})
			}
		} else {
			actionName = "cmd.steer"
			target := open[w.RNG.IntN(len(open))]
			batch = append(batch, state.Command{
				ID:    w.IDs.Next(),
				RunID: target.ID,
				Kind:  state.CommandSteer,
				Text:  fmt.Sprintf("steer fallback step=%d", w.step),
			})
		}

	default:
		// Register a Hold on an open or recent run.
		actionName = "cmd.hold"
		target := open[w.RNG.IntN(len(open))]
		batch = append(batch, state.Command{
			ID:    w.IDs.Next(),
			RunID: target.ID,
			Kind:  state.CommandHold,
			Text:  fmt.Sprintf("follow-up question for %s at step %d", target.ID, w.step),
		})
	}

	receipts, err := uw.Store.AcceptCommandBatch(ctx, batch, nil)
	if err == nil {
		_ = w.Checker.RecordCommandReceipts(uw.User.ID, receipts)
	}
	if err == nil || errors.Is(err, memstore.ErrInjectedFault) {
		for _, c := range batch {
			uw.submittedCmds = append(uw.submittedCmds, c)
		}
		if len(uw.submittedCmds) > 64 {
			uw.submittedCmds = uw.submittedCmds[len(uw.submittedCmds)-64:]
		}
	}
	w.Record(uw.User.ID, "client", actionName, fmt.Sprintf("count=%d run=%s err=%v", len(batch), batch[0].RunID, err))
}

func pickParentWithWorkQuestion(open []state.Run, rng interface{ IntN(int) int }) (state.Run, bool) {
	var cands []state.Run
	for _, r := range open {
		if r.ParentID == "" && !r.CancelRequested && r.Finishing == nil &&
			r.Question != nil && r.Question.Kind == state.QuestionWork &&
			r.Question.ResolvedBy == "" && r.Question.Revision == r.Revision {
			cands = append(cands, r)
		}
	}
	if len(cands) == 0 {
		return state.Run{}, false
	}
	return cands[rng.IntN(len(cands))], true
}

func pickRunWithUnresolvedQuestion(open []state.Run, rng interface{ IntN(int) int }) (state.Run, bool) {
	var cands []state.Run
	for _, r := range open {
		if !r.CancelRequested && r.Finishing == nil &&
			r.Question != nil && r.Question.ResolvedBy == "" && r.Question.Revision == r.Revision {
			cands = append(cands, r)
		}
	}
	if len(cands) == 0 {
		return state.Run{}, false
	}
	return cands[rng.IntN(len(cands))], true
}

// -----------------------------------------------------------------------------
// 2. Simulated Worker Execution
// -----------------------------------------------------------------------------

func (w *World) stepWorkerExec(ctx context.Context, uw *UserWorld) {
	// Respect global pause: never claim paid task execution while paused; repair StopPaused instead.
	if paused, err := w.Paused(ctx); err == nil && paused {
		err := w.repairUserLeases(ctx, uw)
		w.Record(uw.User.ID, "worker", "paused_repair", fmt.Sprintf("err=%v", err))
		return
	}

	// Pick a currently claimable runnable task lease for this user.
	leases, err := w.Backend.ListTaskLeases(ctx, uw.User.ID)
	if err != nil {
		w.Record(uw.User.ID, "worker", "list_leases_err", err.Error())
		return
	}
	now := w.Clock.Now()
	var claimable []docstore.TaskLease
	for _, l := range leases {
		if !l.Runnable || l.WakeAt.After(now) {
			continue
		}
		if l.LeaseUntil != nil && l.LeaseUntil.After(now) {
			continue
		}
		claimable = append(claimable, l)
	}
	if len(claimable) == 0 {
		err := w.repairUserLeases(ctx, uw)
		w.Record(uw.User.ID, "worker", "repair_for_work", fmt.Sprintf("err=%v", err))
		return
	}

	chosen := claimable[w.RNG.IntN(len(claimable))]
	owner := w.nextWorkerOwner("coord-worker")
	claim, ok, err := w.Backend.ClaimTaskByID(ctx, uw.User.ID, chosen.RunID, owner, state.TaskLease)
	if err != nil || !ok {
		w.Record(uw.User.ID, "worker", "claim_skip", fmt.Sprintf("run=%s ok=%v err=%v", chosen.RunID, ok, err))
		return
	}
	runID := claim.RunID

	ctrl, fence, err := uw.Store.AcquireTaskControl(ctx, runID, owner)
	if err != nil {
		_ = w.Backend.ReleaseTask(ctx, uw.User.ID, runID, owner, claim.WakeSeq, w.Clock.Now().Add(2*time.Second))
		w.Record(uw.User.ID, "worker", "acquire_ctrl_err", fmt.Sprintf("run=%s err=%v", runID, err))
		return
	}
	w.Stats.WorkerSteps++

	actionRoll := w.RNG.IntN(100)
	// Hard worker crash: abandon both discovery lease and TaskControl without releasing so
	// another worker must wait for TaskLease (30s) expiry and take over with Fence+1.
	if actionRoll < 8 {
		w.Stats.WorkerCrashes++
		w.Record(uw.User.ID, "worker", "crash_abandon", fmt.Sprintf("run=%s owner=%s fence=%d", runID, owner, fence.Fence))
		return
	}

	releaseNormal := func() {
		_ = uw.Store.ReleaseTaskControl(ctx, runID, fence)
		_ = w.Backend.ReleaseTask(ctx, uw.User.ID, runID, owner, claim.WakeSeq, w.Clock.Now())
	}
	parkWith := func(reason string) {
		_ = uw.Store.ReleaseTaskControl(ctx, runID, fence)
		parked, pErr := w.Backend.ParkTask(ctx, uw.User.ID, runID, owner, claim.WakeSeq, reason)
		if pErr != nil || !parked {
			_ = w.Backend.ReleaseTask(ctx, uw.User.ID, runID, owner, claim.WakeSeq, w.Clock.Now())
		}
	}
	deleteLease := func() {
		_ = uw.Store.ReleaseTaskControl(ctx, runID, fence)
		deleted, dErr := w.Backend.DeleteTaskLease(ctx, uw.User.ID, runID, claim.WakeSeq)
		if dErr != nil || !deleted {
			_ = w.Backend.ReleaseTask(ctx, uw.User.ID, runID, owner, claim.WakeSeq, w.Clock.Now())
		}
	}

	snap, err := uw.Store.Reconcile(ctx, runID)
	if err != nil {
		releaseNormal()
		w.Record(uw.User.ID, "worker", "reconcile_err", fmt.Sprintf("run=%s err=%v", runID, err))
		return
	}
	for _, cmd := range snap.Commands {
		if !cmd.Delivered {
			_ = uw.Store.Delivered(ctx, cmd.ID)
		}
	}
	run := snap.Run

	// If run already finished or is finishing, handle cleanup & lease deletion/park.
	if run.Status != state.RunRunning || run.Finishing != nil {
		detail := ""
		if w.RNG.IntN(4) == 0 {
			detail = "transient cleanup err"
		}
		err := uw.Store.FinishCleanup(ctx, run.ID, detail)
		if err == nil && detail == "" {
			if updated, gerr := uw.Store.GetRun(ctx, run.ID); gerr == nil {
				if updated.Status != state.RunRunning && !updated.CleanupPending {
					deleteLease()
					w.Record(uw.User.ID, "worker", "finish_cleanup_done", fmt.Sprintf("run=%s status=%s", run.ID, updated.Status))
					return
				}
				if updated.Finishing != nil && w.hasActiveOrUncleanChild(ctx, uw, updated) {
					parkWith(docstore.WaitFinishing)
					w.Record(uw.User.ID, "worker", "finish_wait_children", fmt.Sprintf("run=%s", run.ID))
					return
				}
			}
		}
		releaseNormal()
		w.Record(uw.User.ID, "worker", "finish_cleanup", fmt.Sprintf("run=%s status=%s finishing=%v detail=%q err=%v",
			run.ID, run.Status, run.Finishing != nil, detail, err))
		return
	}

	// If commands remain unincorporated, incorporate first (most of the time) or test Guard rejection.
	if run.Incorporated < state.TaskPosition(run) && w.RNG.IntN(10) < 8 {
		err := uw.Store.Incorporate(ctx, state.IncorporateRequest{
			RunID:    run.ID,
			Revision: run.Revision,
			Position: state.TaskPosition(run),
		})
		if err == nil {
			nextCtrl := ctrl
			nextCtrl.RunID = run.ID
			nextCtrl.Phase = state.TaskPhaseModel
			if run.Revision > nextCtrl.Revision {
				nextCtrl.Revision = run.Revision
			}
			if pos := state.TaskPosition(run); pos > nextCtrl.Position {
				nextCtrl.Position = pos
			}
			_, _ = uw.Store.SaveTaskControl(ctx, fence, nextCtrl)
			w.Record(uw.User.ID, "worker", "incorporate", fmt.Sprintf("run=%s rev=%d pos=%d",
				run.ID, run.Revision, state.TaskPosition(run)))
		} else {
			w.Record(uw.User.ID, "worker", "incorporate_err", fmt.Sprintf("run=%s err=%v", run.ID, err))
		}
		releaseNormal()
		return
	}

	switch {
	case actionRoll < 26:
		// Guard + Atomic AdvanceTaskStep (Spend + parent rollup + TaskControl cursor) + optional Renew & Artifact.
		_, gerr := uw.Store.Guard(ctx, run.ID, run.Revision)
		if gerr == nil {
			if w.RNG.IntN(3) == 0 {
				_, _ = w.Backend.RenewTaskLeases(ctx, owner, []docstore.TaskKey{{UserID: uw.User.ID, RunID: run.ID}}, state.TaskLease)
				_ = uw.Store.RenewTaskControl(ctx, run.ID, fence)
			}
			delta := 0.01 * float64(1+w.RNG.IntN(10))
			nextCtrl := ctrl
			nextCtrl.RunID = run.ID
			nextCtrl.Phase = state.TaskPhaseCalls
			nextCtrl.Models++
			if run.Revision > nextCtrl.Revision {
				nextCtrl.Revision = run.Revision
			}
			if run.Incorporated > nextCtrl.Position {
				nextCtrl.Position = run.Incorporated
			}
			_, _, serr := uw.Store.AdvanceTaskStep(ctx, fence, nextCtrl, &state.SpendUpdate{
				RunID:  run.ID,
				Spent:  run.Spent + delta,
				Budget: run.Budget,
			})
			if serr == nil && w.RNG.IntN(3) == 0 {
				artID := fmt.Sprintf("art_%d_%d", w.step, w.RNG.IntN(1000))
				if perr := uw.Store.PutPNG(ctx, run.ID, artID, []byte("PNG-SIM-"+artID)); perr == nil {
					_ = uw.Store.Artifact(ctx, run.ID, state.Artifact{ID: artID, Kind: "screenshot"})
				}
			}
			w.Record(uw.User.ID, "worker", "advance_spend", fmt.Sprintf("run=%s rev=%d spent=%.2f err=%v", run.ID, run.Revision, run.Spent+delta, serr))
		} else {
			w.Record(uw.User.ID, "worker", "guard_rejected", fmt.Sprintf("run=%s rev=%d err=%v", run.ID, run.Revision, gerr))
		}
		releaseNormal()

	case actionRoll < 46:
		// Operations: Admit / PutOperation / SettleOperation / duplicate settle attempt.
		w.stepWorkerOperation(ctx, uw, run)
		releaseNormal()

	case actionRoll < 60:
		// Project / SetProgress
		if w.RNG.IntN(2) == 0 {
			err := uw.Store.Project(ctx, state.ProjectRequest{
				RunID:    run.ID,
				Revision: run.Revision,
				Kind:     "status",
				Text:     fmt.Sprintf("working step %d", w.step),
			})
			w.Record(uw.User.ID, "worker", "project", fmt.Sprintf("run=%s rev=%d err=%v", run.ID, run.Revision, err))
		} else {
			err := uw.Store.SetProgress(ctx, run.ID, run.Revision, fmt.Sprintf("progress %d%%", w.RNG.IntN(100)))
			w.Record(uw.User.ID, "worker", "set_progress", fmt.Sprintf("run=%s rev=%d err=%v", run.ID, run.Revision, err))
		}
		releaseNormal()

	case actionRoll < 74:
		// ProjectQuestion (either user input question or work request for spawning subtasks) -> park WaitQuestion.
		qKind := "input"
		if run.ParentID == "" && w.RNG.IntN(2) == 0 {
			qKind = state.QuestionWork
		}
		qID := "q_" + w.IDs.Next()
		q := state.Question{
			ID:       qID,
			Kind:     qKind,
			Text:     fmt.Sprintf("question (%s) at step %d", qKind, w.step),
			Revision: run.Revision,
			ReadOnly: qKind == state.QuestionWork && w.RNG.IntN(2) == 0,
		}
		err := uw.Store.ProjectQuestion(ctx, run.ID, run.Revision, q)
		if err == nil {
			parkWith(docstore.WaitQuestion)
		} else {
			releaseNormal()
		}
		w.Record(uw.User.ID, "worker", "project_question", fmt.Sprintf("run=%s rev=%d kind=%s q=%s err=%v", run.ID, run.Revision, qKind, qID, err))

	case actionRoll < 94:
		// Publish outcome (completed / failed / cancelled), exercising held publication & finishing with subtasks.
		status := state.RunCompleted
		detail := ""
		text := fmt.Sprintf("completed at step %d", w.step)
		if run.CancelRequested {
			status = state.RunCancelled
			detail = "cancelled by worker"
			text = ""
		} else {
			r := w.RNG.IntN(10)
			if r == 0 {
				status = state.RunFailed
				detail = "simulated tool failure"
				text = ""
			} else if r == 1 {
				status = state.RunCancelled
				detail = "self-cancelled"
				text = ""
			}
		}
		bypass := w.RNG.IntN(3) == 0
		pub, err := uw.Store.Publish(ctx, state.PublishRequest{
			RunID:         run.ID,
			Revision:      run.Revision,
			Status:        status,
			Text:          text,
			Detail:        detail,
			Spent:         run.Spent,
			BypassRouting: bypass,
		})
		switch {
		case errors.Is(err, state.ErrRoutingPending):
			parkWith(docstore.WaitRoutingPending)
		case err == nil && pub.Status != state.RunRunning && !pub.CleanupPending:
			deleteLease()
		case err == nil && pub.Finishing != nil && w.hasActiveOrUncleanChild(ctx, uw, pub):
			parkWith(docstore.WaitFinishing)
		default:
			releaseNormal()
		}
		w.Record(uw.User.ID, "worker", "publish", fmt.Sprintf("run=%s req=%s bypass=%v got_status=%s finishing=%v cleanup=%v err=%v",
			run.ID, status, bypass, pub.Status, pub.Finishing != nil, pub.CleanupPending, err))

	default:
		// Adversarial FinishCleanup while running & Finishing==nil (must return ErrBusy).
		err := uw.Store.FinishCleanup(ctx, run.ID, "")
		releaseNormal()
		w.Record(uw.User.ID, "worker", "cleanup_while_running", fmt.Sprintf("run=%s err=%v", run.ID, err))
	}
}

func (w *World) stepWorkerOperation(ctx context.Context, uw *UserWorld, run state.Run) {
	// Find existing tracked ops for this run.
	var runOps []*trackedOp
	for _, op := range uw.ops {
		if op.runID == run.ID {
			runOps = append(runOps, op)
		}
	}

	// 1. Adversarial duplicate settlement attempt on an already-settled op.
	if len(runOps) > 0 && w.RNG.IntN(4) == 0 {
		op := runOps[w.RNG.IntN(len(runOps))]
		if op.settled {
			altOutcome := "failed"
			if op.outcome == "failed" {
				altOutcome = "confirmed"
			}
			altRef, herr := uw.Store.AppendHistory(ctx, run.ID, uw.historyHead[run.ID], []byte(fmt.Sprintf(`{"dup":%d}`, w.step)))
			if herr == nil {
				uw.historyHead[run.ID] = altRef
				err := uw.Store.SettleOperation(ctx, run.ID, state.OperationRecord{
					ID:          op.id,
					CallID:      op.callID,
					Name:        op.name,
					EffectClass: op.effect,
					Outcome:     altOutcome,
					ResultRef:   altRef,
					Revision:    op.revision,
					Attempt:     op.attempt,
					Action:      &state.Action{Digest: op.digest, Text: "action " + op.id, Spec: map[string]string{"op": op.id}},
				})
				w.Record(uw.User.ID, "worker", "op.dup_settle", fmt.Sprintf("run=%s op=%s want=%s err=%v", run.ID, op.id, altOutcome, err))
				return
			}
		}
	}

	// 2. Advance or settle an unsettled op for this run.
	for _, op := range runOps {
		if !op.settled {
			var nextOutcome string
			switch op.outcome {
			case "pending":
				if w.RNG.IntN(2) == 0 {
					nextOutcome = "ready"
				} else {
					nextOutcome = "attempted"
				}
			case "ready":
				nextOutcome = "attempted"
			case "attempted":
				switch w.RNG.IntN(3) {
				case 0:
					nextOutcome = "confirmed"
				case 1:
					nextOutcome = "failed"
				default:
					nextOutcome = "uncertain"
				}
			case "uncertain":
				if w.RNG.IntN(2) == 0 {
					nextOutcome = "confirmed"
				} else {
					nextOutcome = "failed"
				}
			default:
				continue
			}

			rec := state.OperationRecord{
				ID:          op.id,
				CallID:      op.callID,
				Name:        op.name,
				EffectClass: op.effect,
				Outcome:     nextOutcome,
				Revision:    op.revision,
				Attempt:     op.attempt,
				Action:      &state.Action{Digest: op.digest, Text: "action " + op.id, Spec: map[string]string{"op": op.id}},
			}

			if nextOutcome == "confirmed" || nextOutcome == "failed" || nextOutcome == "uncertain" {
				ref, herr := uw.Store.AppendHistory(ctx, run.ID, uw.historyHead[run.ID],
					[]byte(fmt.Sprintf(`{"op":%q,"outcome":%q,"step":%d}`, op.id, nextOutcome, w.step)))
				if herr != nil {
					w.Record(uw.User.ID, "worker", "op.history_err", fmt.Sprintf("run=%s op=%s err=%v", run.ID, op.id, herr))
					return
				}
				uw.historyHead[run.ID] = ref
				rec.ResultRef = ref
				err := uw.Store.SettleOperation(ctx, run.ID, rec)
				if err == nil || errors.Is(err, memstore.ErrCommitThenError) {
					op.outcome = nextOutcome
					op.resultRef = ref
					if nextOutcome == "confirmed" || nextOutcome == "failed" {
						op.settled = true
					}
				}
				w.Record(uw.User.ID, "worker", "op.settle", fmt.Sprintf("run=%s op=%s outcome=%s err=%v", run.ID, op.id, nextOutcome, err))
				return
			}

			err := uw.Store.PutOperation(ctx, run.ID, rec)
			if err == nil || errors.Is(err, memstore.ErrCommitThenError) {
				op.outcome = nextOutcome
			}
			w.Record(uw.User.ID, "worker", "op.put", fmt.Sprintf("run=%s op=%s outcome=%s err=%v", run.ID, op.id, nextOutcome, err))
			return
		}
	}

	// 3. Admit and record a brand-new operation on this run.
	uw.nextOpSeq++
	opID := fmt.Sprintf("op_%d_%d", w.step, uw.nextOpSeq)
	callID := fmt.Sprintf("call_%d_%d", w.step, uw.nextOpSeq)
	effects := []string{"read", "reversible_write", "sensitive_send"}
	effect := effects[w.RNG.IntN(len(effects))]
	digest := fmt.Sprintf("dig_%s", opID)

	if err := uw.Store.Admit(ctx, run.ID, run.Revision, opID, w.Clock.Now().Add(5*time.Minute)); err != nil {
		w.Record(uw.User.ID, "worker", "op.admit_err", fmt.Sprintf("run=%s op=%s err=%v", run.ID, opID, err))
		return
	}

	initialOutcome := "pending"
	if w.RNG.IntN(2) == 0 {
		initialOutcome = "attempted"
	}
	rec := state.OperationRecord{
		ID:          opID,
		CallID:      callID,
		Name:        "sim_tool_" + effect,
		EffectClass: effect,
		Outcome:     initialOutcome,
		Revision:    run.Revision,
		Attempt:     1,
		Action:      &state.Action{Digest: digest, Text: "action " + opID, Spec: map[string]string{"op": opID}},
	}
	err := uw.Store.PutOperation(ctx, run.ID, rec)
	if err == nil || errors.Is(err, memstore.ErrCommitThenError) {
		uw.ops = append(uw.ops, &trackedOp{
			runID:    run.ID,
			id:       opID,
			callID:   callID,
			name:     rec.Name,
			effect:   effect,
			revision: run.Revision,
			attempt:  1,
			digest:   digest,
			outcome:  initialOutcome,
		})
	}
	w.Record(uw.User.ID, "worker", "op.admit_put", fmt.Sprintf("run=%s op=%s outcome=%s err=%v", run.ID, opID, initialOutcome, err))
}

// -----------------------------------------------------------------------------
// 3. Permissions & Interactions
// -----------------------------------------------------------------------------

func (w *World) stepPermissions(ctx context.Context, uw *UserWorld) {
	roll := w.RNG.IntN(100)

	switch {
	case roll < 32:
		// Open an approval or question interaction on an active, guarded running task.
		open, err := uw.Store.OpenTasks(ctx)
		if err != nil || len(open) == 0 {
			w.Record(uw.User.ID, "perms", "open.skip", fmt.Sprintf("open=%d err=%v", len(open), err))
			return
		}
		var eligible []state.Run
		for _, r := range open {
			if r.Status == state.RunRunning && !r.CancelRequested && r.Finishing == nil {
				eligible = append(eligible, r)
			}
		}
		if len(eligible) == 0 {
			w.Record(uw.User.ID, "perms", "open.none_eligible", "")
			return
		}
		target := eligible[w.RNG.IntN(len(eligible))]
		// Mirror real Nori: guard before opening a task-bound interaction so revision is current.
		if target.Incorporated < state.TaskPosition(target) {
			if err := uw.Store.Incorporate(ctx, state.IncorporateRequest{
				RunID: target.ID, Revision: target.Revision, Position: state.TaskPosition(target),
			}); err != nil {
				w.Record(uw.User.ID, "perms", "open.inc_err", fmt.Sprintf("run=%s err=%v", target.ID, err))
				return
			}
		}
		if _, err := uw.Store.Guard(ctx, target.ID, target.Revision); err != nil {
			w.Record(uw.User.ID, "perms", "open.guard_err", fmt.Sprintf("run=%s err=%v", target.ID, err))
			return
		}

		interID := "int_" + w.IDs.Next()
		kind := "approval"
		digest := "act_" + w.IDs.Next()
		if w.RNG.IntN(3) == 0 {
			kind = "question"
			digest = ""
		}
		rev := target.Revision
		inter := permissions.Interaction{
			ID:                  interID,
			TaskID:              target.ID,
			InstructionRevision: &rev,
			Kind:                kind,
			Prompt:              fmt.Sprintf("confirm %s on %s at step %d", kind, target.ID, w.step),
			Payload:             json.RawMessage(`{"tool":"sim_send"}`),
			ActionDigest:        digest,
		}
		if w.RNG.IntN(2) == 0 {
			exp := w.Clock.Now().Add(time.Duration(30+w.RNG.IntN(600)) * time.Second)
			inter.ExpiresAt = &exp
		}
		got, err := uw.Permissions.Open(ctx, inter, "sim_purpose")
		if err == nil || errors.Is(err, memstore.ErrInjectedFault) {
			uw.interactions = append(uw.interactions, &trackedInteraction{
				id:           interID,
				taskID:       target.ID,
				kind:         kind,
				actionDigest: digest,
			})
			if len(uw.interactions) > 64 {
				uw.interactions = uw.interactions[len(uw.interactions)-64:]
			}
		}
		w.Record(uw.User.ID, "perms", "open", fmt.Sprintf("id=%s run=%s rev=%d kind=%s status=%s err=%v", interID, target.ID, rev, kind, got.Status, err))

	case roll < 68 && len(uw.interactions) > 0:
		// Respond to an interaction (valid, stale revision, wrong digest, expired, or exact retry).
		ti := uw.interactions[w.RNG.IntN(len(uw.interactions))]

		// Retry an earlier response with identical request_id.
		if ti.hasResponse && w.RNG.IntN(4) == 0 {
			_, ans, err := uw.Permissions.Respond(ctx, ti.id, ti.lastRequestID, 1, ti.lastResponse)
			w.Record(uw.User.ID, "perms", "respond.retry", fmt.Sprintf("id=%s req=%s ans=%v err=%v", ti.id, ti.lastRequestID, ans != nil, err))
			return
		}

		cur, err := uw.Permissions.Interaction(ctx, ti.id)
		if err != nil {
			w.Record(uw.User.ID, "perms", "respond.read_err", fmt.Sprintf("id=%s err=%v", ti.id, err))
			return
		}

		expectedRev := cur.Revision
		resp := permissions.Response{}
		switch ti.kind {
		case "approval":
			if w.RNG.IntN(2) == 0 {
				resp.Kind = "approve"
			} else {
				resp.Kind = "decline"
			}
			resp.ActionDigest = ti.actionDigest
			if w.RNG.IntN(5) == 0 {
				resp.ActionDigest = "wrong_digest"
			}
		default:
			resp.Kind = "answer"
			resp.Text = fmt.Sprintf("interaction answer step %d", w.step)
		}

		// Occasionally send a stale expected revision.
		if w.RNG.IntN(5) == 0 {
			expectedRev = cur.Revision + 1
		}

		reqID := w.IDs.Next()
		_, ans, err := uw.Permissions.Respond(ctx, ti.id, reqID, expectedRev, resp)
		if err == nil || errors.Is(err, memstore.ErrCommitThenError) {
			ti.lastRequestID = reqID
			ti.lastResponse = resp
			ti.hasResponse = true
		}
		if err == nil && ans != nil && w.RNG.IntN(2) == 0 {
			w.deliverSingleAnswer(ctx, uw, ti.id, *ans)
		}
		w.Record(uw.User.ID, "perms", "respond", fmt.Sprintf("id=%s kind=%s exp=%d cur_status=%s ans=%v err=%v",
			ti.id, resp.Kind, expectedRev, cur.Status, ans != nil, err))

	case roll < 82:
		// SaveRule (create or update) or RevokeRule.
		if len(uw.ruleIDs) > 0 && w.RNG.IntN(3) == 0 {
			id := uw.ruleIDs[w.RNG.IntN(len(uw.ruleIDs))]
			if rule, err := uw.Permissions.Rule(ctx, id); err == nil {
				if w.RNG.IntN(2) == 0 {
					_, rerr := uw.Permissions.RevokeRule(ctx, id, w.IDs.Next(), rule.Revision)
					w.Record(uw.User.ID, "perms", "rule.revoke", fmt.Sprintf("id=%s rev=%d err=%v", id, rule.Revision, rerr))
				} else {
					_, uerr := uw.Permissions.SaveRule(ctx, id, w.IDs.Next(), rule.Revision, w.randomRuleEditable())
					w.Record(uw.User.ID, "perms", "rule.update", fmt.Sprintf("id=%s rev=%d err=%v", id, rule.Revision, uerr))
				}
				return
			}
		}
		reqID := w.IDs.Next()
		receipt, err := uw.Permissions.SaveRule(ctx, "", reqID, 0, w.randomRuleEditable())
		if err == nil {
			var saved permissions.Rule
			if json.Unmarshal(receipt.Resource, &saved) == nil && saved.ID != "" {
				uw.ruleIDs = append(uw.ruleIDs, saved.ID)
			}
		}
		w.Record(uw.User.ID, "perms", "rule.create", fmt.Sprintf("req=%s err=%v", reqID, err))

	case roll < 92:
		// Permissions.Check against current rules & approvals.
		origins := []string{"user", "automation"}
		effects := []string{"read", "reversible_write", "sensitive_send", "unknown"}
		req := permissions.Request{
			Origin:       origins[w.RNG.IntN(len(origins))],
			ToolID:       "sim_tool",
			Effect:       effects[w.RNG.IntN(len(effects))],
			ConnectionID: "conn_1",
		}
		if len(uw.interactions) > 0 && w.RNG.IntN(2) == 0 {
			ti := uw.interactions[w.RNG.IntN(len(uw.interactions))]
			req.RunID = ti.taskID
			req.Revision = 1
			req.Digest = ti.actionDigest
		}
		dec, err := uw.Permissions.Check(ctx, req)
		w.Record(uw.User.ID, "perms", "check", fmt.Sprintf("origin=%s effect=%s result=%s err=%v", req.Origin, req.Effect, dec.Result, err))

	default:
		// Retry undelivered interaction answers (mirrors app.deliverAnswers).
		err := w.deliverUndeliveredAnswers(ctx, uw)
		w.Record(uw.User.ID, "perms", "deliver_answers", fmt.Sprintf("err=%v", err))
	}
}

func (w *World) randomRuleEditable() permissions.RuleEditable {
	effect := "allow"
	if w.RNG.IntN(3) == 0 {
		effect = "deny"
	}
	classes := [][]string{
		{"read"},
		{"reversible_write"},
		{"sensitive_send"},
		{"read", "reversible_write"},
	}
	rule := permissions.RuleEditable{
		Effect:        effect,
		ToolIDs:       []string{permissions.Wildcard},
		ConnectionIDs: []string{permissions.Wildcard},
		ActionClasses: classes[w.RNG.IntN(len(classes))],
		Title:         fmt.Sprintf("sim rule %d", w.step),
	}
	if effect == "allow" && w.RNG.IntN(2) == 0 {
		rule.MaxOperations = 1 + w.RNG.IntN(4)
	}
	if w.RNG.IntN(2) == 0 {
		exp := w.Clock.Now().Add(time.Duration(60+w.RNG.IntN(1800)) * time.Second)
		rule.ExpiresAt = &exp
	}
	return rule
}

func (w *World) deliverSingleAnswer(ctx context.Context, uw *UserWorld, interID string, ans permissions.Answer) {
	receipts, err := uw.Store.AcceptCommandBatch(ctx, []state.Command{{
		ID:         ans.CommandID,
		RunID:      ans.TaskID,
		Kind:       state.CommandAnswer,
		QuestionID: ans.Question,
		Text:       ans.Text,
	}}, nil)
	if err == nil {
		_ = w.Checker.RecordCommandReceipts(uw.User.ID, receipts)
		uw.Permissions.MarkDelivered(ctx, interID)
	}
}

func (w *World) deliverUndeliveredAnswers(ctx context.Context, uw *UserWorld) error {
	answers, ids, err := uw.Permissions.UndeliveredAnswers(ctx)
	if err != nil {
		return err
	}
	for i, answer := range answers {
		run, runErr := uw.Store.GetRun(ctx, answer.TaskID)
		waiting := runErr == nil && run.Status == state.RunRunning && run.Question != nil && run.Question.ID == answer.Question && run.Question.ResolvedBy == ""
		if answer.Recheck && !waiting {
			continue
		}
		receipts, err := uw.Store.AcceptCommandBatch(ctx, []state.Command{{
			ID:         answer.CommandID,
			RunID:      answer.TaskID,
			Kind:       state.CommandAnswer,
			QuestionID: answer.Question,
			Text:       answer.Text,
		}}, nil)
		if err == nil {
			_ = w.Checker.RecordCommandReceipts(uw.User.ID, receipts)
		}
		if errors.Is(err, state.ErrQuestionStale) || errors.Is(err, state.ErrBusy) {
			if current, e := uw.Store.GetRun(ctx, answer.TaskID); e == nil && current.Status == state.RunRunning && (current.Question == nil || current.Question.ID == answer.Question) {
				continue
			}
		}
		if (err == nil || errors.Is(err, state.ErrDuplicate) || errors.Is(err, state.ErrQuestionStale) || errors.Is(err, state.ErrBusy)) && !answer.Recheck {
			uw.Permissions.MarkDelivered(ctx, ids[i])
		}
	}
	return nil
}

// -----------------------------------------------------------------------------
// 4. Scheduler (Automations, Goals, Tick)
// -----------------------------------------------------------------------------

func (w *World) stepScheduler(ctx context.Context, uw *UserWorld) {
	roll := w.RNG.IntN(100)

	switch {
	case roll < 30 && len(uw.automationIDs) < 6:
		// Create a new automation (once, recurring, or watch).
		reqID := w.IDs.Next()
		editable := w.randomAutomationEditable(uw)
		receipt, err := uw.Scheduler.Save(ctx, "", reqID, 0, editable)
		if err == nil {
			var a scheduler.Automation
			if json.Unmarshal(receipt.Resource, &a) == nil && a.ID != "" {
				uw.automationIDs = append(uw.automationIDs, a.ID)
			}
		} else if errors.Is(err, memstore.ErrCommitThenError) {
			if a, cerr := uw.Scheduler.Created(ctx, reqID); cerr == nil && a.ID != "" {
				uw.automationIDs = append(uw.automationIDs, a.ID)
			}
		}
		w.Record(uw.User.ID, "sched", "auto.create", fmt.Sprintf("kind=%s missed=%s err=%v", editable.Trigger.Kind, editable.MissedRun, err))

	case roll < 45 && len(uw.automationIDs) > 0:
		// Edit an existing automation (advances generation!).
		id := uw.automationIDs[w.RNG.IntN(len(uw.automationIDs))]
		cur, err := uw.Scheduler.Get(ctx, id)
		if err != nil {
			w.Record(uw.User.ID, "sched", "auto.edit_read_err", fmt.Sprintf("id=%s err=%v", id, err))
			return
		}
		editable := w.randomAutomationEditable(uw)
		_, err = uw.Scheduler.Save(ctx, id, w.IDs.Next(), cur.Revision, editable)
		w.Record(uw.User.ID, "sched", "auto.edit", fmt.Sprintf("id=%s rev=%d err=%v", id, cur.Revision, err))

	case roll < 65 && len(uw.automationIDs) > 0:
		// Act on an automation: pause, resume, snooze, run_now, cancel.
		id := uw.automationIDs[w.RNG.IntN(len(uw.automationIDs))]
		cur, err := uw.Scheduler.Get(ctx, id)
		if err != nil {
			w.Record(uw.User.ID, "sched", "auto.act_read_err", fmt.Sprintf("id=%s err=%v", id, err))
			return
		}
		var act scheduler.Action
		switch {
		case cur.Status == "paused":
			if w.RNG.IntN(4) == 0 {
				act = scheduler.Action{Action: "cancel"}
			} else {
				act = scheduler.Action{Action: "resume"}
			}
		case cur.Status == "enabled":
			switch w.RNG.IntN(5) {
			case 0:
				act = scheduler.Action{Action: "pause"}
			case 1:
				until := w.Clock.Now().Add(time.Duration(120+w.RNG.IntN(600)) * time.Second)
				act = scheduler.Action{Action: "snooze", Until: &until}
			case 2, 3:
				act = scheduler.Action{Action: "run_now"}
			default:
				act = scheduler.Action{Action: "cancel"}
			}
		default:
			act = scheduler.Action{Action: "run_now"} // adversarial act on cancelled
		}
		_, err = uw.Scheduler.Act(ctx, id, w.IDs.Next(), cur.Revision, act)
		w.Record(uw.User.ID, "sched", "auto.act", fmt.Sprintf("id=%s status=%s action=%s err=%v", id, cur.Status, act.Action, err))

	case roll < 80:
		// Goals: create, act, or record progress.
		w.stepGoal(ctx, uw)

	default:
		// Tick scheduler!
		next, err := uw.Scheduler.Tick(ctx)
		nextStr := "<nil>"
		if next != nil {
			nextStr = next.UTC().Format(time.RFC3339)
		}
		w.Record(uw.User.ID, "sched", "tick", fmt.Sprintf("next=%s err=%v", nextStr, err))
	}
}

func (w *World) randomAutomationEditable(uw *UserWorld) scheduler.Editable {
	missed := []string{"skip", "latest", "all"}
	now := w.Clock.Now()
	var trig scheduler.Trigger
	switch w.RNG.IntN(3) {
	case 0:
		at := now.Add(time.Duration(60+w.RNG.IntN(900)) * time.Second).Truncate(time.Second)
		trig = scheduler.Trigger{Kind: "once", At: &at}
	case 1:
		loc, _ := time.LoadLocation(uw.User.TimeZone)
		localNow := now.In(loc).Truncate(time.Minute)
		trig = scheduler.Trigger{
			Kind:       "recurring",
			LocalStart: localNow.Format("2006-01-02T15:04:05"),
			Timezone:   uw.User.TimeZone,
			RRule:      "FREQ=DAILY;INTERVAL=1;BYHOUR=0,3,6,9,12,15,18,21;BYMINUTE=0,30",
		}
	default:
		intervals := []int64{300, 600, 1200, 1800}
		trig = scheduler.Trigger{
			Kind: "watch",
			Source: &scheduler.ResourceRef{
				ConnectionID: "conn_1",
				Kind:         "mail",
				ProviderID:   "inbox",
			},
			PredicateRef:       "unread_important",
			MinIntervalSeconds: intervals[w.RNG.IntN(len(intervals))],
		}
	}

	ed := scheduler.Editable{
		Title:                  fmt.Sprintf("Sim Auto %d", w.step),
		Instruction:            fmt.Sprintf("Run check at step %d", w.step),
		Trigger:                trig,
		BudgetUSD:              0.5,
		MaxActiveRuns:          1 + w.RNG.IntN(2),
		MissedRun:              missed[w.RNG.IntN(len(missed))],
		ExecutionWindowSeconds: []int64{0, 300, 1800}[w.RNG.IntN(3)],
		DestinationIDs:         []string{scheduler.InAppDestination},
	}
	if w.RNG.IntN(2) == 0 {
		ed.MaxOccurrences = 2 + w.RNG.IntN(4)
	}
	if w.RNG.IntN(2) == 0 {
		ed.RequestedAuthority = &scheduler.RequestedAuthority{
			ActionClasses: []string{"read", "reversible_write"},
			ToolIDs:       []string{permissions.Wildcard},
		}
	}
	if len(uw.goalIDs) > 0 && w.RNG.IntN(3) == 0 {
		ed.GoalID = uw.goalIDs[w.RNG.IntN(len(uw.goalIDs))]
	}
	return ed
}

func (w *World) stepGoal(ctx context.Context, uw *UserWorld) {
	if len(uw.goalIDs) == 0 || (len(uw.goalIDs) < 4 && w.RNG.IntN(2) == 0) {
		ge := scheduler.GoalEditable{
			Title:    fmt.Sprintf("Sim Goal %d", w.step),
			Outcome:  "Achieve verified state",
			FollowUp: []string{"none", "remind", "monitor"}[w.RNG.IntN(3)],
			CompletionCriteria: []struct {
				ID   string `json:"id,omitempty"`
				Text string `json:"text"`
			}{{ID: "c1", Text: "criterion 1"}},
		}
		receipt, err := uw.Scheduler.SaveGoal(ctx, "", w.IDs.Next(), 0, ge)
		if err == nil {
			var g scheduler.Goal
			if json.Unmarshal(receipt.Resource, &g) == nil && g.ID != "" {
				uw.goalIDs = append(uw.goalIDs, g.ID)
			}
		}
		w.Record(uw.User.ID, "sched", "goal.create", fmt.Sprintf("err=%v", err))
		return
	}

	id := uw.goalIDs[w.RNG.IntN(len(uw.goalIDs))]
	g, err := uw.Scheduler.Goal(ctx, id)
	if err != nil {
		w.Record(uw.User.ID, "sched", "goal.read_err", fmt.Sprintf("id=%s err=%v", id, err))
		return
	}
	if g.Status == "active" && w.RNG.IntN(2) == 0 {
		_, err = uw.Scheduler.RecordProgress(ctx, id, w.IDs.Next(), scheduler.GoalProgress{
			Progress:    fmt.Sprintf("progress step %d", w.step),
			NextActions: []string{"next step"},
		})
		w.Record(uw.User.ID, "sched", "goal.progress", fmt.Sprintf("id=%s err=%v", id, err))
		return
	}

	actName := "pause"
	switch g.Status {
	case "active":
		actName = []string{"pause", "complete", "abandon"}[w.RNG.IntN(3)]
	case "paused":
		actName = []string{"resume", "complete", "abandon"}[w.RNG.IntN(3)]
	}
	_, err = uw.Scheduler.ActGoal(ctx, id, w.IDs.Next(), g.Revision, scheduler.GoalAction{
		Action:       actName,
		KeepMonitors: w.RNG.IntN(2) == 0,
	})
	w.Record(uw.User.ID, "sched", "goal.act", fmt.Sprintf("id=%s status=%s act=%s err=%v", id, g.Status, actName, err))
}

// -----------------------------------------------------------------------------
// 5. Conversation Inputs, Turns, and ReadMarks
// -----------------------------------------------------------------------------

func (w *World) stepConversation(ctx context.Context, uw *UserWorld) {
	roll := w.RNG.IntN(100)

	switch {
	case roll < 35:
		// AcceptConversationInput (sets UserRoutingPending=true!) or replay existing input.
		if len(uw.inputs) > 0 && w.RNG.IntN(4) == 0 {
			in := uw.inputs[w.RNG.IntN(len(uw.inputs))]
			err := uw.Store.AcceptConversationInput(ctx, in.id, in.text, nil)
			w.Record(uw.User.ID, "conv", "input.replay", fmt.Sprintf("id=%s err=%v", in.id, err))
			return
		}
		id := w.IDs.Next()
		text := fmt.Sprintf("user message at step %d", w.step)
		err := uw.Store.AcceptConversationInput(ctx, id, text, nil)
		if err == nil || errors.Is(err, memstore.ErrInjectedFault) {
			uw.inputs = append(uw.inputs, trackedInput{id: id, text: text})
			if len(uw.inputs) > 32 {
				uw.inputs = uw.inputs[len(uw.inputs)-32:]
			}
		}
		w.Record(uw.User.ID, "conv", "input.accept", fmt.Sprintf("id=%s err=%v", id, err))

	case roll < 75:
		// Step a conversation turn (Acquire -> Renew/Restart/Release/Publish).
		if uw.activeConvFence == nil {
			owner := "sim-outer-" + w.IDs.Next()[:8]
			turnID := w.IDs.Next()
			ctrl, turn, err := uw.Store.AcquireConversation(ctx, owner, turnID)
			if err != nil || turn == nil {
				w.Record(uw.User.ID, "conv", "turn.acquire", fmt.Sprintf("turn=%v err=%v", turn != nil, err))
				return
			}
			f := state.ConversationFence{
				Owner:       ctrl.Owner,
				Fence:       ctrl.Fence,
				TurnID:      turn.ID,
				Through:     turn.Through,
				UserThrough: turn.UserThrough,
				Frozen:      turn.Frozen,
			}
			uw.activeConvFence = &f
			uw.activeConvTurn = turn
			w.Record(uw.User.ID, "conv", "turn.acquired", fmt.Sprintf("turn=%s through=%d user_through=%d fence=%d",
				turn.ID, turn.Through, turn.UserThrough, ctrl.Fence))
			return
		}

		f := *uw.activeConvFence
		turn := *uw.activeConvTurn
		sub := w.RNG.IntN(100)
		switch {
		case sub < 20:
			stale, err := uw.Store.RenewConversation(ctx, f)
			if err != nil && !errors.Is(err, memstore.ErrInjectedFault) {
				uw.activeConvFence, uw.activeConvTurn = nil, nil
			}
			w.Record(uw.User.ID, "conv", "turn.renew", fmt.Sprintf("turn=%s stale=%v err=%v", turn.ID, stale, err))
		case sub < 35:
			err := uw.Store.ReleaseConversation(ctx, f)
			if err == nil || !errors.Is(err, memstore.ErrInjectedFault) {
				uw.activeConvFence, uw.activeConvTurn = nil, nil
			}
			w.Record(uw.User.ID, "conv", "turn.release", fmt.Sprintf("turn=%s err=%v", turn.ID, err))
		case sub < 50:
			updated, wait, err := uw.Store.RestartConversation(ctx, f, turn)
			if err == nil {
				uw.activeConvTurn = &updated
				f.Through = updated.Through
				f.UserThrough = updated.UserThrough
				f.Frozen = updated.Frozen
				uw.activeConvFence = &f
			} else if !errors.Is(err, memstore.ErrInjectedFault) {
				uw.activeConvFence, uw.activeConvTurn = nil, nil
			}
			w.Record(uw.User.ID, "conv", "turn.restart", fmt.Sprintf("turn=%s wait=%s err=%v", turn.ID, wait, err))
		default:
			populateAttempt(&turn, w.Clock.Now())
			err := uw.Store.PublishConversation(ctx, f, turn, fmt.Sprintf("assistant reply step %d", w.step))
			if err == nil {
				uw.assistantMsgIDs = append(uw.assistantMsgIDs, state.AssistantMessageID(turn.ID))
				uw.activeConvFence, uw.activeConvTurn = nil, nil
			} else if errors.Is(err, state.ErrConversationBusy) {
				uw.activeConvFence, uw.activeConvTurn = nil, nil
			}
			w.Record(uw.User.ID, "conv", "turn.publish", fmt.Sprintf("turn=%s err=%v", turn.ID, err))
		}

	case roll < 90:
		// Exercise Coupled Memory mutations and FTS MemoryIndex search under store faults.
		w.stepMemory(ctx, uw)

	default:
		// MarkSeen advancing read mark forward (or testing older/non-assistant rejection).
		if len(uw.assistantMsgIDs) > 0 {
			msgID := uw.assistantMsgIDs[w.RNG.IntN(len(uw.assistantMsgIDs))]
			err := uw.Store.MarkSeen(ctx, msgID)
			w.Record(uw.User.ID, "conv", "mark_seen", fmt.Sprintf("msg=%s err=%v", msgID, err))
			return
		}
		if len(uw.inputs) > 0 {
			// Adversarial: MarkSeen on a user message must fail with ErrCommandInvalid.
			msgID := state.UserMessageID(uw.inputs[w.RNG.IntN(len(uw.inputs))].id)
			err := uw.Store.MarkSeen(ctx, msgID)
			w.Record(uw.User.ID, "conv", "mark_seen_user_msg", fmt.Sprintf("msg=%s err=%v", msgID, err))
			return
		}
		w.Record(uw.User.ID, "conv", "mark_seen.skip", "no messages")
	}
}

func populateAttempt(turn *state.ConversationTurn, now time.Time) {
	if turn.Attempt.ID == "" {
		turn.Attempt.ID = fmt.Sprintf("%s-att-%d", turn.ID, turn.Restarts+1)
	}
	turn.Attempt.TurnID = turn.ID
	turn.Attempt.Number = turn.Restarts + 1
	turn.Attempt.Status = "completed"
	if turn.Attempt.StartedAt.IsZero() {
		turn.Attempt.StartedAt = turn.StartedAt
	}
	fin := now.UTC()
	turn.Attempt.FinishedAt = &fin
}

func (w *World) stepMemory(ctx context.Context, uw *UserWorld) {
	formatRes := func(res state.MemoryMutationResult) (json.RawMessage, error) {
		return json.Marshal(res)
	}
	switch w.RNG.IntN(5) {
	case 0:
		var pinned *bool
		if w.RNG.IntN(4) == 0 {
			p := true
			pinned = &p
		}
		_, err := uw.Store.MutateMemory(ctx, state.MemoryMutationRequest{
			RequestID:      w.IDs.Next(),
			Kind:           state.MemoryMutateCreateCore,
			Text:           fmt.Sprintf("coord memory step=%d topic=coord_alpha_%d", w.step, w.RNG.IntN(12)),
			About:          "the user",
			Section:        "Context",
			Pinned:         pinned,
			FormatResource: formatRes,
		})
		w.Record(uw.User.ID, "mem", "create", fmt.Sprintf("err=%v", err))
	case 1, 2:
		mems, err := uw.Store.Memories(ctx)
		if err != nil || len(mems) == 0 {
			w.Record(uw.User.ID, "mem", "list_empty", fmt.Sprintf("err=%v", err))
			return
		}
		m := mems[w.RNG.IntN(len(mems))]
		req := state.MemoryMutationRequest{
			RequestID:        w.IDs.Next(),
			ExpectedRevision: m.UpdatedAt.UnixMilli(),
			TargetID:         m.ID,
			FormatResource:   formatRes,
		}
		switch w.RNG.IntN(4) {
		case 0:
			req.Kind = state.MemoryMutateEditCore
			req.Text = fmt.Sprintf("coord edited step=%d topic=coord_alpha", w.step)
			req.About = "the user"
			req.Section = m.Section
		case 1:
			if m.Pinned {
				req.Kind = state.MemoryMutateUnpinCore
			} else {
				req.Kind = state.MemoryMutatePinCore
			}
		case 2:
			req.Kind = state.MemoryMutateDemoteCore
		default:
			req.Kind = state.MemoryMutateForgetCore
		}
		_, merr := uw.Store.MutateMemory(ctx, req)
		w.Record(uw.User.ID, "mem", req.Kind, fmt.Sprintf("id=%s err=%v", m.ID, merr))
	default:
		from, _ := uw.Store.ContextFrom(ctx)
		hits, err := uw.Store.SearchMemory(ctx, docstore.MemorySearchQuery{
			Query:        "coord_alpha",
			CreatedAfter: from,
			Limit:        8,
			Now:          w.Clock.Now().UTC(),
		})
		w.Record(uw.User.ID, "mem", "search", fmt.Sprintf("hits=%d err=%v", len(hits), err))
	}
}

// -----------------------------------------------------------------------------
// 6. Lease Repair & User Wake Lanes (Postgres-native discovery)
// -----------------------------------------------------------------------------

func (w *World) stepReconcileAndWakes(ctx context.Context, uw *UserWorld) {
	roll := w.RNG.IntN(100)

	switch {
	case roll < 45:
		w.Stats.RepairPasses++
		err := w.repairUserLeases(ctx, uw)
		w.Record(uw.User.ID, "repair", "leases", fmt.Sprintf("err=%v", err))

	case roll < 72:
		// Claim and handle one due conversation user wake (mirroring worker.Pool.handleConversation).
		owner := w.nextWorkerOwner("conv-lane")
		claim, ok, err := w.Backend.ClaimUserWake(ctx, docstore.LaneConversation, owner, state.ConversationLease)
		if err != nil || !ok {
			w.Record(uw.User.ID, "wake", "claim_conv", fmt.Sprintf("ok=%v err=%v", ok, err))
			return
		}
		w.Stats.UserWakesClaimed++
		target := w.User(claim.UserID)
		if target == nil {
			_, _ = w.Backend.ParkUserWake(ctx, claim.UserID, docstore.LaneConversation, owner, claim.WakeSeq)
			return
		}
		conv, _, statusErr := target.Store.ConversationStatus(ctx)
		if statusErr == nil && !conv.Pending() && conv.Active == "" {
			parked, pErr := w.Backend.ParkUserWake(ctx, claim.UserID, docstore.LaneConversation, owner, claim.WakeSeq)
			if pErr != nil || !parked {
				_ = w.Backend.ReleaseUserWake(ctx, claim.UserID, docstore.LaneConversation, owner, 0, time.Time{})
			}
			w.Record(claim.UserID, "wake", "conv_park", fmt.Sprintf("parked=%v err=%v", parked, pErr))
			return
		}
		_ = w.Backend.ReleaseUserWake(ctx, claim.UserID, docstore.LaneConversation, owner, claim.WakeSeq, w.Clock.Now().Add(2*time.Second))
		w.Record(claim.UserID, "wake", "conv_release", fmt.Sprintf("err=%v", statusErr))

	default:
		// Claim and handle one due scheduler user wake (mirroring worker.Pool.handleScheduler).
		owner := w.nextWorkerOwner("sched-lane")
		claim, ok, err := w.Backend.ClaimUserWake(ctx, docstore.LaneScheduler, owner, state.ConversationLease)
		if err != nil || !ok {
			w.Record(uw.User.ID, "wake", "claim_sched", fmt.Sprintf("ok=%v err=%v", ok, err))
			return
		}
		w.Stats.UserWakesClaimed++
		target := w.User(claim.UserID)
		if target == nil {
			_, _ = w.Backend.ParkUserWake(ctx, claim.UserID, docstore.LaneScheduler, owner, claim.WakeSeq)
			return
		}
		_, tickErr := target.Scheduler.Tick(ctx)
		_ = w.deliverUndeliveredAnswers(ctx, target)
		if tickErr != nil {
			_ = w.Backend.ReleaseUserWake(ctx, claim.UserID, docstore.LaneScheduler, owner, claim.WakeSeq, w.Clock.Now().Add(2*time.Second))
			w.Record(claim.UserID, "wake", "sched_retry", fmt.Sprintf("err=%v", tickErr))
			return
		}
		nextDue := target.Scheduler.NextScheduled(ctx)
		if nextDue == nil {
			parked, pErr := w.Backend.ParkUserWake(ctx, claim.UserID, docstore.LaneScheduler, owner, claim.WakeSeq)
			if pErr != nil || !parked {
				_ = w.Backend.ReleaseUserWake(ctx, claim.UserID, docstore.LaneScheduler, owner, 0, time.Time{})
			}
			w.Record(claim.UserID, "wake", "sched_park", fmt.Sprintf("parked=%v err=%v", parked, pErr))
			return
		}
		_ = w.Backend.ReleaseUserWake(ctx, claim.UserID, docstore.LaneScheduler, owner, claim.WakeSeq, *nextDue)
		w.Record(claim.UserID, "wake", "sched_rearm", fmt.Sprintf("next=%s", nextDue.UTC().Format(time.RFC3339)))
	}
}

// repairUserLeases mirrors app.App.RepairLeases(ctx): reconciles nori_task_leases and
// nori_user_wakes against authoritative nori_records truth, including task.StopPaused while paused.
func (w *World) repairUserLeases(ctx context.Context, uw *UserWorld) error {
	var failures []error
	control, err := uw.Store.Control(ctx)
	if err != nil {
		return err
	}
	openRuns, err := uw.Store.OpenTasks(ctx)
	if err != nil {
		failures = append(failures, err)
	}
	cleanupRuns, err := uw.Store.CleanupTasks(ctx)
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
	runIDs := make([]string, 0, len(needLease))
	for id := range needLease {
		runIDs = append(runIDs, id)
	}
	slices.Sort(runIDs)

	conv, _, convErr := uw.Store.ConversationStatus(ctx)
	if convErr != nil {
		failures = append(failures, convErr)
	}

	existingLeases := map[string]docstore.TaskLease{}
	list, err := w.Backend.ListTaskLeases(ctx, uw.User.ID)
	if err != nil {
		failures = append(failures, err)
	} else {
		for _, l := range list {
			existingLeases[l.RunID] = l
		}
	}

	for _, runID := range runIDs {
		run := needLease[runID]
		if control.Paused && (run.CancelRequested || run.Finishing != nil || (run.Status != state.RunRunning && run.CleanupPending)) {
			stopped, err := task.StopPaused(ctx, uw.Store, runID)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if stopped.Status != state.RunRunning && !stopped.CleanupPending {
				if _, err := w.Backend.DeleteTaskLease(ctx, uw.User.ID, runID, 0); err != nil {
					failures = append(failures, err)
				}
				continue
			}
		}
		lease, exists := existingLeases[runID]
		if !exists {
			if _, err := w.Backend.EnsureTaskLease(ctx, uw.User.ID, runID, time.Time{}); err != nil {
				failures = append(failures, err)
			}
			continue
		}
		if !lease.Runnable && !control.Paused {
			shouldWake := false
			switch lease.WaitingReason {
			case docstore.WaitPaused:
				shouldWake = true
			case docstore.WaitRoutingPending:
				if convErr == nil && !conv.RoutingPending() {
					shouldWake = true
				}
			case docstore.WaitQuestion:
				waitingOnSubtasks := run.Question != nil && run.Question.Kind == state.QuestionWork && len(state.RunningSubtasks(run)) > 0
				if run.Question == nil || run.CancelRequested || (run.Question.ResolvedBy != "" && !waitingOnSubtasks) {
					shouldWake = true
				}
			case docstore.WaitFinishing:
				if !hasActiveOrUncleanChildMap(run, needLease) {
					shouldWake = true
				}
			default:
				shouldWake = true
			}
			if shouldWake {
				if err := w.Backend.WakeTask(ctx, uw.User.ID, runID, time.Time{}); err != nil {
					failures = append(failures, err)
				}
			}
		}
	}

	// Delete orphaned leases whose runs are missing or terminal & clean.
	leaseRunIDs := make([]string, 0, len(existingLeases))
	for id := range existingLeases {
		leaseRunIDs = append(leaseRunIDs, id)
	}
	slices.Sort(leaseRunIDs)
	for _, runID := range leaseRunIDs {
		if _, needed := needLease[runID]; needed {
			continue
		}
		run, err := uw.Store.GetRun(ctx, runID)
		if errors.Is(err, state.ErrNotFound) {
			if _, delErr := w.Backend.DeleteTaskLease(ctx, uw.User.ID, runID, 0); delErr != nil {
				failures = append(failures, delErr)
			}
			continue
		}
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if run.Status != state.RunRunning && !run.CleanupPending {
			snap, sErr := uw.Store.Reconcile(ctx, runID)
			if sErr != nil {
				failures = append(failures, sErr)
				continue
			}
			var delCmdErr error
			for _, cmd := range snap.Commands {
				if !cmd.Delivered {
					if dErr := uw.Store.Delivered(ctx, cmd.ID); dErr != nil {
						delCmdErr = errors.Join(delCmdErr, dErr)
					}
				}
			}
			if delCmdErr != nil {
				failures = append(failures, delCmdErr)
				continue
			}
			if _, delErr := w.Backend.DeleteTaskLease(ctx, uw.User.ID, runID, 0); delErr != nil {
				failures = append(failures, delErr)
			}
		}
	}

	// Reconcile any residual undelivered commands for terminal & clean runs whose lease is already gone.
	after := ""
	for {
		page, err := uw.Store.Pending(ctx, after)
		if err != nil {
			failures = append(failures, err)
			break
		}
		for _, cmd := range page.Commands {
			if _, active := needLease[cmd.RunID]; active {
				continue
			}
			run, err := uw.Store.GetRun(ctx, cmd.RunID)
			if errors.Is(err, state.ErrNotFound) || (err == nil && run.Status != state.RunRunning && !run.CleanupPending) {
				if dErr := uw.Store.Delivered(ctx, cmd.ID); dErr != nil {
					failures = append(failures, dErr)
				}
			} else if err != nil {
				failures = append(failures, err)
			} else {
				if _, eErr := w.Backend.EnsureTaskLease(ctx, uw.User.ID, cmd.RunID, time.Time{}); eErr != nil {
					failures = append(failures, eErr)
				}
			}
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}

	// Reconcile outer conversation & scheduler wakes.
	wakesByLane := map[string]docstore.UserWake{}
	if wakes, err := w.Backend.ListUserWakes(ctx, uw.User.ID); err != nil {
		failures = append(failures, err)
	} else {
		for _, wk := range wakes {
			wakesByLane[wk.Lane] = wk
		}
	}
	if convErr == nil && (conv.Pending() || conv.Active != "") {
		wk, ok := wakesByLane[docstore.LaneConversation]
		if !ok || !wk.Runnable {
			if err := w.Backend.WakeUser(ctx, uw.User.ID, docstore.LaneConversation, time.Time{}); err != nil {
				failures = append(failures, err)
			}
		}
	}
	if nextDue := uw.Scheduler.NextScheduled(ctx); nextDue != nil {
		wk, ok := wakesByLane[docstore.LaneScheduler]
		if !ok || !wk.Runnable || wk.WakeAt.After(*nextDue) {
			if err := w.Backend.WakeUser(ctx, uw.User.ID, docstore.LaneScheduler, *nextDue); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

func (w *World) hasActiveOrUncleanChild(ctx context.Context, uw *UserWorld, parent state.Run) bool {
	for _, link := range parent.Subtasks {
		child, err := uw.Store.GetRun(ctx, link.ID)
		if err != nil {
			if link.Status == state.RunRunning {
				return true
			}
			continue
		}
		if child.Status == state.RunRunning || child.CleanupPending {
			return true
		}
	}
	return false
}

func hasActiveOrUncleanChildMap(parent state.Run, activeRuns map[string]state.Run) bool {
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

// -----------------------------------------------------------------------------
// 7. Time & Pause Chaos
// -----------------------------------------------------------------------------

func (w *World) stepTimeAndPause(ctx context.Context) {
	if w.RNG.IntN(100) < 35 {
		paused, err := w.Paused(ctx)
		if err != nil {
			w.Record("", "control", "pause_read_err", err.Error())
			return
		}
		next := !paused
		if err := w.SetPaused(ctx, next); err != nil {
			w.Record("", "control", "pause_set_err", err.Error())
			return
		}
		w.Stats.PauseToggles++
		w.Record("", "control", "set_paused", fmt.Sprintf("paused=%v", next))
		return
	}

	// Curated time-jump ladder crossing RestartQuiet (1s), TaskLease/ConversationLease (30s),
	// IdleFlushDelay (+2m), IdleDreamDelay (+15m), IdleExtractDelay (+20m),
	// DreamMinInterval (+12h), repair intervals (60s), interaction expiries,
	// and automation due slots.
	durations := []time.Duration{
		50 * time.Millisecond,
		500 * time.Millisecond,
		2 * time.Second,
		15 * time.Second,
		45 * time.Second,
		state.IdleFlushDelay + 5*time.Second,
		6 * time.Minute,
		state.IdleDreamDelay + 5*time.Second,
		state.IdleExtractDelay + 5*time.Second,
		65 * time.Minute,
		state.DreamMinInterval + 5*time.Minute,
	}
	d := durations[w.RNG.IntN(len(durations))]
	now := w.Clock.Advance(d)
	w.Record("", "clock", "advance", fmt.Sprintf("delta=%s now=%s", d, now.Format(time.RFC3339Nano)))
}

// -----------------------------------------------------------------------------
// Quiescence Drain
// -----------------------------------------------------------------------------

// DrainToQuiescence disables faults, unpauses execution, drains conversation
// routing, finishes all remaining simulated worker executions and cleanup
// (subtasks before parents), repairs leases, ticks schedulers, parks idle user
// wakes to a fixed point, and runs both CheckSafetyAt and CheckQuiescence
// (including CheckLeaseQuiescence).
func (w *World) DrainToQuiescence(ctx context.Context) error {
	w.SetFaultsEnabled(false)
	if err := w.SetPaused(ctx, false); err != nil {
		return fmt.Errorf("unpause for drain: %w", err)
	}
	w.Record("", "drain", "start", "faults=false paused=false")

	// Clear any tracked chaos conversation fences so drain owns conversation turns cleanly.
	for _, uw := range w.Users {
		uw.activeConvFence = nil
		uw.activeConvTurn = nil
	}

	// Advance clock once past any active unexpired conversation lease, crashed task lease,
	// crashed TaskControl lease, or user-wake lease so drain can cleanly take over.
	if err := w.advancePastActiveLeases(ctx); err != nil {
		return err
	}

	for round := 1; round <= drainMaxRounds; round++ {
		for _, uw := range w.Users {
			// 1. Drain conversation routing first so held publications can release.
			if err := w.drainUserConversation(ctx, uw); err != nil {
				return fmt.Errorf("drain round %d user %s conversation: %w", round, uw.User.ID, err)
			}

			// 2. Deliver any undelivered interaction answers.
			if err := w.deliverUndeliveredAnswers(ctx, uw); err != nil {
				return fmt.Errorf("drain round %d user %s deliverAnswers: %w", round, uw.User.ID, err)
			}

			// 3. Tick scheduler until all due automations advance and pending/finished occurrences settle.
			if err := w.drainUserScheduler(ctx, uw); err != nil {
				return fmt.Errorf("drain round %d user %s scheduler: %w", round, uw.User.ID, err)
			}

			// 4. Repair leases (ensure missing leases, wake parked runs, delete orphans).
			if err := w.repairUserLeases(ctx, uw); err != nil {
				return fmt.Errorf("drain round %d user %s repair: %w", round, uw.User.ID, err)
			}

			// 5. Finish all open simulated worker executions (subtasks before parents) and cleanup.
			if err := w.drainUserWorkers(ctx, uw); err != nil {
				return fmt.Errorf("drain round %d user %s workers: %w", round, uw.User.ID, err)
			}

			// 6. Drain conversation inputs generated by task completions/questions.
			if err := w.drainUserConversation(ctx, uw); err != nil {
				return fmt.Errorf("drain round %d user %s post-worker conversation: %w", round, uw.User.ID, err)
			}

			// 7. Final lease repair, scheduler settlement, and user wake parking for this round.
			if err := w.repairUserLeases(ctx, uw); err != nil {
				return fmt.Errorf("drain round %d user %s final repair: %w", round, uw.User.ID, err)
			}
			if err := w.drainUserScheduler(ctx, uw); err != nil {
				return fmt.Errorf("drain round %d user %s final scheduler: %w", round, uw.User.ID, err)
			}
			if err := w.drainUserWakes(ctx, uw); err != nil {
				return fmt.Errorf("drain round %d user %s user wakes: %w", round, uw.User.ID, err)
			}
		}

		quiescent, err := w.isQuiescent(ctx)
		if err != nil {
			return fmt.Errorf("drain round %d quiescent check: %w", round, err)
		}
		if quiescent {
			w.Record("", "drain", "converged", fmt.Sprintf("rounds=%d", round))
			break
		}
		if err := w.advancePastActiveLeases(ctx); err != nil {
			return err
		}
	}

	targets := w.UserTargets()
	if err := w.Checker.CheckSafetyAt(ctx, targets, w.Clock.Now()); err != nil {
		return fmt.Errorf("post-drain safety: %w", err)
	}
	if err := w.Checker.CheckQuiescence(ctx, targets, w.Clock.Now()); err != nil {
		return err
	}
	return nil
}

func (w *World) advancePastActiveLeases(ctx context.Context) error {
	now := w.Clock.Now()
	maxLease := now
	consider := func(t time.Time) {
		if t.After(maxLease) {
			maxLease = t
		}
	}
	snap := w.Backend.Snapshot()
	for _, uw := range w.Users {
		conv, _, err := uw.Store.ConversationStatus(ctx)
		if err != nil {
			return fmt.Errorf("drain read conversation status for %s: %w", uw.User.ID, err)
		}
		if conv.Owner != "" {
			consider(conv.LeaseUntil)
		}
		for _, fields := range snap.Collections["owners/"+uw.User.DataOwner()+"/task_controls"] {
			var tc state.TaskControl
			if docstore.Decode(fields, &tc) == nil && tc.Owner != "" {
				consider(tc.LeaseUntil)
			}
		}
	}
	if leases, err := w.Backend.ListTaskLeases(ctx, ""); err == nil {
		for _, l := range leases {
			if l.LeaseUntil != nil {
				consider(*l.LeaseUntil)
			}
			if l.Runnable {
				consider(l.WakeAt)
			}
		}
	}
	if wakes, err := w.Backend.ListUserWakes(ctx, ""); err == nil {
		for _, wk := range wakes {
			if wk.LeaseUntil != nil {
				consider(*wk.LeaseUntil)
			}
		}
	}
	if maxLease.After(now) {
		delta := maxLease.Sub(now) + time.Second
		w.Clock.Advance(delta)
		w.Record("", "drain", "advance_past_lease", fmt.Sprintf("delta=%s", delta))
	}
	return nil
}

func (w *World) drainUserWakes(ctx context.Context, uw *UserWorld) error {
	now := w.Clock.Now()
	wakes, err := w.Backend.ListUserWakes(ctx, uw.User.ID)
	if err != nil {
		return err
	}
	for _, wk := range wakes {
		if !wk.Runnable && wk.Owner == "" {
			continue
		}
		if wk.Lane == docstore.LaneScheduler && wk.Owner == "" && wk.WakeAt.After(now) {
			// Future scheduled wake is valid at quiescence.
			continue
		}
		if wk.LeaseUntil != nil && wk.LeaseUntil.After(now) {
			w.Clock.Advance(wk.LeaseUntil.Sub(now) + time.Second)
			now = w.Clock.Now()
		}
		owner := w.nextWorkerOwner("drain-wake")
		// Drain wakes for this lane (may claim another user first, so re-release others or loop).
		for range len(w.Users) + 4 {
			claim, ok, err := w.Backend.ClaimUserWake(ctx, wk.Lane, owner, state.ConversationLease)
			if err != nil {
				return err
			}
			if !ok {
				break
			}
			target := w.User(claim.UserID)
			if target == nil {
				_, _ = w.Backend.ParkUserWake(ctx, claim.UserID, wk.Lane, owner, claim.WakeSeq)
				continue
			}
			switch wk.Lane {
			case docstore.LaneConversation:
				conv, _, err := target.Store.ConversationStatus(ctx)
				if err != nil {
					return err
				}
				if !conv.Pending() && conv.Active == "" {
					_, _ = w.Backend.ParkUserWake(ctx, claim.UserID, wk.Lane, owner, claim.WakeSeq)
				} else {
					_ = w.Backend.ReleaseUserWake(ctx, claim.UserID, wk.Lane, owner, claim.WakeSeq, now)
				}
			case docstore.LaneScheduler:
				nextDue := target.Scheduler.NextScheduled(ctx)
				if nextDue == nil {
					_, _ = w.Backend.ParkUserWake(ctx, claim.UserID, wk.Lane, owner, claim.WakeSeq)
				} else if nextDue.After(w.Clock.Now()) {
					_ = w.Backend.ReleaseUserWake(ctx, claim.UserID, wk.Lane, owner, claim.WakeSeq, *nextDue)
				} else {
					_ = w.Backend.ReleaseUserWake(ctx, claim.UserID, wk.Lane, owner, claim.WakeSeq, *nextDue)
				}
			}
		}
	}
	return nil
}

func (w *World) drainUserConversation(ctx context.Context, uw *UserWorld) error {
	for range 32 {
		ctrl, _, err := uw.Store.ConversationStatus(ctx)
		if err != nil {
			return err
		}
		if !ctrl.Pending() && ctrl.Active == "" {
			return nil
		}
		if ctrl.Owner != "" && ctrl.LeaseUntil.After(w.Clock.Now()) {
			w.Clock.Advance(ctrl.LeaseUntil.Sub(w.Clock.Now()) + time.Second)
		}
		owner := "drain-" + w.IDs.Next()[:8]
		turnID := w.IDs.Next()
		ctrl, turn, err := uw.Store.AcquireConversation(ctx, owner, turnID)
		if err != nil {
			return err
		}
		if turn == nil {
			return nil
		}
		fence := state.ConversationFence{
			Owner:       ctrl.Owner,
			Fence:       ctrl.Fence,
			TurnID:      turn.ID,
			Through:     turn.Through,
			UserThrough: turn.UserThrough,
			Frozen:      turn.Frozen,
		}
		if state.StaleFor(ctrl, fence) {
			if _, wait := state.RestartWait(ctrl, *turn, w.Clock.Now()); wait > 0 {
				w.Clock.Advance(wait + time.Millisecond)
			}
			restarted, _, err := uw.Store.RestartConversation(ctx, fence, *turn)
			if err != nil {
				return err
			}
			turn = &restarted
			fence.Through = turn.Through
			fence.UserThrough = turn.UserThrough
			fence.Frozen = turn.Frozen
		}
		populateAttempt(turn, w.Clock.Now())
		if err := uw.Store.PublishConversation(ctx, fence, *turn, "drain reply"); err != nil {
			return err
		}
	}
	return nil
}

func (w *World) drainUserScheduler(ctx context.Context, uw *UserWorld) error {
	for range drainMaxTicks {
		if _, err := uw.Scheduler.Tick(ctx); err != nil {
			return err
		}
		due, err := w.hasDueAutomations(ctx, uw)
		if err != nil {
			return err
		}
		if !due {
			return nil
		}
	}
	return nil
}

func (w *World) hasDueAutomations(ctx context.Context, uw *UserWorld) (bool, error) {
	now := w.Clock.Now()
	autos, err := uw.Scheduler.List(ctx, "enabled")
	if err != nil {
		return false, err
	}
	for _, a := range autos {
		if a.NextAt != nil && !a.NextAt.After(now) {
			return true, nil
		}
	}
	return false, nil
}

func (w *World) drainUserWorkers(ctx context.Context, uw *UserWorld) error {
	open, err := uw.Store.OpenTasks(ctx)
	if err != nil {
		return err
	}

	// Finish subtasks first so parents can cleanly finalize without unfinished children.
	var subtasks, parents []state.Run
	for _, r := range open {
		if r.ParentID != "" {
			subtasks = append(subtasks, r)
		} else {
			parents = append(parents, r)
		}
	}
	for _, r := range append(subtasks, parents...) {
		if err := w.drainSingleRun(ctx, uw, r.ID); err != nil {
			return err
		}
	}

	// Clear any remaining terminal cleanup tasks and delete their leases.
	cleanupRuns, err := uw.Store.CleanupTasks(ctx)
	if err != nil {
		return err
	}
	for _, r := range cleanupRuns {
		if r.Status != state.RunRunning || r.Finishing != nil || r.CleanupError != "" {
			if err := uw.Store.FinishCleanup(ctx, r.ID, ""); err != nil {
				return err
			}
			if updated, gerr := uw.Store.GetRun(ctx, r.ID); gerr == nil && updated.Status != state.RunRunning && !updated.CleanupPending {
				_, _ = w.Backend.DeleteTaskLease(ctx, uw.User.ID, r.ID, 0)
			}
		}
	}
	return nil
}

func (w *World) drainSingleRun(ctx context.Context, uw *UserWorld, runID string) error {
	snap, err := uw.Store.Reconcile(ctx, runID)
	if err != nil {
		return err
	}
	for _, cmd := range snap.Commands {
		if !cmd.Delivered {
			if err := uw.Store.Delivered(ctx, cmd.ID); err != nil {
				return err
			}
		}
	}
	run := snap.Run

	// If a crashed worker still holds TaskControl, acquire & release cleanly after expiry.
	if tc, err := uw.Store.GetTaskControl(ctx, run.ID); err == nil && tc.Owner != "" {
		if tc.LeaseUntil.After(w.Clock.Now()) {
			w.Clock.Advance(tc.LeaseUntil.Sub(w.Clock.Now()) + time.Second)
		}
		if _, fence, err := uw.Store.AcquireTaskControl(ctx, run.ID, w.nextWorkerOwner("drain-ctrl")); err == nil {
			_ = uw.Store.ReleaseTaskControl(ctx, run.ID, fence)
		}
	}

	if run.Status != state.RunRunning {
		if run.CleanupPending {
			if err := uw.Store.FinishCleanup(ctx, run.ID, ""); err != nil {
				return err
			}
		}
		_, _ = w.Backend.DeleteTaskLease(ctx, uw.User.ID, run.ID, 0)
		return nil
	}

	// Incorporate any pending commands on the live run.
	if run.Incorporated < state.TaskPosition(run) {
		if err := uw.Store.Incorporate(ctx, state.IncorporateRequest{
			RunID:    run.ID,
			Revision: run.Revision,
			Position: state.TaskPosition(run),
		}); err != nil {
			return err
		}
		run, err = uw.Store.GetRun(ctx, run.ID)
		if err != nil {
			return err
		}
	}

	if run.Finishing != nil {
		if err := uw.Store.FinishCleanup(ctx, run.ID, ""); err != nil {
			return err
		}
	} else {
		status := state.RunCompleted
		detail := ""
		text := "drained completion"
		if run.CancelRequested {
			status = state.RunCancelled
			detail = "drained cancellation"
			text = ""
		}
		pub, err := uw.Store.Publish(ctx, state.PublishRequest{
			RunID:         run.ID,
			Revision:      run.Revision,
			Status:        status,
			Text:          text,
			Detail:        detail,
			Spent:         run.Spent,
			BypassRouting: true,
		})
		if err != nil {
			return err
		}
		if pub.Finishing != nil || pub.CleanupPending {
			if err := uw.Store.FinishCleanup(ctx, run.ID, ""); err != nil {
				return err
			}
		}
	}

	// Mark any late subtask_result/answer commands delivered before deleting the lease.
	if finalSnap, sErr := uw.Store.Reconcile(ctx, run.ID); sErr == nil {
		for _, cmd := range finalSnap.Commands {
			if !cmd.Delivered {
				_ = uw.Store.Delivered(ctx, cmd.ID)
			}
		}
	}

	updated, err := uw.Store.GetRun(ctx, run.ID)
	if err != nil {
		return err
	}
	if updated.Status != state.RunRunning && !updated.CleanupPending {
		_, _ = w.Backend.DeleteTaskLease(ctx, uw.User.ID, run.ID, 0)
	}
	return nil
}

func (w *World) isQuiescent(ctx context.Context) (bool, error) {
	now := w.Clock.Now()
	for _, uw := range w.Users {
		page, err := uw.Store.Pending(ctx, "")
		if err != nil {
			return false, err
		}
		if len(page.Commands) > 0 {
			return false, nil
		}
		open, err := uw.Store.OpenTasks(ctx)
		if err != nil {
			return false, err
		}
		if len(open) > 0 {
			return false, nil
		}
		cleanup, err := uw.Store.CleanupTasks(ctx)
		if err != nil {
			return false, err
		}
		if len(cleanup) > 0 {
			return false, nil
		}
		pubs, err := uw.Store.PublicationTasks(ctx)
		if err != nil {
			return false, err
		}
		if len(pubs) > 0 {
			return false, nil
		}
		conv, _, err := uw.Store.ConversationStatus(ctx)
		if err != nil {
			return false, err
		}
		if conv.Pending() || conv.Active != "" {
			return false, nil
		}
		wv, err := uw.Store.WorkVersion(ctx, true)
		if err != nil {
			return false, err
		}
		if wv != "" {
			return false, nil
		}
		due, err := w.hasDueAutomations(ctx, uw)
		if err != nil {
			return false, err
		}
		if due {
			return false, nil
		}
		for _, status := range []string{"pending", "accepted", "running"} {
			occPage, _, err := uw.Store.Resources(ctx, scheduler.CollectionOccurrences, map[string]string{"status": status}, "", 10)
			if err != nil {
				return false, err
			}
			if len(occPage) > 0 {
				return false, nil
			}
		}
		leases, err := w.Backend.ListTaskLeases(ctx, uw.User.ID)
		if err != nil {
			return false, err
		}
		if len(leases) > 0 {
			return false, nil
		}
		wakes, err := w.Backend.ListUserWakes(ctx, uw.User.ID)
		if err != nil {
			return false, err
		}
		for _, wk := range wakes {
			if wk.Owner != "" || wk.LeaseUntil != nil {
				return false, nil
			}
			if wk.Runnable && !wk.WakeAt.After(now) {
				return false, nil
			}
		}
	}
	return true, nil
}
