package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"nori/internal/agent"
	"nori/internal/observability"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/transcript"
)

const (
	// A write's attempts cover workers that die. Each later attempt only reads the provider.
	maxActionAttempts = 3
	saveTimeout       = 10 * time.Second
)

var declinedReason = prompts.Runtime("declined", nil)

// PendingAction is a write's place in the workflow. Command is the approve command once
// the user has decided. The saved operation record holds the action itself.
type PendingAction struct {
	Operation, CallID, Name, Command string
}

type ActionPrepared struct {
	RefToolOutcome
	Question *state.Question
}

type ActionInput struct {
	Step
	Ref     string
	Action  PendingAction
	Outcome string // A decision that ends the write without sending it.
}

// settleAction applies the decision a sync found, then runs an approved write.
func (c *Checkpoint) settleAction(ctx, quick workflow.Context, id string, synced HistorySync) error {
	if c.Action == nil {
		return nil
	}
	var a *Activities
	step := Step{id, c.Revision}
	switch {
	case synced.ActionClosed != "":
		// The sync already gave the call its result, so only the record changes.
		err := workflow.ExecuteActivity(quick, a.CloseAction, ActionInput{Step: step, Action: *c.Action, Outcome: synced.ActionClosed}).Get(ctx, nil)
		if err != nil {
			return err
		}
		c.Action = nil
		return nil
	case synced.Decision == state.CommandDecline:
		var ref string
		err := workflow.ExecuteActivity(quick, a.CloseAction, ActionInput{Step: step, Ref: c.Ref, Action: *c.Action, Outcome: "declined"}).Get(ctx, &ref)
		if err != nil {
			return err
		}
		c.Ref, c.Action = ref, nil
		return nil
	case synced.Decision == state.CommandApprove:
		c.Action.Command = synced.DecisionID
	}
	if c.Action.Command == "" {
		return nil
	}
	var output RefToolOutcome
	var err error
	for attempt := 1; attempt <= maxActionAttempts; attempt++ {
		err = workflow.ExecuteActivity(ctx, a.RunAction, ActionInput{Step: step, Ref: c.Ref, Action: *c.Action}).Get(ctx, &output)
		var application *temporal.ApplicationError
		if err == nil || errors.As(err, &application) {
			break
		}
	}
	if err != nil {
		return err
	}
	c.Ref, c.Action = output.Ref, nil
	c.afterTool(output, id)
	return nil
}

func operationNamed(ops []state.OperationRecord, id string) (state.OperationRecord, error) {
	for _, op := range ops {
		if op.ID == id {
			return op, nil
		}
	}
	return state.OperationRecord{}, errors.New("the write's operation record is missing")
}

// settleEarlier rereads writes this task left uncertain with the same tool, so the
// records show what the provider shows before anything new is proposed.
func (a *Activities) settleEarlier(ctx context.Context, in ToolInput, ops []state.OperationRecord) error {
	for _, op := range ops {
		if op.Outcome != "uncertain" || op.Name != in.Call.Name || op.Action == nil {
			continue
		}
		outcome, output := a.Agent.CheckWrite(ctx, op.Name, op.Action.Spec)
		if outcome == agent.WriteUncertain {
			continue
		}
		if _, err := a.saveWrite(ctx, in.RunID, op, outcome, output); err != nil {
			return err
		}
	}
	return nil
}

// saveWrite saves the result, then the record that points at it, past any cancellation.
func (a *Activities) saveWrite(ctx context.Context, run string, op state.OperationRecord, outcome string, output map[string]any) (map[string]any, error) {
	save, cancel := context.WithTimeout(context.WithoutCancel(ctx), saveTimeout)
	defer cancel()
	if output == nil {
		output = map[string]any{}
	}
	output["operation_id"], output["outcome"], output["effect_class"] = op.ID, outcome, op.EffectClass
	data, err := json.Marshal(output)
	if err != nil {
		return nil, classified(err)
	}
	if op.ResultRef, err = a.Store.AppendHistory(save, run, "", data); err != nil {
		observability.RecordErrorCategory(save, "task.evidence", err)
		return nil, classified(err)
	}
	op.Outcome = outcome
	if err = a.Store.PutOperation(save, run, op); err != nil {
		observability.RecordErrorCategory(save, "task.evidence", err)
		return nil, classified(err)
	}
	// A late provider answer may have queued a notification after the workflow
	// already published Stop. Wake the outer loop even if this activity is cancelled.
	a.notify(save)
	return output, nil
}

