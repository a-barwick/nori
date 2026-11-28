package task

import (
	"context"
	"errors"
	"sync"
	"time"

	"nori/internal/llmguard"
	"nori/internal/prompts"
	"nori/internal/state"
)

const homeModelWaiting = "Waiting for Home AI Server"

func (s *runSession) stepModel() stepResult {
	if s.act.HomeModelAvailability != nil {
		if proceed, result := s.checkHomeModelAvailability(); !proceed {
			return result
		}
	}
	if err := s.ensureAccounting(s.watcher.ctx); err != nil {
		if res, lost := s.checkLeaseLost(); lost {
			return res
		}
		return s.backoff(err)
	}
	if proceed, res := s.prepareModelStep(); !proceed {
		return res
	}

	mCtx, mCancel := s.stepCtx(stepTimeoutLong)
	var obsMu sync.Mutex
	var obsCost float64
	var obsUnknown, obsSent bool
	mCtx = llmguard.WithCostObserver(mCtx, func(costUSD float64, costKnown, sent bool) {
		if !sent {
			return
		}
		obsMu.Lock()
		obsSent = true
		if costUSD > 0 {
			obsCost += costUSD
		}
		if !costKnown {
			obsUnknown = true
		}
		obsMu.Unlock()
	})
	model, err := s.act.ModelRef(mCtx, RefModelInput{
		Step:    Step{RunID: s.runID, Revision: s.ctrl.Revision},
		ModelID: s.ctrl.PendingModelID,
		Ref:     s.ctrl.Ref,
		Subtask: s.ctrl.Subtask,
	})
	mCancel()
	obsMu.Lock()
	if obsSent {
		if obsCost > model.Cost {
			model.Cost = obsCost
		}
		if obsCost > 0 && model.NotSent {
			model.NotSent = false
		}
		if obsUnknown && !model.NotSent {
			model.CostKnown = false
		}
	}
	obsMu.Unlock()
	if res, stopped := s.checkStopped(); stopped {
		return res
	}
	if err != nil {
		return s.handleModelErr(model, err)
	}
	return s.commitModelOutcome(model)
}

func (s *runSession) waitForHomeModel(ctx context.Context) stepResult {
	if err := s.store.SetProgress(ctx, s.runID, s.ctrl.Revision, homeModelWaiting); err != nil {
		return s.handleStepErr(err, false)
	}
	s.setBackoff(30*time.Second, s.capturedSeq)
	return stepDone(OutcomeBackoff, nil)
}

func (s *runSession) checkHomeModelAvailability() (bool, stepResult) {
	ctx, cancel := s.stepCtx(stepTimeoutQuick)
	defer cancel()
	run, err := s.store.Guard(ctx, s.runID, s.ctrl.Revision)
	if err != nil {
		switch {
		case errors.Is(err, state.ErrSuperseded):
			return false, s.transitionPhase(state.TaskPhaseSync)
		case errors.Is(err, state.ErrPaused):
			return false, s.handleMidRunPaused()
		default:
			return false, s.handleStepErr(err, false)
		}
	}
	ready, err := s.act.HomeModelAvailability(ctx, run.ModelSelection)
	if err != nil {
		return false, s.backoff(err)
	}
	if ready {
		if run.Progress == homeModelWaiting {
			if err := s.store.SetProgress(ctx, run.ID, s.ctrl.Revision, ""); err != nil {
				return false, s.handleStepErr(err, false)
			}
		}
		return true, stepContinue()
	}
	return false, s.waitForHomeModel(ctx)
}

