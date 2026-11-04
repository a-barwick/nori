package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go.temporal.io/sdk/activity"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"nori/internal/agent"
	"nori/internal/state"
	"nori/internal/transcript"
)

// Only immutable references and bounded control metadata cross Temporal history.
// The history chain itself retains every exact Responses item, including opaque
// reasoning, in private storage and is never reconstructed from a summary.
type HistoryInput struct {
	Step
	Ref      string
	Position int64
	Question *state.Question
	Action   *PendingAction
}
type HistorySync struct {
	Ref                string
	Revision, Position int64
	Grants             int
	QuestionResolved   bool
	Request            string
	// Others, Budget and CostUnknown are the family's shared accounting: what its other tasks have spent, its budget,
	// and whether any of it has an unknown cost.
	Others, Budget float64
	CostUnknown    bool
	// Subtask and ReadOnly are fixed when the task is accepted.
	Subtask, ReadOnly bool
	// Decision is the approve or decline command for the pending write. A later
	// instruction overrides an approval. ActionClosed is the outcome when an
	// instruction ended the write here, so its call already has a result.
	Decision     state.CommandKind
	DecisionID   string
	ActionClosed string
}
type CurrentPosition struct{ Revision, Position int64 }

func (a *Activities) CurrentPosition(ctx context.Context, id string) (out CurrentPosition, err error) {
	ctx, span := startActivity(ctx, "task.history.position", "CHAIN", a.activityRun(ctx, id), 0)
	defer func() { recordActivityFailure(ctx, err, "task.history.position"); span.End() }()
	snap, e := a.Reconcile(ctx, id)
	if e != nil {
		return out, e
	}
	return CurrentPosition{Revision: snap.Run.Revision, Position: snap.Run.Position}, nil
}

type RefModelInput struct {
	Step
	Ref     string
	Subtask bool
}
type CallRef struct {
	ID, Name string
	Index    int
}
type RefModelOutcome struct {
	Ref       string
	Calls     []CallRef
	Cost      float64
	CostKnown bool
	NotSent   bool
	Failure   string
	Invalid   bool
}
type RefToolInput struct {
	Step
	Ref, OperationID string
	Index            int
	ReadOnly         bool
}
type RefToolOutcome struct {
	Ref, Outcome, EffectClass string
	// Cost is spend the tool incurred outside a model decision.
	Cost                 float64
	CostUnknown          bool
	StartedJob, EndedJob string
	// Unresolved names the operations a question about this result is for, and Clears lists those an answer releases.
	Unresolved string
	Clears     []string
}
type RefInput struct {
	Step
	Ref      string
	Index    int
	ReadOnly bool
}
type AppendResult struct {
	Step
	Ref, CallID, Name string
	Response          map[string]any
}
type RefPublication struct {
	Publication
	Ref        string
	Unverified bool
}

func (a *Activities) evidence() (state.TaskEvidenceStore, error) {
	return a.Store, nil
}
func (a *Activities) appendMessages(ctx context.Context, id, parent string, messages []transcript.Message) (string, error) {
	store, e := a.evidence()
	if e != nil {
		return "", e
	}
	data, e := json.Marshal(messages)
	if e != nil {
		return "", e
	}
	return store.AppendHistory(ctx, id, parent, data)
}
func historyHeartbeat(ctx context.Context) func() {
	if !activity.IsActivity(ctx) {
		return func() {}
	}
	activity.RecordHeartbeat(ctx)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				activity.RecordHeartbeat(ctx)
			}
		}
	}()
	return func() { close(done) }
}
func (a *Activities) history(ctx context.Context, id, ref string) ([]transcript.Message, error) {
	stopHeartbeat := historyHeartbeat(ctx)
	defer stopHeartbeat()
	store, e := a.evidence()
	if e != nil {
		return nil, e
	}
	if ref == "" {
		return nil, nil
	}
	blocks, e := store.ReadHistory(ctx, id, ref)
	if e != nil {
		return nil, e
	}
	var messages []transcript.Message
	for _, block := range blocks {
		var chunk []transcript.Message
		if e = json.Unmarshal(block, &chunk); e != nil {
			return nil, errors.New("task transcript block is invalid")
		}
		messages = append(messages, chunk...)
	}
	return messages, nil
}