func (a *Activities) writeResult(ctx context.Context, in Step, ref string, call transcript.ToolCall, output map[string]any) (RefToolOutcome, error) {
	output["evidence_ref"] = output["operation_id"]
	out := RefToolOutcome{}
	save, cancel := context.WithTimeout(context.WithoutCancel(ctx), saveTimeout)
	defer cancel()
	var err error
	out.Ref, err = a.appendMessages(save, in.RunID, ref, []transcript.Message{transcript.ToolResultMessage(call.ID, call.Name, output)})
	if err != nil {
		return out, classified(errors.New("write result transcript could not be retained; the write will not be repeated"))
	}
	out.Outcome, _ = output["outcome"].(string)
	out.EffectClass, _ = output["effect_class"].(string)
	return out, nil
}

// PrepareAction saves the exact action and returns the question that shows it. A write
// that needs no approval, because the provider already shows it or refused it, ends here.
func (a *Activities) PrepareAction(parent context.Context, in RefToolInput) (out ActionPrepared, err error) {
	ctx, done, run, err := a.bounded(parent, in.Step, false)
	if err != nil {
		return out, err
	}
	defer done()
	ctx, span := startActivity(ctx, "task.action.prepare", "TOOL", run, in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.action.prepare"); span.End() }()
	messages, err := a.history(ctx, in.RunID, in.Ref)
	if err != nil {
		return out, classified(err)
	}
	call, err := lastCall(messages, in.Index)
	if err != nil {
		return out, classified(err)
	}
	effect := agent.WriteEffect(call.Name)
	if in.ReadOnly {
		out.RefToolOutcome, err = a.writeResult(ctx, in.Step, in.Ref, call, map[string]any{
			"outcome": "needs_input", "operation_id": in.OperationID, "effect_class": "read",
			"error": prompts.Runtime("reads-only", nil),
		})
		return out, err
	}
	if err = a.Store.Admit(ctx, in.RunID, in.Revision, in.OperationID); err != nil {
		return out, classified(err)
	}
	op := state.OperationRecord{ID: in.OperationID, CallID: call.ID, Name: call.Name, Revision: in.Revision, EffectClass: effect, Outcome: "pending"}
	if err = a.Store.PutOperation(ctx, in.RunID, op); err != nil {
		return out, classified(err)
	}
	ops, err := a.Store.Operations(ctx, in.RunID)
	if err != nil {
		return out, classified(err)
	}
	if err = a.settleEarlier(ctx, ToolInput{Step: in.Step, OperationID: in.OperationID, Call: call}, ops); err != nil {
		return out, err
	}
	prepared, callErr := a.Agent.PrepareWrite(observability.SuppressErrorCapture(ctx), call.Name, call.Args)
	if callErr != nil {
		if ctx.Err() != nil {
			// A pause or Stop cut the read short. Nothing was sent, so close the record, and the saved task says what to do.
			if _, saveErr := a.saveWrite(ctx, in.RunID, op, agent.WriteFailed, map[string]any{"applied": false, "reason": "Interrupted before asking. Nothing was sent."}); saveErr != nil {
				return out, saveErr
			}
			check, cancel := context.WithTimeout(context.WithoutCancel(ctx), saveTimeout)
			defer cancel()
			if _, guardErr := a.Store.Guard(check, in.RunID, in.Revision); guardErr != nil {
				return out, classified(guardErr)
			}
			return out, a.ioError(in.Step, callErr)
		}
		output, saveErr := a.saveWrite(ctx, in.RunID, op, agent.WriteFailed, map[string]any{"applied": false, "error": traceText(callErr.Error())})
		if saveErr != nil {
			return out, saveErr
		}
		out.RefToolOutcome, err = a.writeResult(ctx, in.Step, in.Ref, call, output)
		return out, err
	}
	finish := func(outcome string, result map[string]any) (ActionPrepared, error) {
		output, saveErr := a.saveWrite(ctx, in.RunID, op, outcome, result)
		if saveErr != nil {
			return out, saveErr
		}
		out.RefToolOutcome, err = a.writeResult(ctx, in.Step, in.Ref, call, output)
		return out, err
	}
	if prepared.Result != nil {
		return finish(toolOutcome(prepared.Result), prepared.Result)
	}
	for _, earlier := range ops {
		if earlier.Outcome == "declined" && earlier.Action != nil && earlier.Action.Digest == prepared.Digest {
			return finish(agent.WriteFailed, map[string]any{"applied": false, "reason": prompts.Runtime("declined-exact", nil)})
		}
	}
	question := state.Question{ID: fmt.Sprintf("%s-%s", in.OperationID, prepared.Digest[:12]), Kind: "approval", Text: prepared.Text, Revision: in.Revision}
	op.Outcome, op.Action = "prepared", &state.Action{Digest: prepared.Digest, Text: prepared.Text, QuestionID: question.ID, Spec: prepared.Spec}
	save, cancel := context.WithTimeout(context.WithoutCancel(ctx), saveTimeout)
	defer cancel()
	if err = a.Store.PutOperation(save, in.RunID, op); err != nil {
		return out, classified(err)
	}
	out.Ref, out.Question = in.Ref, &question
	return out, nil
}

// RunAction sends an approved write once. Whatever brings it here again, including a
// worker that died mid-call, finds the approval used and only reads the provider.
func (a *Activities) RunAction(parent context.Context, in ActionInput) (out RefToolOutcome, err error) {
	stopHeartbeat := historyHeartbeat(parent)
	defer stopHeartbeat()
	ctx, span := startActivity(parent, "task.action.run", "TOOL", a.activityRun(parent, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.action.run"); span.End() }()
	messages, err := a.history(ctx, in.RunID, in.Ref)
	if err != nil {
		return out, classified(err)
	}
	call := transcript.ToolCall{ID: in.Action.CallID, Name: in.Action.Name}
	ops, err := a.Store.Operations(ctx, in.RunID)
	if err != nil {
		return out, classified(err)
	}
	op, err := operationNamed(ops, in.Action.Operation)
	if err != nil {
		return out, classified(err)
	}
	if op.ResultRef != "" {
		// A worker died after saving the result and before the history had it.
		blocks, readErr := a.Store.ReadHistory(ctx, in.RunID, op.ResultRef)
		if readErr != nil || len(blocks) != 1 {
			return out, classified(errors.New("saved write result is unavailable"))
		}
		var output map[string]any
		if err = json.Unmarshal(blocks[0], &output); err != nil {
			return out, classified(err)
		}
		return a.resultOnce(ctx, in, messages, call, output)
	}
	// The provider call outlives a Stop: a write that started finishes and is reported.
	provider := observability.SuppressErrorCapture(context.WithoutCancel(ctx))
	var outcome string
	var output map[string]any
	used, useErr := a.Store.UseApproval(ctx, in.RunID, in.Revision, op.ID, in.Action.Command)
	switch {
	case useErr == nil:
		op = used
		outcome, output = a.Agent.SendWrite(provider, op.Name, op.Action.Spec)
	case errors.Is(useErr, state.ErrApprovalUsed):
		outcome, output = a.Agent.CheckWrite(provider, op.Name, op.Action.Spec)
	default:
		return out, classified(useErr)
	}
	if output, err = a.saveWrite(ctx, in.RunID, op, outcome, output); err != nil {
		return out, err
	}
	return a.resultOnce(ctx, in, messages, call, output)
}

// resultOnce gives the call its one result, whichever attempt got here first.
func (a *Activities) resultOnce(ctx context.Context, in ActionInput, messages []transcript.Message, call transcript.ToolCall, output map[string]any) (RefToolOutcome, error) {
	outcome, _ := output["outcome"].(string)
	effect, _ := output["effect_class"].(string)
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.ToolResult != nil && part.ToolResult.CallID == call.ID {
				return RefToolOutcome{Ref: in.Ref, Outcome: outcome, EffectClass: effect}, nil
			}
		}
	}
	return a.writeResult(ctx, in.Step, in.Ref, call, output)
}

// CloseAction ends a write that was never sent. A decline also gives the call its result.
func (a *Activities) CloseAction(ctx context.Context, in ActionInput) (ref string, err error) {
	ops, err := a.Store.Operations(ctx, in.RunID)
	if err != nil {
		return "", classified(err)
	}
	op, err := operationNamed(ops, in.Action.Operation)
	if err != nil {
		return "", classified(err)
	}
	if in.Outcome == "voided" {
		op.Outcome = "voided"
		return "", classified(a.Store.PutOperation(ctx, in.RunID, op))
	}
	output, err := a.saveWrite(ctx, in.RunID, op, in.Outcome, map[string]any{"applied": false, "reason": declinedReason})
	if err != nil || in.Ref == "" {
		return "", err
	}
	out, err := a.writeResult(ctx, in.Step, in.Ref, transcript.ToolCall{ID: in.Action.CallID, Name: in.Action.Name}, output)
	return out.Ref, err
}
