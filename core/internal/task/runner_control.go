package task

import (
	"context"
	"errors"
	"time"

	"nori/internal/state"
	"nori/internal/state/docstore"
)

type familyAccounting struct {
	loaded      bool
	spent       float64
	others      float64
	budget      float64
	costUnknown bool
}

type stepResult struct {
	done    bool
	outcome RunOutcome
	err     error
}

func stepContinue() stepResult { return stepResult{} }

func stepDone(outcome RunOutcome, err error) stepResult {
	return stepResult{done: true, outcome: outcome, err: err}
}

type runSession struct {
	runner *Runner
	ctx    context.Context
	act    Activities
	store  state.TaskStore
	userID string
	runID  string
	owner  string

	capturedSeq int64

	haveFence    bool
	fence        state.TaskFence
	ctrl         state.TaskControl
	watcher      *leaseWatcher
	leaseSettled bool
	releaseWake  time.Time
	releaseSeq   int64
	acct         familyAccounting
}

func (s *runSession) now() time.Time {
	return s.runner.now(&s.act)
}

func (s *runSession) stepCtx(d time.Duration) (context.Context, context.CancelFunc) {
	return s.runner.withStepTimeout(s.watcher.ctx, &s.act, d)
}

func (s *runSession) baseCtx(d time.Duration) (context.Context, context.CancelFunc) {
	return s.runner.withStepTimeout(s.ctx, &s.act, d)
}

func (s *runSession) activeCtx(d time.Duration) (context.Context, context.CancelFunc) {
	parent := s.ctx
	if s.watcher != nil {
		parent = s.watcher.ctx
	}
	return s.runner.withStepTimeout(parent, &s.act, d)
}

func (s *runSession) stopWatcher() {
	if s.watcher != nil {
		s.watcher.stop()
		s.watcher = nil
	}
}

func (s *runSession) startWatcher() {
	s.stopWatcher()
	s.watcher = s.runner.startTaskLeaseWatcher(s.ctx, s.store, s.userID, s.runID, s.owner, s.fence)
}

func (s *runSession) close() {
	s.stopWatcher()
	relCtx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), shutdownReleaseTimeout)
	defer cancel()
	if s.haveFence {
		_ = s.store.ReleaseTaskControl(relCtx, s.runID, s.fence)
		s.haveFence = false
	}
	if !s.leaseSettled {
		_ = s.runner.Leases.ReleaseTask(relCtx, s.userID, s.runID, s.owner, s.releaseSeq, s.releaseWake)
		s.leaseSettled = true
	}
}

func (s *runSession) setBackoff(d time.Duration, seq int64) {
	s.releaseWake = s.now().Add(d)
	s.releaseSeq = seq
}

func (s *runSession) backoff(err error) stepResult {
	s.setBackoff(transientBackoff, s.capturedSeq)
	return stepDone(OutcomeBackoff, err)
}

func (s *runSession) classifyLeaseErr(watchErr error) stepResult {
	if errors.Is(watchErr, state.ErrTaskBusy) {
		s.haveFence = false
		s.setBackoff(busyBackoff, s.capturedSeq)
		return stepDone(OutcomeBusy, nil)
	}
	return s.backoff(watchErr)
}

func (s *runSession) classifyControlErr(err error) stepResult {
	if res, lost := s.checkLeaseLost(); lost {
		return res
	}
	if errors.Is(err, state.ErrTaskBusy) {
		s.haveFence = false
		s.setBackoff(busyBackoff, s.capturedSeq)
		return stepDone(OutcomeBusy, nil)
	}
	if s.ctx.Err() != nil {
		return stepDone(OutcomeReleased, s.ctx.Err())
	}
	return s.backoff(err)
}

func (s *runSession) checkLeaseLost() (stepResult, bool) {
	if s.watcher != nil {
		if watchErr := s.watcher.lastErr(); watchErr != nil {
			return s.classifyLeaseErr(watchErr), true
		}
	}
	return stepContinue(), false
}

