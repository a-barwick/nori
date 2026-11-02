package task

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"nori/internal/state"
	"nori/internal/transcript"
)

type CompletionInput struct {
	Step
	Ref, ID           string
	Position          int64
	CorrectionAttempt int
}
type CompletionOutcome struct{ Ref, Action, Detail string }

func proposal(call transcript.ToolCall) state.CompletionProposal {
	p := state.CompletionProposal{}
	p.Answer, _ = call.Args["answer"].(string)
	for _, field := range []string{"evidence", "unmet"} {
		values, _ := call.Args[field].([]any)
		for _, value := range values {
			text, _ := value.(string)
			if field == "evidence" {
				p.Evidence = append(p.Evidence, text)
			} else {
				p.Unmet = append(p.Unmet, text)
			}
		}
	}
	return p
}

// citable lists the outcomes a saved result can show. Voided and prepared writes sent nothing and have no result.
func citable(outcome string) bool {
	switch outcome {
	case "confirmed", "submitted", "failed", "skipped", "declined":
		return true
	}
	return false
}

func completionFailures(
	messages []transcript.Message, ops []state.OperationRecord, p state.CompletionProposal,
	revision, position int64, snap state.TaskSnapshot,
) ([]string, bool) {
	failures := []string{}
	writes := false
	if snap.Run.Revision != revision || snap.Run.Position != position {
		failures = append(failures, "Accepted instructions changed or remain unread.")
	}
	responses := map[string]bool{}
	calls := map[string]string{}
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.ToolCall != nil {
				calls[part.ToolCall.ID] = part.ToolCall.Name
			}
			if part.ToolResult != nil {
				responses[part.ToolResult.CallID] = true
			}
		}
	}
	for id, name := range calls {
		if name != "complete" && !responses[id] {
			failures = append(failures, "A tool call has no saved result.")
		}
	}
	refs := map[string]bool{}
	resolved := false
	for _, op := range ops {
		if op.EffectClass != "read" {
			writes = true
		}
		// Listing an uncertain write as unmet is the way out: the task ends incomplete and the record stays uncertain.
		if op.Outcome == "pending" || op.Outcome == "attempted" || op.Outcome == "" || (op.Outcome == "uncertain" && len(p.Unmet) == 0) {
			failures = append(failures, "An attempted operation has an unresolved outcome.")
		}
		// A request_work result is always a saved result, even when a subtask left a write unresolved. That unresolved
		// write must still be listed as unmet.
		if (citable(op.Outcome) || op.Name == "request_work") && op.ResultRef != "" {
			resolved = true
			refs[op.ID] = true
			refs[op.ResultRef] = true
			for _, id := range op.ArtifactIDs {
				refs[id] = true
			}
		}
	}
	if resolved && len(p.Evidence) == 0 {
		failures = append(failures, "The proposal must cite saved evidence for observed tool results.")
	}
	for _, ref := range p.Evidence {
		if !refs[ref] {
			failures = append(failures, "A claimed evidence reference does not resolve to a saved terminal tool result.")
		}
	}
	return failures, writes
}
func (a *Activities) CheckCompletion(ctx context.Context, in CompletionInput) (out CompletionOutcome, err error) {
	ctx, span := startActivity(ctx, "task.completion.check", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	check := state.CompletionCheck{}
	defer func() {
		span.SetAttributes(attribute.Int("nori.completion.failure_count", len(check.RecordFailures)))
		recordActivityFailure(ctx, err, "task.completion.check")
		span.End()
	}()
	out.Ref = in.Ref
	store, e := a.evidence()
	if e != nil {
		return out, classified(e)
	}
	messages, e := a.history(ctx, in.RunID, in.Ref)
	if e != nil {
		return out, classified(e)
	}
	call, e := lastCall(messages, 0)
	if e != nil || call.Name != "complete" {
		return out, classified(errors.New("completion proposal is missing"))
	}
	snap, e := a.Store.Reconcile(ctx, in.RunID)
	if e != nil {
		return out, classified(e)
	}
	if snap.Run.CancelRequested {
		return out, classified(state.ErrCancelled)
	}
	ops, e := store.Operations(ctx, in.RunID)
	if e != nil {
		return out, classified(e)
	}
	check = state.CompletionCheck{ID: in.ID, Revision: in.Revision, Position: in.Position, Proposal: proposal(call), CorrectionAttempt: in.CorrectionAttempt}
	var writes bool
	check.RecordFailures, writes = completionFailures(messages, ops, check.Proposal, in.Revision, in.Position, snap)
	// Check actual private blobs, not merely strings that look like references.
	cited := map[string]bool{}
	for _, ref := range check.Proposal.Evidence {
		cited[ref] = true
	}
	for _, op := range ops {
		if citable(op.Outcome) {
			if op.ResultRef == "" {
				check.RecordFailures = append(check.RecordFailures, "A resolved operation has no saved result.")
			} else {
				blocks, readErr := store.ReadHistory(ctx, in.RunID, op.ResultRef)
				if readErr != nil {
					check.RecordFailures = append(check.RecordFailures, "A saved operation result is unavailable.")
					continue
				}
				relevant := cited[op.ID] || cited[op.ResultRef]
				for _, id := range op.ArtifactIDs {
					relevant = relevant || cited[id]
				}
				if relevant {
					for _, block := range blocks {
						var result any
						if json.Unmarshal(block, &result) != nil {
							check.RecordFailures = append(check.RecordFailures, "A cited tool result is not readable.")
							break
						}
					}
				}
			}
		}
	}
	if e = store.PutCompletionCheck(ctx, in.RunID, check); e != nil {
		return out, classified(e)
	}
	if len(check.RecordFailures) == 0 {
		// The outer loop compares the answer with the cited results when it reports the end.
		if len(check.Proposal.Unmet) > 0 {
			out.Action = "partial"
			out.Detail = "Incomplete. The task reported these items as unmet: " + strings.Join(check.Proposal.Unmet, "; ")
			return out, nil
		}
		out.Action = "publish"
		return out, nil
	}
	if snap.Run.Revision != in.Revision || snap.Run.Position != in.Position {
		out.Action = "reconcile"
		changed := map[string]any{
			"accepted": false,
			"reason":   "Instructions changed. Reconcile all later instructions before proposing completion again.",
		}
		result := transcript.ToolResultMessage(call.ID, call.Name, changed)
		out.Ref, e = a.appendMessages(ctx, in.RunID, in.Ref, []transcript.Message{result})
		return out, classified(e)
	}
	feedback := map[string]any{"accepted": false, "record_failures": check.RecordFailures}
	if writes {
		out.Action = "ask"
		out.Detail = "The task's records could not be checked: " + strings.Join(check.RecordFailures, " ") + " " +
			"This task attempted a write or an action with unknown effects. " +
			"Please clarify what remains; Nori will not repeat that action automatically."
		feedback["instruction"] = "Ask Austin before further action; never repeat an attempted write automatically."
	} else if in.CorrectionAttempt >= 2 {
		out.Action = "partial"
		out.Detail = "Incomplete: the task's records still failed their checks after two read-only corrections. " +
			"This proposed answer is partial and the task is not completed."
	} else {
		out.Action = "correct"
		feedback["instruction"] = "Correct the answer using read-only tools only, then propose complete again. " +
			"Do not run shell commands or writes. Use request_input if another action is required."
	}
	out.Ref, e = a.appendMessages(ctx, in.RunID, in.Ref, []transcript.Message{transcript.ToolResultMessage(call.ID, call.Name, feedback)})
	return out, classified(e)
}
