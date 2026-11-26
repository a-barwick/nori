package task

import (
	"errors"

	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

func (s *runSession) stepPublish() stepResult {
	if s.ctrl.PendingPublication == nil {
		gCtx, gCancel := s.stepCtx(stepTimeoutQuick)
		run, gErr := s.store.GetRun(gCtx, s.runID)
		gCancel()
		if gErr != nil {
			return s.classifyControlErr(gErr)
		}
		if run.Status != state.RunRunning || run.Finishing != nil {
			return s.transitionPhase(state.TaskPhaseCleanup)
		}
		return s.transitionPhase(state.TaskPhaseSync)
	}

	pending := *s.ctrl.PendingPublication
	pub := toRefPublication(pending)
	pubTimeout := stepTimeoutQuick
	if pub.Ref == "" && !pub.ExpectedIncomplete {
		pubTimeout = stepTimeoutCleanup
	}
	pCtx, pCancel := s.stepCtx(pubTimeout)
	_, err := s.act.PublishRef(pCtx, pub)
	pCancel()
	if err != nil {
		if res, stopped := s.checkStopped(); stopped {
			return res
		}
		switch {
		case errors.Is(err, state.ErrRoutingPending):
			return s.park(docstore.WaitRoutingPending, true)
		case errors.Is(err, state.ErrSuperseded):
			next := s.ctrl
			if pending.CompletionCall != nil {
				aCtx, aCancel := s.stepCtx(stepTimeoutQuick)
				ref, appErr := s.act.AppendToolResult(aCtx, AppendResult{
					Step:   Step{RunID: s.runID, Revision: s.ctrl.Revision},
					Ref:    next.Ref,
					CallID: pending.CompletionCall.ID,
					Name:   pending.CompletionCall.Name,
					Response: map[string]any{
						"accepted": false,
						"reason":   prompts.Runtime("workflow-accepted-instructions-changed-before-publication-reconc", nil),
					},
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
			next.PendingPublication = nil
			next.Phase = state.TaskPhaseSync
			return s.saveCtrl(next)
		case errors.Is(err, state.ErrCancelled):
			next := s.ctrl
			next.PendingPublication = &state.TaskPublication{
				RunID:         s.runID,
				Revision:      pending.Revision,
				Status:        state.RunCancelled,
				Detail:        state.ErrCancelled.Error(),
				StopConfirmed: true,
				CostUnknown:   pending.CostUnknown,
				Spent:         pending.Spent,
				BypassRouting: true,
			}
			return s.saveCtrl(next)
		case errors.Is(err, state.ErrPaused):
			return s.handleMidRunPaused()
		default:
			return s.backoff(err)
		}
	}

	next := s.ctrl
	next.PendingPublication = nil
	next.Phase = state.TaskPhaseCleanup
	return s.saveCtrl(next)
}

func (s *runSession) stepCleanup() stepResult {
	gCtx, gCancel := s.stepCtx(stepTimeoutQuick)
	run, err := s.store.GetRun(gCtx, s.runID)
	gCancel()
	if err != nil {
		return s.classifyControlErr(err)
	}
	if run.Status == state.RunRunning && run.Finishing == nil {
		return s.transitionPhase(state.TaskPhaseSync)
	}

	if run.CleanupPending {
		cCtx, cCancel := s.stepCtx(stepTimeoutCleanup)
		err := s.act.Cleanup(cCtx, s.runID)
		cCancel()
		if err != nil {
			if res, stopped := s.checkStopped(); stopped {
				return res
			}
			if errors.Is(err, state.ErrBusy) {
				return s.transitionPhase(state.TaskPhaseSync)
			}
			return s.backoff(err)
		}
		gCtx2, gCancel2 := s.stepCtx(stepTimeoutQuick)
		run, err = s.store.GetRun(gCtx2, s.runID)
		gCancel2()
		if err != nil {
			return s.classifyControlErr(err)
		}
	} else if run.Status == state.RunRunning && run.Finishing != nil {
		pCtx, pCancel := s.stepCtx(stepTimeoutCleanup)
		run, err = s.store.Publish(pCtx, *run.Finishing)
		pCancel()
		if err != nil {
			if res, stopped := s.checkStopped(); stopped {
				return res
			}
			return s.backoff(err)
		}
	}

	if run.Status != state.RunRunning && !run.CleanupPending {
		dCtx, dCancel := s.stepCtx(stepTimeoutQuick)
		err := markRunCommandsDelivered(dCtx, s.store, s.runID)
		dCancel()
		if err != nil {
			return s.classifyControlErr(err)
		}
		if res := s.transitionPhase(state.TaskPhaseDone); res.done {
			return res
		}
		return s.deleteLease()
	}
	if run.Status == state.RunRunning && run.Finishing != nil {
		return s.park(docstore.WaitFinishing, true)
	}
	return s.backoff(errors.New("task cleanup left run in unexpected state"))
}

func (s *runSession) stepDone() stepResult {
	gCtx, gCancel := s.stepCtx(stepTimeoutQuick)
	run, err := s.store.GetRun(gCtx, s.runID)
	gCancel()
	if err != nil {
		return s.classifyControlErr(err)
	}
	if run.Status != state.RunRunning && !run.CleanupPending {
		dCtx, dCancel := s.stepCtx(stepTimeoutQuick)
		err := markRunCommandsDelivered(dCtx, s.store, s.runID)
		dCancel()
		if err != nil {
			return s.classifyControlErr(err)
		}
		return s.deleteLease()
	}
	if run.CleanupPending || run.Finishing != nil {
		return s.transitionPhase(state.TaskPhaseCleanup)
	}
	return s.transitionPhase(state.TaskPhaseSync)
}