func (s *runSession) checkStopped() (stepResult, bool) {
	if res, lost := s.checkLeaseLost(); lost {
		return res, true
	}
	if s.ctx.Err() != nil {
		return stepDone(OutcomeReleased, s.ctx.Err()), true
	}
	return stepContinue(), false
}

func (s *runSession) checkLoopAbort() (stepResult, bool) {
	if res, stopped := s.checkStopped(); stopped {
		return res, true
	}
	if s.runner.draining() {
		return stepDone(OutcomeReleased, nil), true
	}
	return stepContinue(), false
}

func (s *runSession) handleStepErr(err error, allowPermanent bool) stepResult {
	switch {
	case errors.Is(err, state.ErrCancelled):
		return s.transitionFailure(state.RunCancelled, state.ErrCancelled.Error(), true, nil)
	case errors.Is(err, state.ErrBusy):
		return s.handleBusy()
	case allowPermanent && isPermanentTaskError(err):
		return s.transitionFailure(state.RunFailed, err.Error(), false, nil)
	default:
		return s.backoff(err)
	}
}

func (s *runSession) releaseControl() {
	s.stopWatcher()
	if !s.haveFence {
		return
	}
	relCtx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), shutdownReleaseTimeout)
	_ = s.store.ReleaseTaskControl(relCtx, s.runID, s.fence)
	cancel()
	s.haveFence = false
}

func (s *runSession) deleteLease() stepResult {
	s.releaseControl()
	delCtx, cancel := s.baseCtx(stepTimeoutQuick)
	defer cancel()
	if err := markRunCommandsDelivered(delCtx, s.store, s.runID); err != nil {
		return s.backoff(err)
	}
	if _, err := s.runner.Leases.DeleteTaskLease(delCtx, s.userID, s.runID, 0); err != nil {
		return s.backoff(err)
	}
	s.leaseSettled = true
	return stepDone(OutcomeDone, nil)
}

func (s *runSession) park(reason string, reacquire bool) stepResult {
	s.releaseControl()
	pCtx, cancel := s.baseCtx(stepTimeoutQuick)
	parked, err := s.runner.Leases.ParkTask(pCtx, s.userID, s.runID, s.owner, s.capturedSeq, reason)
	cancel()
	if err != nil {
		return s.backoff(err)
	}
	if parked {
		s.leaseSettled = true
		return stepDone(OutcomeParked, nil)
	}
	if s.ctx.Err() != nil {
		return stepDone(OutcomeReleased, s.ctx.Err())
	}
	if s.runner.draining() {
		return stepDone(OutcomeReleased, nil)
	}
	rCtx, rCancel := s.baseCtx(stepTimeoutQuick)
	refreshed, stillOwned, err := s.runner.Leases.RefreshTaskClaim(rCtx, s.userID, s.runID, s.owner, state.TaskLease)
	rCancel()
	if err != nil {
		return s.backoff(err)
	}
	if !stillOwned {
		s.leaseSettled = true
		return stepDone(OutcomeBusy, nil)
	}
	s.capturedSeq = refreshed.WakeSeq
	if !reacquire {
		return stepContinue()
	}
	aCtx, aCancel := s.baseCtx(stepTimeoutQuick)
	nextCtrl, nextFence, err := s.store.AcquireTaskControl(aCtx, s.runID, s.owner)
	aCancel()
	if errors.Is(err, state.ErrTaskBusy) {
		s.setBackoff(busyBackoff, s.capturedSeq)
		return stepDone(OutcomeBusy, nil)
	}
	if err != nil {
		return s.backoff(err)
	}
	s.ctrl = nextCtrl
	s.fence = nextFence
	s.haveFence = true
	s.startWatcher()
	return stepContinue()
}