func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}
func lastCall(messages []transcript.Message, index int) (transcript.ToolCall, error) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != transcript.RoleModel {
			continue
		}
		calls := messages[i].ToolCalls()
		if index >= 0 && index < len(calls) {
			return calls[index], nil
		}
		break
	}
	return transcript.ToolCall{}, errors.New("saved model call reference does not resolve")
}
func (a *Activities) SyncHistory(ctx context.Context, in HistoryInput) (out HistorySync, err error) {
	ctx, span := startActivity(ctx, "task.history.sync", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.history.sync"); span.End() }()
	snap, e := a.Reconcile(ctx, in.RunID)
	if e != nil {
		return out, e
	}
	out = HistorySync{
		Ref: in.Ref, Revision: snap.Run.Revision, Position: in.Position, Request: clip(snap.Run.Request, 1500),
		Subtask: snap.Run.ParentID != "", ReadOnly: snap.Run.ReadOnly,
	}
	var parent *state.Run
	if snap.Run.ParentID != "" {
		owner, e := a.Store.GetRun(ctx, snap.Run.ParentID)
		if e != nil {
			return out, classified(e)
		}
		parent = &owner
	}
	out.Others, out.Budget, out.CostUnknown = state.FamilyCost(snap.Run, parent)
	if in.Ref == "" {
		background, e := a.Context(ctx, in.RunID)
		if e != nil {
			return out, e
		}
		if len(background) > 0 {
			out.Ref, e = a.appendMessages(ctx, in.RunID, "", background)
			if e != nil {
				return out, classified(e)
			}
		}
	}
	messages := []transcript.Message{}
	q := in.Question
	action := in.Action
	for _, cmd := range snap.Commands {
		if cmd.Position <= in.Position {
			continue
		}
		switch cmd.Kind {
		case state.CommandStart, state.CommandSteer:
			if action != nil {
				closed := map[string]any{"voided": true, "operation_id": action.Operation, "reason": "Austin changed the task instructions, so nothing was sent. Propose again if the write is still needed."}
				out.ActionClosed = "voided"
				if out.Decision == state.CommandDecline {
					closed, out.ActionClosed = map[string]any{"declined": true, "operation_id": action.Operation, "reason": declinedReason}, "declined"
				}
				messages = append(messages, transcript.ToolResultMessage(action.CallID, action.Name, closed))
				action, out.Decision, out.DecisionID = nil, "", ""
			}
			if q != nil && q.CallID != "" {
				superseded := map[string]any{"superseded": true, "reason": "Austin changed the task instructions."}
				if q.Kind == state.QuestionWork {
					superseded["reason"] = "Austin changed the task instructions, so this request is closed and any subtask started for it was stopped. Ask again if the work is still needed."
				}
				messages = append(messages, transcript.ToolResultMessage(q.CallID, questionTool(q), superseded))
			}
			q = nil
			out.QuestionResolved = true
			messages = append(messages, transcript.UserText(cmd.Text))
			if note := attachmentNote(cmd.Attachments); note != "" {
				messages = append(messages, transcript.UserText(note))
			}
			if cmd.Revises != "" {
				messages = append(messages, transcript.UserText("This task revises saved result "+cmd.Revises+". Read it with result_read, and with result_open if it's a file. "+
					"Your answer or first saved file becomes the new version, and the old one stays as history."))
			}
			if cmd.Kind == state.CommandStart {
				out.Request = clip(cmd.Text, 1500)
			}
		case state.CommandAnswer:
			if q != nil && q.ID == cmd.QuestionID {
				if e := a.clearAnswered(ctx, in.RunID, q); e != nil {
					return out, classified(e)
				}
				if q.CallID != "" {
					answer := map[string]any{"answer": cmd.Text, "question_id": cmd.QuestionID}
					messages = append(messages, transcript.ToolResultMessage(q.CallID, questionTool(q), answer))
				} else {
					messages = append(messages, transcript.UserText(cmd.Text))
				}
				if note := attachmentNote(cmd.Attachments); note != "" {
					messages = append(messages, transcript.UserText(note))
				}
				q = nil
				out.QuestionResolved = true
			}
		case state.CommandSubtaskResult:
			// A closed wait already has its call's one result.
			if q != nil && q.ID == cmd.QuestionID {
				result, e := a.subtaskResult(ctx, snap.Run, *q, cmd)
				if e != nil {
					return out, classified(e)
				}
				messages = append(messages, transcript.ToolResultMessage(q.CallID, questionTool(q), result))
				q = nil
				out.QuestionResolved = true
			}
		case state.CommandApprove, state.CommandDecline:
			if q != nil && q.ID == cmd.QuestionID && q.Kind == "approval" {
				q = nil
				out.QuestionResolved = true
				out.Decision, out.DecisionID = cmd.Kind, cmd.ID
			}
		case state.CommandContinue:
			if q != nil && q.ID == cmd.QuestionID && q.Kind == "budget" {
				out.Grants++
				q = nil
				out.QuestionResolved = true
				messages = append(messages, transcript.UserText(
					fmt.Sprintf("Austin approved another $%.2f of model spend. Continue the task.", agent.BudgetUSD)))
			}
		}
		out.Position = cmd.Position
	}
	if len(messages) > 0 {
		out.Ref, e = a.appendMessages(ctx, in.RunID, out.Ref, messages)
		if e != nil {
			return out, classified(e)
		}
	}
	return out, nil
}
func (a *Activities) ModelRef(ctx context.Context, in RefModelInput) (out RefModelOutcome, err error) {
	ctx, span := startActivity(ctx, "task.history.model", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.history.model"); span.End() }()
	messages, e := a.history(ctx, in.RunID, in.Ref)
	if e != nil {
		return out, classified(e)
	}
	outcome, e := a.Model(ctx, ModelInput{Step: in.Step, Messages: fitContext(messages), Interruptible: true, Subtask: in.Subtask})
	if e != nil {
		return out, e
	}
	response := outcome.Response
	out = RefModelOutcome{Ref: in.Ref, Cost: response.Cost, CostKnown: response.CostKnown, NotSent: response.NotSent, Failure: outcome.Failure}
	// Accounting is returned even if persisting the response fails. Activity errors
	// would discard this value, so persistence failure is an ordinary failure field.
	if response.Message != nil {
		out.Ref, e = a.appendMessages(ctx, in.RunID, in.Ref, []transcript.Message{*response.Message})
		if e != nil {
			out.Ref = in.Ref
			out.Failure = "model output could not be retained; paid request will not be replayed"
			return out, nil
		}
	}
	if out.Failure != "" {
		return out, nil
	}
	if len(response.Calls) == 0 {
		out.Invalid = true
		correction := transcript.UserText("Plain text does not finish this task. " +
			"Call complete(answer,evidence,unmet), or request_input for a missing prerequisite.")
		out.Ref, e = a.appendMessages(ctx, in.RunID, out.Ref, []transcript.Message{correction})
		if e != nil {
			out.Failure = "completion protocol correction could not be retained"
		}
		return out, nil
	}
	if e = agent.ValidateCalls(response.Calls, true, !in.Subtask); e != nil {
		parts := []transcript.Part{}
		for _, call := range response.Calls {
			rejected := map[string]any{"rejected": true, "reason": e.Error(), "executed": false}
			parts = append(parts, transcript.Part{ToolResult: &transcript.ToolResult{CallID: call.ID, Name: call.Name, Output: rejected}})
		}
		out.Invalid = true
		out.Ref, e = a.appendMessages(ctx, in.RunID, out.Ref, []transcript.Message{{Role: transcript.RoleUser, Parts: parts}})
		if e != nil {
			out.Failure = "rejected batch results could not be retained"
		}
		return out, nil
	}
	for i, call := range response.Calls {
		out.Calls = append(out.Calls, CallRef{ID: call.ID, Name: call.Name, Index: i})
	}
	span.SetAttributes(attribute.Int("nori.tool.call_count", len(out.Calls)))
	return out, nil
}
func (a *Activities) ToolRef(ctx context.Context, in RefToolInput) (out RefToolOutcome, err error) {
	ctx, span := startActivity(ctx, "task.history.tool", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.history.tool"); span.End() }()
	messages, e := a.history(ctx, in.RunID, in.Ref)
	if e != nil {
		return out, classified(e)
	}
	call, e := lastCall(messages, in.Index)
	if e != nil {
		return out, classified(e)
	}
	var result map[string]any
	if in.ReadOnly && toolEffect(call.Name) != "read" {
		result = map[string]any{
			"outcome":      "needs_input",
			"error":        "Completion correction permits reads only. Ask Austin before further writes or shell execution.",
			"effect_class": "read",
			"operation_id": in.OperationID,
		}
	} else {
		result, e = a.durableTool(ctx, ToolInput{Step: in.Step, OperationID: in.OperationID, Call: call})
		if e != nil {
			return out, e
		}
	}
	// The model receives the immutable result reference as well as the tool data.
	result["evidence_ref"] = in.OperationID
	out.Ref, e = a.appendMessages(ctx, in.RunID, in.Ref, []transcript.Message{transcript.ToolResultMessage(call.ID, call.Name, result)})
	if e != nil {
		return out, classified(errors.New("tool result transcript could not be retained; operation will not be reissued"))
	}
	out.Outcome, _ = result["outcome"].(string)
	out.EffectClass, _ = result["effect_class"].(string)
	out.Cost, _ = result["cost_usd"].(float64)
	out.CostUnknown, _ = result["cost_unknown"].(bool)
	out.StartedJob, out.EndedJob = jobTransition(call, result)
	if out.Outcome == "uncertain" && out.EffectClass != "read" {
		out.Unresolved, out.Clears, e = a.nameUnresolved(ctx, in.RunID, messages)
		if e != nil {
			return out, classified(e)
		}
	}
	return out, nil
}
func (a *Activities) InputRef(ctx context.Context, in RefInput) (out state.Question, err error) {
	ctx, span := startActivity(ctx, "task.history.question", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.history.question"); span.End() }()
	messages, e := a.history(ctx, in.RunID, in.Ref)
	if e != nil {
		return out, classified(e)
	}
	call, e := lastCall(messages, in.Index)
	if e != nil {
		return out, classified(e)
	}
	text, _ := call.Args["question"].(string)
	if call.Name == "request_work" {
		return a.workQuestion(ctx, in, call)
	}
	// A provider may explain a failure in output_text and put only the next
	// question in request_input. Publish both so the user can make a decision.
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != transcript.RoleModel {
			continue
		}
		var explanation []string
		for _, part := range messages[i].Parts {
			if strings.TrimSpace(part.Text) != "" {
				explanation = append(explanation, part.Text)
			}
		}
		prefix := strings.TrimSpace(strings.Join(explanation, "\n"))
		if room := min(4096, 8192-len(text)-5); prefix != "" && prefix != text && room > 0 {
			text = clip(prefix, room) + "\n\n" + text
		}
		break
	}
	named, clears, e := a.nameUnresolved(ctx, in.RunID, messages)
	if e != nil {
		return out, classified(e)
	}
	if named != "" {
		// The runtime's lines come first and stay whole, so a long model question is the part that's shortened.
		named = clip(named, 4096)
		text = named + "\n\n" + clip(text, state.MaxMessageBytes-len(named)-8)
	}
	return state.Question{Kind: "input", Text: text, CallID: call.ID, Revision: in.Revision, Clears: clears}, nil
}
func (a *Activities) AppendToolResult(ctx context.Context, in AppendResult) (ref string, err error) {
	ctx, span := startActivity(ctx, "task.history.append_result", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.history.append_result"); span.End() }()
	// A rejected completion can already have its result saved before a
	// publication loses a revision race. Keep exactly one output for each call.
	messages, e := a.history(ctx, in.RunID, in.Ref)
	if e != nil {
		return "", classified(e)
	}
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.ToolResult != nil && part.ToolResult.CallID == in.CallID {
				return in.Ref, nil
			}
		}
	}
	result := transcript.ToolResultMessage(in.CallID, in.Name, in.Response)
	ref, err = a.appendMessages(ctx, in.RunID, in.Ref, []transcript.Message{result})
	return ref, classified(err)
}
func (a *Activities) PublishRef(ctx context.Context, in RefPublication) (run state.Run, err error) {
	ctx, span := startActivity(ctx, "task.history.publish", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.history.publish"); span.End() }()
	if in.Ref != "" {
		in.ExpectedIncomplete = in.Status == "failed"
		messages, e := a.history(ctx, in.RunID, in.Ref)
		if e != nil {
			return state.Run{}, classified(e)
		}
		call, e := lastCall(messages, 0)
		if e != nil {
			return state.Run{}, classified(e)
		}
		in.Text, _ = call.Args["answer"].(string)
		proposed := proposal(call)
		in.Answer = &state.ResultDraft{Checked: in.Status == state.RunCompleted && !in.Unverified, Evidence: proposed.Evidence, Unmet: proposed.Unmet}
		if ops, e := a.Store.Operations(ctx, in.RunID); e == nil {
			in.Answer.Artifacts, in.Answer.Cited = citedOutputs(ops, proposed.Evidence)
		}
	}
	if in.Unverified {
		in.Text += "\n\nCompletion verification was unavailable; this answer is unverified."
	}
	if in.Status != "completed" && !in.Unverified && in.Text != "" && in.Detail != "" {
		in.Text += "\n\n" + in.Detail
	}
	run, e := a.Publish(ctx, in.Publication)
	return state.Run{ID: run.ID, Status: run.Status, Revision: run.Revision}, e
}