func (s *runSession) prepareModelStep() (proceed bool, res stepResult) {
	if s.ctrl.PendingModelID == "" {
		pCtx, pCancel := s.stepCtx(stepTimeoutQuick)
		err := s.act.Project(pCtx, Projection{Step: Step{RunID: s.runID, Revision: s.ctrl.Revision}, Kind: "model", Text: "Thinking"})
		pCancel()
		if err != nil {
			if res, stopped := s.checkStopped(); stopped {
				return false, res
			}
			switch {
			case errors.Is(err, state.ErrSuperseded), errors.Is(err, state.ErrPaused):
				next := s.ctrl
				next.ModelStepStartedAt = nil
				next.Phase = state.TaskPhaseSync
				if res := s.saveCtrl(next); res.done {
					return false, res
				}
				if errors.Is(err, state.ErrPaused) {
					return false, s.handleMidRunPaused()
				}
				return false, stepContinue()
			default:
				return false, s.handleStepErr(err, false)
			}
		}
		next := s.ctrl
		next.PendingModelID = nextModelID(s.runID, s.ctrl)
		if next.ModelStepStartedAt == nil {
			started := s.now().UTC().Truncate(time.Microsecond)
			next.ModelStepStartedAt = &started
		}
		if res := s.saveCtrl(next); res.done {
			return false, res
		}
		return true, stepContinue()
	}

	if s.ctrl.ModelStepStartedAt == nil {
		next := s.ctrl
		started := s.now().UTC().Truncate(time.Microsecond)
		next.ModelStepStartedAt = &started
		if res := s.saveCtrl(next); res.done {
			return false, res
		}
	}
	return true, stepContinue()
}

func (s *runSession) handleModelErr(model RefModelOutcome, err error) stepResult {
	var spend *state.SpendUpdate
	sent := !model.NotSent
	if sent && (model.Cost > 0 || !model.CostKnown) {
		spend = &state.SpendUpdate{
			RunID:       s.runID,
			Spent:       s.acct.spent + model.Cost,
			Budget:      s.acct.budget,
			CostUnknown: s.acct.costUnknown || !model.CostKnown,
		}
	}
	switch {
	case errors.Is(err, state.ErrSuperseded), errors.Is(err, state.ErrPaused):
		next := s.ctrl
		if sent {
			next.Models++
			if !model.CostKnown {
				next.EstimatedUnknownUSD += unknownModelStepExposureUSD
			}
		}
		next.PendingModelID = ""
		next.ModelStepStartedAt = nil
		next.Phase = state.TaskPhaseSync
		if res := s.saveOrAdvance(next, spend); res.done {
			return res
		}
		if errors.Is(err, state.ErrPaused) {
			return s.handleMidRunPaused()
		}
		return stepContinue()
	case errors.Is(err, state.ErrCancelled):
		if sent {
			s.ctrl.Models++
		}
		return s.transitionFailure(state.RunCancelled, state.ErrCancelled.Error(), true, spend)
	case errors.Is(err, state.ErrBusy):
		return s.handleBusy()
	default:
		if isPermanentTaskError(err) {
			failSpend := &state.SpendUpdate{
				RunID:       s.runID,
				Spent:       s.acct.spent + model.Cost,
				Budget:      s.acct.budget,
				CostUnknown: true,
			}
			return s.transitionFailure(state.RunFailed, err.Error(), false, failSpend)
		}
		if spend != nil {
			next := s.ctrl
			if sent && !model.CostKnown {
				next.EstimatedUnknownUSD += unknownModelStepExposureUSD
			}
			if res := s.saveOrAdvance(next, spend); res.done {
				return res
			}
		}
		return s.backoff(err)
	}
}