func (s *runSession) handlePaused(run state.Run, reacquire bool) stepResult {
	if run.CancelRequested || run.Finishing != nil || (run.Status != state.RunRunning && run.CleanupPending) {
		sCtx, cancel := s.activeCtx(stepTimeoutCleanup)
		_, err := StopPaused(sCtx, s.store, s.runID)
		cancel()
		if err != nil {
			if res, stopped := s.checkStopped(); stopped {
				return res
			}
			return s.backoff(err)
		}
		gCtx, gCancel := s.activeCtx(stepTimeoutQuick)
		after, gErr := s.store.GetRun(gCtx, s.runID)
		gCancel()
		if gErr != nil {
			if res, lost := s.checkLeaseLost(); lost {
				return res
			}
			return s.backoff(gErr)
		}
		if after.Status != state.RunRunning && !after.CleanupPending {
			return s.deleteLease()
		}
		if after.Status == state.RunRunning && after.Finishing != nil {
			return s.park(docstore.WaitFinishing, reacquire)
		}
		return s.backoff(nil)
	}
	return s.park(docstore.WaitPaused, reacquire)
}

func (s *runSession) handleMidRunPaused() stepResult {
	gCtx, gCancel := s.baseCtx(stepTimeoutQuick)
	run, gErr := s.store.GetRun(gCtx, s.runID)
	gCancel()
	if gErr != nil {
		return s.backoff(gErr)
	}
	return s.handlePaused(run, true)
}

func (s *runSession) preAcquire() (state.Run, stepResult) {
	for {
		if s.ctx.Err() != nil {
			return state.Run{}, stepDone(OutcomeReleased, s.ctx.Err())
		}
		if s.runner.draining() {
			return state.Run{}, stepDone(OutcomeReleased, nil)
		}
		gCtx, gCancel := s.baseCtx(stepTimeoutQuick)
		run, err := s.store.GetRun(gCtx, s.runID)
		gCancel()
		if errors.Is(err, state.ErrNotFound) {
			return state.Run{}, s.deleteLease()
		}
		if err != nil {
			return state.Run{}, s.backoff(err)
		}
		if run.Status != state.RunRunning && !run.CleanupPending {
			return state.Run{}, s.deleteLease()
		}
		cCtx, cCancel := s.baseCtx(stepTimeoutQuick)
		control, err := s.store.Control(cCtx)
		cCancel()
		if err != nil {
			return state.Run{}, s.backoff(err)
		}
		if control.Paused {
			if res := s.handlePaused(run, false); res.done {
				return state.Run{}, res
			}
			continue
		}
		return run, stepContinue()
	}
}

func (s *runSession) acquireControl(initialRun state.Run) stepResult {
	aCtx, aCancel := s.baseCtx(stepTimeoutQuick)
	ctrl, fence, err := s.store.AcquireTaskControl(aCtx, s.runID, s.owner)
	aCancel()
	if errors.Is(err, state.ErrTaskBusy) {
		s.setBackoff(busyBackoff, s.capturedSeq)
		return stepDone(OutcomeBusy, nil)
	}
	if err != nil {
		return s.backoff(err)
	}
	s.ctrl = ctrl
	s.fence = fence
	s.haveFence = true
	s.startWatcher()

	if s.ctrl.Phase == "" {
		s.ctrl.Phase = state.TaskPhaseSync
	}
	if (initialRun.Status != state.RunRunning && initialRun.CleanupPending) ||
		(initialRun.Status == state.RunRunning && initialRun.Finishing != nil) {
		if s.ctrl.Phase != state.TaskPhaseCleanup && s.ctrl.Phase != state.TaskPhaseDone {
			next := s.ctrl
			next.Phase = state.TaskPhaseCleanup
			clearPendingWork(&next)
			next.PendingPublication = nil
			return s.saveCtrl(next)
		}
	}
	return stepContinue()
}

