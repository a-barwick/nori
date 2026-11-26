package task

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"nori/internal/agent"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

const (
	defaultMaxModelAttempts        = 24
	defaultMaxSubtaskModelAttempts = 14
	defaultMaxPaidTools            = 20
	maxConsecutiveInvalidRounds    = 3
	maxTotalInvalidRounds          = 5
	unknownModelStepExposureUSD    = 0.02
	maxRepeatedFailedToolSigs      = 3
	maxRepeatedConfirmedToolSigs   = 8
	recentToolSigWindow            = 12
)

func (s *runSession) maxModelAttemptsFor(subtask bool) int {
	if s.runner != nil && s.runner.MaxModelAttempts > 0 {
		return s.runner.MaxModelAttempts
	}
	if subtask {
		return defaultMaxSubtaskModelAttempts
	}
	if s.acct.budget >= 5.0 {
		return 64
	}
	return defaultMaxModelAttempts
}

func isPaidTool(policy agent.ToolPolicy) bool {
	return policy.InterruptedCostUSD > 0 || policy.InterruptedCostUnknown
}

func appendToolSig(sigs []string, sig string) ([]string, bool) {
	if sig == "" {
		return sigs, false
	}
	out := append(append([]string(nil), sigs...), sig)
	if len(out) > recentToolSigWindow {
		out = out[len(out)-recentToolSigWindow:]
	}
	count := 0
	for _, s := range out {
		if s == sig {
			count++
		}
	}
	threshold := maxRepeatedFailedToolSigs
	if len(sig) >= 10 && sig[:10] == "confirmed:" {
		threshold = maxRepeatedConfirmedToolSigs
	}
	return out, count >= threshold
}

func (s *runSession) runPhase() stepResult {
	switch s.ctrl.Phase {
	case "", state.TaskPhaseSync:
		return s.stepSync()
	case state.TaskPhaseAsk:
		return s.stepAsk()
	case state.TaskPhaseModel:
		return s.stepModel()
	case state.TaskPhaseCalls:
		return s.stepCalls()
	case state.TaskPhasePublish:
		return s.stepPublish()
	case state.TaskPhaseCleanup:
		return s.stepCleanup()
	case state.TaskPhaseDone:
		return s.stepDone()
	default:
		return s.transitionPhase(state.TaskPhaseSync)
	}
}

func (s *runSession) stepSync() stepResult {
	sCtx, sCancel := s.stepCtx(stepTimeoutQuick)
	synced, err := s.act.SyncHistory(sCtx, HistoryInput{
		Step:     Step{RunID: s.runID, Revision: s.ctrl.Revision},
		Ref:      s.ctrl.Ref,
		Position: s.ctrl.Position,
		Question: s.ctrl.Question,
	})
	sCancel()
	if err != nil {
		if res, stopped := s.checkStopped(); stopped {
			return res
		}
		switch {
		case errors.Is(err, state.ErrSuperseded):
			return stepContinue()
		case errors.Is(err, state.ErrPaused):
			return s.handleMidRunPaused()
		default:
			return s.handleStepErr(err, true)
		}
	}

	s.acct = familyAccounting{
		loaded:      true,
		spent:       synced.Spent,
		others:      synced.Others,
		budget:      synced.Budget,
		costUnknown: synced.CostUnknown,
	}

	next := s.ctrl
	next.Ref = synced.Ref
	next.Revision = synced.Revision
	next.Position = synced.Position
	next.Request = synced.Request
	if synced.QuestionResolved {
		next.Question = nil
		next.QuestionPublished = false
	}
	next.Subtask = synced.Subtask
	next.Inherited = synced.ReadOnly
	clearPendingWork(&next)

	effectiveSpend := s.acct.spent + s.acct.others + next.EstimatedUnknownUSD
	if next.Question == nil && s.acct.budget > 0 && effectiveSpend >= s.acct.budget {
		detail := prompts.Runtime("spending-limit", map[string]any{"Budget": s.acct.budget})
		next.PendingPublication = &state.TaskPublication{
			RunID:              s.runID,
			Revision:           next.Revision,
			Status:             state.RunFailed,
			Detail:             detail,
			Text:               detail,
			StopConfirmed:      true,
			ExpectedIncomplete: true,
			Spent:              s.acct.spent,
			CostUnknown:        s.acct.costUnknown,
			BypassRouting:      false,
		}
		next.Phase = state.TaskPhasePublish
		return s.saveCtrl(next)
	}
	if next.Question == nil && next.ModelAttempts >= s.maxModelAttemptsFor(next.Subtask) {
		detail := fmt.Sprintf("This task reached its step limit (%d model attempts) and stopped to avoid runaway cost.", s.maxModelAttemptsFor(next.Subtask))
		next.PendingPublication = &state.TaskPublication{
			RunID:              s.runID,
			Revision:           next.Revision,
			Status:             state.RunFailed,
			Detail:             detail,
			Text:               detail,
			StopConfirmed:      true,
			ExpectedIncomplete: true,
			Spent:              s.acct.spent,
			CostUnknown:        s.acct.costUnknown,
			BypassRouting:      false,
		}
		next.Phase = state.TaskPhasePublish
		return s.saveCtrl(next)
	}

	if next.Question != nil {
		if !next.QuestionPublished {
			next.Phase = state.TaskPhaseAsk
			return s.saveCtrl(next)
		}
		next.Phase = state.TaskPhaseSync
		if res := s.saveCtrl(next); res.done {
			return res
		}
		return s.park(docstore.WaitQuestion, true)
	}

	next.Phase = state.TaskPhaseModel
	return s.saveCtrl(next)
}

