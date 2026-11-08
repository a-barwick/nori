package task

import (
	"context"
	"errors"
	"strings"

	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/transcript"
)

type CompletionInput struct {
	Step
	Ref, ID  string
	Position int64
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

// CheckCompletion preserves instruction ordering and reports unresolved operations without
// sending the model through a correction loop or asking the user to repair bookkeeping.
func (a *Activities) CheckCompletion(ctx context.Context, in CompletionInput) (out CompletionOutcome, err error) {
	ctx, span := startActivity(ctx, "task.completion.check", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.completion.check"); span.End() }()
	out.Ref = in.Ref
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
	if snap.Run.Revision != in.Revision || snap.Run.Position != in.Position {
		out.Action = "reconcile"
		result := transcript.ToolResultMessage(call.ID, call.Name, map[string]any{"accepted": false, "reason": prompts.Runtime("completion-instructions-changed-read-the-latest-instructions-befor", nil)})
		out.Ref, e = a.appendMessages(ctx, in.RunID, in.Ref, []transcript.Message{result})
		return out, classified(e)
	}
	ops, e := a.Store.Operations(ctx, in.RunID)
	if e != nil {
		return out, classified(e)
	}
	check := state.CompletionCheck{ID: in.ID, Revision: in.Revision, Position: in.Position, Proposal: proposal(call)}
	for _, op := range ops {
		switch op.Outcome {
		case "pending", "attempted", "ready", "uncertain", "":
			check.Proposal.Unmet = append(check.Proposal.Unmet, prompts.Runtime("operation-unresolved", map[string]any{"Name": op.Name}))
		}
	}
	if e = a.Store.PutCompletionCheck(ctx, in.RunID, check); e != nil {
		return out, classified(e)
	}
	out.Action = "publish"
	if len(check.Proposal.Unmet) > 0 {
		out.Action = "partial"
		out.Detail = prompts.Runtime("completion-incomplete", nil) + strings.Join(check.Proposal.Unmet, "; ")
	}
	return out, nil
}