func (s *runSession) ensureAccounting(stepParent context.Context) error {
	if s.acct.loaded {
		return nil
	}
	fCtx, fCancel := s.runner.withStepTimeout(stepParent, &s.act, stepTimeoutQuick)
	defer fCancel()
	run, err := s.store.GetRun(fCtx, s.runID)
	if err != nil {
		return err
	}
	var parent *state.Run
	if run.ParentID != "" {
		ownerRun, err := s.store.GetRun(fCtx, run.ParentID)
		if err != nil {
			return err
		}
		parent = &ownerRun
	}
	others, budget, costUnknown := state.FamilyCost(run, parent)
	s.acct = familyAccounting{
		loaded:      true,
		spent:       run.Spent,
		others:      others,
		budget:      budget,
		costUnknown: run.CostUnknown || costUnknown,
	}
	return nil
}

func (s *runSession) saveCtrl(next state.TaskControl) stepResult {
	sCtx, sCancel := s.stepCtx(stepTimeoutQuick)
	saved, err := s.store.SaveTaskControl(sCtx, s.fence, next)
	sCancel()
	if err != nil {
		return s.classifyControlErr(err)
	}
	s.ctrl = saved
	return stepContinue()
}

func (s *runSession) advanceStep(next state.TaskControl, spend *state.SpendUpdate) stepResult {
	sCtx, sCancel := s.stepCtx(stepTimeoutQuick)
	saved, updatedRun, err := s.store.AdvanceTaskStep(sCtx, s.fence, next, spend)
	sCancel()
	if err != nil {
		return s.classifyControlErr(err)
	}
	s.ctrl = saved
	if spend != nil {
		s.acct.spent = updatedRun.Spent
		if updatedRun.Budget > 0 && s.acct.budget == 0 {
			s.acct.budget = updatedRun.Budget
		}
		s.acct.costUnknown = s.acct.costUnknown || updatedRun.CostUnknown
	}
	return stepContinue()
}

func (s *runSession) saveOrAdvance(next state.TaskControl, spend *state.SpendUpdate) stepResult {
	if spend != nil {
		return s.advanceStep(next, spend)
	}
	return s.saveCtrl(next)
}

func (s *runSession) transitionPhase(phase state.TaskPhase) stepResult {
	next := s.ctrl
	next.Phase = phase
	return s.saveCtrl(next)
}

func (s *runSession) transitionFailure(status state.RunStatus, detail string, bypassRouting bool, spend *state.SpendUpdate) stepResult {
	if !s.acct.loaded {
		_ = s.ensureAccounting(s.watcher.ctx)
	}
	pubSpent := s.acct.spent
	pubUnknown := s.acct.costUnknown
	if spend != nil {
		pubSpent = max(pubSpent, spend.Spent)
		pubUnknown = pubUnknown || spend.CostUnknown
	}
	next := s.ctrl
	clearPendingWork(&next)
	next.PendingPublication = &state.TaskPublication{
		RunID:         s.runID,
		Revision:      s.ctrl.Revision,
		Status:        status,
		Detail:        detail,
		StopConfirmed: true,
		CostUnknown:   pubUnknown,
		Spent:         pubSpent,
		BypassRouting: bypassRouting,
	}
	next.Phase = state.TaskPhasePublish
	return s.saveOrAdvance(next, spend)
}

func (s *runSession) handleBusy() stepResult {
	gCtx, gCancel := s.stepCtx(stepTimeoutQuick)
	run, gErr := s.store.GetRun(gCtx, s.runID)
	gCancel()
	if gErr != nil {
		return s.classifyControlErr(gErr)
	}
	if run.Status != state.RunRunning && !run.CleanupPending {
		return s.deleteLease()
	}
	if run.Finishing != nil || run.CleanupPending {
		next := s.ctrl
		next.Phase = state.TaskPhaseCleanup
		next.PendingPublication = nil
		clearPendingWork(&next)
		return s.saveCtrl(next)
	}
	return s.backoff(state.ErrBusy)
}
