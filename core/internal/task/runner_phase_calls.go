package task

import (
	"errors"
	"fmt"
	"strings"

	"nori/internal/agent"
	"nori/internal/extensions"
	"nori/internal/prompts"
	"nori/internal/state"
)

const maxParallelReads = 4

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