func (s *runSession) commitModelOutcome(model RefModelOutcome) stepResult {
	if model.HomeUnavailable {
		next := s.ctrl
		next.Models++
		next.PendingModelID = ""
		next.ModelStepStartedAt = nil
		if result := s.saveCtrl(next); result.done {
			return result
		}
		ctx, cancel := s.stepCtx(stepTimeoutQuick)
		defer cancel()
		return s.waitForHomeModel(ctx)
	}
	newSpent := s.acct.spent + model.Cost
	newCostUnknown := s.acct.costUnknown || (!model.CostKnown && !model.NotSent)
	spend := &state.SpendUpdate{
		RunID:       s.runID,
		Spent:       newSpent,
		Budget:      s.acct.budget,
		CostUnknown: newCostUnknown,
	}

	next := s.ctrl
	next.Models++
	if !model.NotSent {
		next.ModelAttempts++
		if !model.CostKnown {
			next.EstimatedUnknownUSD += unknownModelStepExposureUSD
		}
	}
	next.Ref = model.Ref
	next.PendingModelID = ""

	started := s.now()
	if s.ctrl.ModelStepStartedAt != nil {
		started = *s.ctrl.ModelStepStartedAt
	}
	if model.Interrupted && s.ctrl.ModelRetries < 2 && s.now().Sub(started) <= modelStepDeadline-modelPassLimit {
		next.ModelRetries++
		next.Phase = state.TaskPhaseModel
		return s.advanceStep(next, spend)
	}

	next.ModelStepStartedAt = nil
	if model.Failure != "" {
		next.PendingPublication = &state.TaskPublication{
			RunID:         s.runID,
			Revision:      s.ctrl.Revision,
			Status:        state.RunFailed,
			Detail:        model.Failure,
			StopConfirmed: true,
			CostUnknown:   newCostUnknown,
			Spent:         newSpent,
			BypassRouting: false,
		}
		next.Phase = state.TaskPhasePublish
		return s.advanceStep(next, spend)
	}
	if model.Invalid {
		next.ConsecutiveInvalid++
		next.TotalInvalid++
		if next.ConsecutiveInvalid >= maxConsecutiveInvalidRounds || next.TotalInvalid >= maxTotalInvalidRounds {
			detail := "The model returned repeated invalid tool calls, so the task stopped."
			next.PendingPublication = &state.TaskPublication{
				RunID:              s.runID,
				Revision:           s.ctrl.Revision,
				Status:             state.RunFailed,
				Detail:             detail,
				Text:               detail,
				StopConfirmed:      true,
				ExpectedIncomplete: true,
				CostUnknown:        newCostUnknown,
				Spent:              newSpent,
				BypassRouting:      false,
			}
			next.Phase = state.TaskPhasePublish
			return s.advanceStep(next, spend)
		}
		next.Phase = state.TaskPhaseSync
		return s.advanceStep(next, spend)
	}
	next.ConsecutiveInvalid = 0

	pCtx, pCancel := s.stepCtx(stepTimeoutQuick)
	current, posErr := s.act.CurrentPosition(pCtx, s.runID)
	pCancel()
	if posErr != nil {
		return s.handleModelPositionErr(model, next, spend, posErr)
	}

	next.PendingCalls = toTaskCallRefs(model.Calls)
	next.CallIndex = 0
	next.CallsChanged = current.Revision != s.ctrl.Revision || current.Position != s.ctrl.Position
	next.CallsRevised = false
	if len(next.PendingCalls) == 0 {
		next.Phase = state.TaskPhaseSync
	} else {
		next.Phase = state.TaskPhaseCalls
	}
	return s.advanceStep(next, spend)
}

func (s *runSession) handleModelPositionErr(
	model RefModelOutcome,
	next state.TaskControl,
	spend *state.SpendUpdate,
	posErr error,
) stepResult {
	if res, stopped := s.checkStopped(); stopped {
		return res
	}
	switch {
	case errors.Is(posErr, state.ErrSuperseded), errors.Is(posErr, state.ErrPaused):
		for _, call := range model.Calls {
			aCtx, aCancel := s.stepCtx(stepTimeoutQuick)
			ref, appErr := s.act.AppendToolResult(aCtx, AppendResult{
				Step:     Step{RunID: s.runID, Revision: s.ctrl.Revision},
				Ref:      next.Ref,
				CallID:   call.ID,
				Name:     call.Name,
				Response: map[string]any{"skipped": true, "reason": prompts.Runtime("workflow-execution-admission-changed-before-this-call-ran", nil)},
			})
			aCancel()
			if appErr != nil {
				if res, stopped := s.checkStopped(); stopped {
					return res
				}
				return s.backoff(appErr)
			}
			next.Ref = ref
		}
		next.Phase = state.TaskPhaseSync
		if res := s.advanceStep(next, spend); res.done {
			return res
		}
		if errors.Is(posErr, state.ErrPaused) {
			return s.handleMidRunPaused()
		}
		return stepContinue()
	case errors.Is(posErr, state.ErrCancelled):
		next.PendingPublication = &state.TaskPublication{
			RunID:         s.runID,
			Revision:      s.ctrl.Revision,
			Status:        state.RunCancelled,
			Detail:        state.ErrCancelled.Error(),
			StopConfirmed: true,
			CostUnknown:   spend.CostUnknown,
			Spent:         spend.Spent,
			BypassRouting: true,
		}
		next.Phase = state.TaskPhasePublish
		return s.advanceStep(next, spend)
	case errors.Is(posErr, state.ErrBusy):
		if res := s.advanceStep(next, spend); res.done {
			return res
		}
		return s.handleBusy()
	default:
		return s.backoff(posErr)
	}
}
