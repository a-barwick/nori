package task

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/agent"
	"nori/internal/extensions"
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

func (s *runSession) stepModel() stepResult {
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
	model, err := s.act.ModelRef(mCtx, RefModelInput{
		Step:    Step{RunID: s.runID, Revision: s.ctrl.Revision},
		ModelID: s.ctrl.PendingModelID,
		Ref:     s.ctrl.Ref,
		Subtask: s.ctrl.Subtask,
	})
	mCancel()
	if res, stopped := s.checkStopped(); stopped {
		return res
	}
	if err != nil {
		return s.handleModelErr(model, err)
	}
	return s.commitModelOutcome(model)
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
		return s.backoff(err)
	}
}

func (s *runSession) commitModelOutcome(model RefModelOutcome) stepResult {
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

func (s *runSession) stepCalls() stepResult {
	if s.ctrl.CallIndex >= len(s.ctrl.PendingCalls) {
		next := s.ctrl
		clearPendingCalls(&next)
		if next.Question != nil && !next.QuestionPublished {
			next.Phase = state.TaskPhaseAsk
		} else {
			next.Phase = state.TaskPhaseSync
		}
		return s.saveCtrl(next)
	}
	if err := s.ensureAccounting(s.watcher.ctx); err != nil {
		if res, lost := s.checkLeaseLost(); lost {
			return res
		}
		return s.backoff(err)
	}

	call := s.ctrl.PendingCalls[s.ctrl.CallIndex]
	step := Step{RunID: s.runID, Revision: s.ctrl.Revision}

	if s.ctrl.CallsChanged || s.ctrl.CallsRevised {
		return s.appendSkippedCall(step, call, map[string]any{
			"skipped": true,
			"reason":  prompts.Runtime("workflow-accepted-input-changed-before-this-call-ran", nil),
		}, s.ctrl, false, false)
	}

	if call.Name != "complete" && s.acct.budget > 0 && s.acct.spent+s.acct.others+s.ctrl.EstimatedUnknownUSD >= s.acct.budget {
		return s.appendSkippedCall(step, call, map[string]any{
			"skipped": true,
			"reason":  prompts.Runtime("spending-limit", map[string]any{"Budget": s.acct.budget}),
		}, s.ctrl, false, false)
	}
	if call.Name != "complete" && isPaidTool(agent.PolicyFor(call.Name, call.Exec)) && s.ctrl.PaidTools >= defaultMaxPaidTools {
		return s.appendSkippedCall(step, call, map[string]any{
			"skipped": true,
			"reason":  fmt.Sprintf("This task reached its limit of %d paid tool calls.", defaultMaxPaidTools),
		}, s.ctrl, false, false)
	}

	switch call.Name {
	case "request_input", "request_work":
		return s.runInputCall(step, call)
	case "complete":
		return s.runCompleteCall(step, call)
	default:
		if batch := s.parallelReadBatch(); len(batch) > 1 {
			return s.runParallelReadBatch(step, batch)
		}
		return s.runToolCall(step, call)
	}
}

const maxParallelReads = 4

func isParallelSafeReadCall(call state.TaskCallRef) bool {
	switch call.Name {
	case "complete", "request_input", "request_work", "compose", "capability_load", "upload_inspect":
		return false
	}
	policy := agent.PolicyFor(call.Name, call.Exec)
	if policy.Effect != "read" || policy.InterruptedCostUnknown {
		return false
	}
	if call.Exec == nil || call.Exec.Builtin {
		return policy.Runtime
	}
	if call.Exec.Binding.Adapter == extensions.AdapterNative &&
		strings.HasPrefix(call.Exec.Binding.Target, "nori.") {
		return true
	}
	return false
}

func (s *runSession) parallelReadBatch() []state.TaskCallRef {
	start := s.ctrl.CallIndex
	if start >= len(s.ctrl.PendingCalls) {
		return nil
	}
	var batch []state.TaskCallRef
	var reservedCost float64
	paidCount := 0
	for i := start; i < len(s.ctrl.PendingCalls) && len(batch) < maxParallelReads; i++ {
		c := s.ctrl.PendingCalls[i]
		if !isParallelSafeReadCall(c) {
			break
		}
		policy := agent.PolicyFor(c.Name, c.Exec)
		if isPaidTool(policy) {
			if s.ctrl.PaidTools+paidCount+1 > defaultMaxPaidTools {
				break
			}
			if s.acct.budget > 0 && s.acct.spent+s.acct.others+s.ctrl.EstimatedUnknownUSD+reservedCost+policy.InterruptedCostUSD >= s.acct.budget {
				break
			}
			paidCount++
			reservedCost += policy.InterruptedCostUSD
		}
		batch = append(batch, c)
	}
	return batch
}

func (s *runSession) runParallelReadBatch(step Step, batch []state.TaskCallRef) stepResult {
	items := make([]RefToolBatchItem, len(batch))
	names := make([]string, len(batch))
	for i, call := range batch {
		items[i] = RefToolBatchItem{
			OperationID: fmt.Sprintf("%s-m%d-t%d", s.runID, s.ctrl.Models, s.ctrl.Tools+1+i),
			Index:       call.Index,
			Exec:        call.Exec,
		}
		names[i] = call.Name
	}
	pCtx, pCancel := s.stepCtx(stepTimeoutQuick)
	err := s.act.Project(pCtx, Projection{Step: step, Kind: "tool", Text: names[0]})
	pCancel()
	if err != nil {
		if res, stopped := s.checkStopped(); stopped {
			return res
		}
		switch {
		case errors.Is(err, state.ErrSuperseded), errors.Is(err, state.ErrPaused):
			next := s.ctrl
			next.Tools++
			return s.appendSkippedCall(step, batch[0], map[string]any{
				"skipped": true,
				"reason":  prompts.Runtime("workflow-execution-admission-changed-before-this-call-ran", nil),
			}, next, true, errors.Is(err, state.ErrPaused))
		default:
			return s.handleStepErr(err, false)
		}
	}

	tCtx, tCancel := s.stepCtx(stepTimeoutLong)
	out, err := s.act.ToolBatchRef(tCtx, RefToolBatchInput{
		Step:     step,
		Ref:      s.ctrl.Ref,
		ReadOnly: s.ctrl.Inherited,
		Items:    items,
	})
	tCancel()
	if res, stopped := s.checkStopped(); stopped {
		return res
	}
	if err != nil {
		switch {
		case errors.Is(err, state.ErrSuperseded), errors.Is(err, state.ErrPaused):
			next := s.ctrl
			next.CallsRevised = true
			if res := s.saveCtrl(next); res.done {
				return res
			}
			if errors.Is(err, state.ErrPaused) {
				return s.handleMidRunPaused()
			}
			return stepContinue()
		case errors.Is(err, state.ErrCancelled):
			return s.transitionFailure(state.RunCancelled, state.ErrCancelled.Error(), true, nil)
		case errors.Is(err, state.ErrBusy):
			return s.handleBusy()
		default:
			if isPermanentTaskError(err) {
				return s.transitionFailure(state.RunFailed, err.Error(), false, nil)
			}
			return s.backoff(err)
		}
	}

	next := s.ctrl
	next.Ref = out.Ref
	next.Tools += len(batch)
	loopDetected := false
	loopToolName := ""
	for i, itemOut := range out.Items {
		policy := agent.PolicyFor(batch[i].Name, batch[i].Exec)
		if isPaidTool(policy) || itemOut.Cost > 0 || itemOut.CostUnknown {
			next.PaidTools++
		}
		if itemOut.CostUnknown {
			next.EstimatedUnknownUSD += unknownModelStepExposureUSD
		}
		if applyAfterTool(&next, itemOut, s.runID) {
			next.CallsRevised = true
		}
		var tripped bool
		next.RecentToolSigs, tripped = appendToolSig(next.RecentToolSigs, itemOut.ToolSig)
		if tripped {
			loopDetected = true
			loopToolName = batch[i].Name
		}
	}
	for range batch {
		advanceCallCursor(&next)
	}

	var spend *state.SpendUpdate
	if out.TotalCost > 0 || out.CostUnknown {
		spend = &state.SpendUpdate{
			RunID:       s.runID,
			Spent:       s.acct.spent + out.TotalCost,
			Budget:      s.acct.budget,
			CostUnknown: s.acct.costUnknown || out.CostUnknown,
		}
	}
	if loopDetected {
		detail := fmt.Sprintf("This task repeated the same %s tool call without making progress and stopped to prevent a runaway loop.", loopToolName)
		clearPendingCalls(&next)
		next.PendingPublication = &state.TaskPublication{
			RunID:              s.runID,
			Revision:           s.ctrl.Revision,
			Status:             state.RunFailed,
			Detail:             detail,
			Text:               detail,
			StopConfirmed:      true,
			ExpectedIncomplete: true,
			Spent:              s.acct.spent + out.TotalCost,
			CostUnknown:        s.acct.costUnknown || out.CostUnknown,
			BypassRouting:      false,
		}
		next.Phase = state.TaskPhasePublish
		return s.advanceStep(next, spend)
	}
	return s.advanceStep(next, spend)
}

func (s *runSession) appendSkippedCall(
	step Step,
	call state.TaskCallRef,
	resp map[string]any,
	next state.TaskControl,
	revise bool,
	paused bool,
) stepResult {
	aCtx, aCancel := s.stepCtx(stepTimeoutQuick)
	ref, err := s.act.AppendToolResult(aCtx, AppendResult{
		Step:     step,
		Ref:      next.Ref,
		CallID:   call.ID,
		Name:     call.Name,
		Response: resp,
	})
	aCancel()
	if err != nil {
		if res, stopped := s.checkStopped(); stopped {
			return res
		}
		if isPermanentTaskError(err) {
			return s.transitionFailure(state.RunFailed, err.Error(), false, nil)
		}
		return s.backoff(err)
	}
	next.Ref = ref
	if revise {
		next.CallsRevised = true
	}
	advanceCallCursor(&next)
	if res := s.saveCtrl(next); res.done {
		return res
	}
	if paused {
		return s.handleMidRunPaused()
	}
	return stepContinue()
}

func (s *runSession) runInputCall(step Step, call state.TaskCallRef) stepResult {
	iCtx, iCancel := s.stepCtx(stepTimeoutQuick)
	q, err := s.act.InputRef(iCtx, RefInput{Step: step, Ref: s.ctrl.Ref, Index: call.Index, ReadOnly: s.ctrl.Inherited})
	iCancel()
	if err != nil {
		if res, stopped := s.checkStopped(); stopped {
			return res
		}
		if isPermanentTaskError(err) {
			return s.transitionFailure(state.RunFailed, err.Error(), false, nil)
		}
		return s.backoff(err)
	}
	q.ID = fmt.Sprintf("%s-question-m%d", s.runID, s.ctrl.Models)
	if call.Name == "request_work" {
		q.ID = fmt.Sprintf("%s-work-m%d", s.runID, s.ctrl.Models)
	}
	next := s.ctrl
	next.Question = &q
	next.QuestionPublished = false
	clearPendingCalls(&next)
	next.Phase = state.TaskPhaseAsk
	return s.saveCtrl(next)
}

func (s *runSession) runCompleteCall(step Step, call state.TaskCallRef) stepResult {
	checkID := fmt.Sprintf("%s-check-%d", s.runID, s.ctrl.Checks+1)
	cCtx, cCancel := s.stepCtx(stepTimeoutQuick)
	check, err := s.act.CheckCompletion(cCtx, CompletionInput{
		Step: step, Ref: s.ctrl.Ref, ID: checkID, Position: s.ctrl.Position,
	})
	cCancel()
	if err != nil {
		if res, stopped := s.checkStopped(); stopped {
			return res
		}
		switch {
		case errors.Is(err, state.ErrSuperseded), errors.Is(err, state.ErrPaused):
			next := s.ctrl
			next.Checks++
			return s.appendSkippedCall(step, call, map[string]any{
				"accepted": false,
				"reason":   prompts.Runtime("workflow-execution-admission-changed-during-completion-checking", nil),
			}, next, true, errors.Is(err, state.ErrPaused))
		default:
			return s.handleStepErr(err, true)
		}
	}

	next := s.ctrl
	next.Checks++
	next.Ref = check.Ref
	clearPendingCalls(&next)
	switch check.Action {
	case "publish", "partial":
		status := state.RunCompleted
		if check.Action != "publish" {
			status = state.RunFailed
		}
		callCopy := call
		next.PendingPublication = &state.TaskPublication{
			RunID:          s.runID,
			Revision:       s.ctrl.Revision,
			Status:         status,
			Detail:         check.Detail,
			Ref:            next.Ref,
			StopConfirmed:  true,
			Spent:          s.acct.spent,
			CostUnknown:    s.acct.costUnknown,
			CompletionCall: &callCopy,
		}
		next.Phase = state.TaskPhasePublish
	case "reconcile":
		next.Phase = state.TaskPhaseSync
	default:
		return s.transitionFailure(state.RunFailed, "completion check returned an invalid decision", false, nil)
	}
	return s.saveCtrl(next)
}

func (s *runSession) runToolCall(step Step, call state.TaskCallRef) stepResult {
	operation := fmt.Sprintf("%s-m%d-t%d", s.runID, s.ctrl.Models, s.ctrl.Tools+1)
	pCtx, pCancel := s.stepCtx(stepTimeoutQuick)
	err := s.act.Project(pCtx, Projection{Step: step, Kind: "tool", Text: call.Name})
	pCancel()
	if err != nil {
		if res, stopped := s.checkStopped(); stopped {
			return res
		}
		switch {
		case errors.Is(err, state.ErrSuperseded), errors.Is(err, state.ErrPaused):
			next := s.ctrl
			next.Tools++
			return s.appendSkippedCall(step, call, map[string]any{
				"skipped": true,
				"reason":  prompts.Runtime("workflow-execution-admission-changed-before-this-call-ran", nil),
			}, next, true, errors.Is(err, state.ErrPaused))
		default:
			return s.handleStepErr(err, false)
		}
	}

	policy := agent.PolicyFor(call.Name, call.Exec)
	tCtx, tCancel := s.stepCtx(stepTimeoutLong)
	output, err := s.act.ToolRef(tCtx, RefToolInput{
		Step:        step,
		Ref:         s.ctrl.Ref,
		OperationID: operation,
		Index:       call.Index,
		ReadOnly:    s.ctrl.Inherited,
		Exec:        call.Exec,
	})
	tCancel()
	if res, stopped := s.checkStopped(); stopped {
		return res
	}
	if err != nil {
		switch {
		case errors.Is(err, state.ErrSuperseded), errors.Is(err, state.ErrPaused):
			next := s.ctrl
			next.Tools++
			return s.appendSkippedCall(step, call, map[string]any{
				"skipped":      true,
				"outcome":      "skipped",
				"operation_id": operation,
				"reason":       prompts.Runtime("workflow-execution-admission-changed-before-this-call-ran", nil),
			}, next, true, errors.Is(err, state.ErrPaused))
		case errors.Is(err, state.ErrCancelled):
			return s.transitionFailure(state.RunCancelled, state.ErrCancelled.Error(), true, nil)
		case errors.Is(err, state.ErrBusy):
			return s.handleBusy()
		default:
			if isPermanentTaskError(err) {
				var failSpend *state.SpendUpdate
				if policy.InterruptedCostUSD > 0 || policy.InterruptedCostUnknown {
					failSpend = &state.SpendUpdate{
						RunID:       s.runID,
						Spent:       s.acct.spent + policy.InterruptedCostUSD,
						Budget:      s.acct.budget,
						CostUnknown: s.acct.costUnknown || policy.InterruptedCostUnknown,
					}
				}
				return s.transitionFailure(state.RunFailed, err.Error(), false, failSpend)
			}
			return s.backoff(err)
		}
	}

	next := s.ctrl
	next.Ref = output.Ref
	next.Tools++
	if isPaidTool(policy) || output.Cost > 0 || output.CostUnknown {
		next.PaidTools++
	}
	if output.CostUnknown {
		next.EstimatedUnknownUSD += unknownModelStepExposureUSD
	}
	if applyAfterTool(&next, output, s.runID) {
		next.CallsRevised = true
	}
	advanceCallCursor(&next)

	var spend *state.SpendUpdate
	if output.Cost > 0 || output.CostUnknown {
		spend = &state.SpendUpdate{
			RunID:       s.runID,
			Spent:       s.acct.spent + output.Cost,
			Budget:      s.acct.budget,
			CostUnknown: s.acct.costUnknown || output.CostUnknown,
		}
	}
	var loopDetected bool
	next.RecentToolSigs, loopDetected = appendToolSig(next.RecentToolSigs, output.ToolSig)
	if loopDetected {
		detail := fmt.Sprintf("This task repeated the same %s tool call without making progress and stopped to prevent a runaway loop.", call.Name)
		clearPendingCalls(&next)
		next.PendingPublication = &state.TaskPublication{
			RunID:              s.runID,
			Revision:           s.ctrl.Revision,
			Status:             state.RunFailed,
			Detail:             detail,
			Text:               detail,
			StopConfirmed:      true,
			ExpectedIncomplete: true,
			Spent:              s.acct.spent + output.Cost,
			CostUnknown:        s.acct.costUnknown || output.CostUnknown,
			BypassRouting:      false,
		}
		next.Phase = state.TaskPhasePublish
		return s.advanceStep(next, spend)
	}
	return s.advanceStep(next, spend)
}

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