func (s *runSession) stepAsk() stepResult {
	if s.ctrl.Question == nil {
		return s.transitionPhase(state.TaskPhaseSync)
	}
	if !s.ctrl.QuestionPublished {
		qCtx, qCancel := s.stepCtx(stepTimeoutQuick)
		err := s.act.Ask(qCtx, QuestionProjection{
			Step:     Step{RunID: s.runID, Revision: s.ctrl.Revision},
			Question: *s.ctrl.Question,
		})
		qCancel()
		if err != nil {
			if res, stopped := s.checkStopped(); stopped {
				return res
			}
			switch {
			case errors.Is(err, state.ErrSuperseded):
				return s.transitionPhase(state.TaskPhaseSync)
			case errors.Is(err, state.ErrPaused):
				if res := s.transitionPhase(state.TaskPhaseSync); res.done {
					return res
				}
				return s.handleMidRunPaused()
			default:
				return s.handleStepErr(err, false)
			}
		}
		next := s.ctrl
		next.QuestionPublished = true
		next.Phase = state.TaskPhaseSync
		if res := s.saveCtrl(next); res.done {
			return res
		}
	}
	return s.park(docstore.WaitQuestion, true)
}

func clearPendingCalls(ctrl *state.TaskControl) {
	ctrl.PendingCalls = nil
	ctrl.CallIndex = 0
	ctrl.CallsChanged = false
	ctrl.CallsRevised = false
}

func clearPendingWork(ctrl *state.TaskControl) {
	ctrl.PendingModelID = ""
	ctrl.ModelStepStartedAt = nil
	clearPendingCalls(ctrl)
}

func nextModelID(runID string, ctrl state.TaskControl) string {
	key := fmt.Sprintf("%s:model:%d:rev:%d:retry:%d:fence:%d", runID, ctrl.Models+1, ctrl.Revision, ctrl.ModelRetries, ctrl.Fence)
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(key)).String()
}

func toTaskCallRefs(calls []CallRef) []state.TaskCallRef {
	if len(calls) == 0 {
		return nil
	}
	out := make([]state.TaskCallRef, len(calls))
	for i, c := range calls {
		out[i] = state.TaskCallRef{
			ID:    c.ID,
			Name:  c.Name,
			Index: c.Index,
			Exec:  c.Exec,
		}
	}
	return out
}

func toRefPublication(p state.TaskPublication) RefPublication {
	return RefPublication{
		Publication: Publication{
			Step:               Step{RunID: p.RunID, Revision: p.Revision},
			Status:             p.Status,
			Detail:             p.Detail,
			Text:               p.Text,
			StopConfirmed:      p.StopConfirmed,
			CostUnknown:        p.CostUnknown,
			Spent:              p.Spent,
			BypassRouting:      p.BypassRouting,
			ExpectedIncomplete: p.ExpectedIncomplete,
			Answer:             p.Answer,
		},
		Ref: p.Ref,
	}
}

func advanceCallCursor(ctrl *state.TaskControl) {
	ctrl.CallIndex++
	if ctrl.CallIndex >= len(ctrl.PendingCalls) {
		clearPendingCalls(ctrl)
		if ctrl.Question != nil && !ctrl.QuestionPublished {
			ctrl.Phase = state.TaskPhaseAsk
		} else {
			ctrl.Phase = state.TaskPhaseSync
		}
	}
}

func applyAfterTool(ctrl *state.TaskControl, output RefToolOutcome, runID string) bool {
	switch {
	case output.Outcome == "needs_input":
		ctrl.Question = &state.Question{
			ID:       fmt.Sprintf("%s-correction-q%d", runID, ctrl.Tools),
			Kind:     "input",
			Text:     prompts.Runtime("read-only-question", nil),
			Revision: ctrl.Revision,
		}
	case output.Outcome == "needs_interaction" && output.InteractionID != "":
		ctrl.Question = &state.Question{
			ID:       output.InteractionID,
			Kind:     "input",
			Text:     output.Prompt,
			Revision: ctrl.Revision,
		}
	case output.Outcome == "uncertain" && output.EffectClass != "read":
		text := prompts.Runtime("uncertain-action-question", nil)
		if output.Unresolved != "" {
			text = output.Unresolved + prompts.Runtime("uncertain-action-followup", nil)
		}
		ctrl.Question = &state.Question{
			ID:       fmt.Sprintf("%s-operation-q%d", runID, ctrl.Tools),
			Kind:     "input",
			Text:     text,
			Revision: ctrl.Revision,
			Clears:   output.Clears,
		}
	default:
		return false
	}
	ctrl.QuestionPublished = false
	return true
}

func isPermanentTaskError(err error) bool {
	return errors.Is(err, errModelCheckpointConflict) ||
		errors.Is(err, errModelCheckpointInvalid) ||
		errors.Is(err, errOperationIdentityConflict) ||
		errors.Is(err, errOperationResultInvalid) ||
		errors.Is(err, errTranscriptBlockInvalid) ||
		errors.Is(err, errCallRefUnresolved) ||
		errors.Is(err, errCompletionProposalMissing)
}
