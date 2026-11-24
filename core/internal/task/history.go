package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"nori/internal/agent"
	"nori/internal/extensions"
	"nori/internal/observability"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/transcript"
)

// The history chain retains every exact Responses item, including opaque
// reasoning, in private storage and is never reconstructed from a summary.
type HistoryInput struct {
	Step
	Ref      string
	Position int64
	Question *state.Question
}
type HistorySync struct {
	Ref                string
	Revision, Position int64
	QuestionResolved   bool
	Request            string
	// Spent is this task's own persisted spend; Others, Budget and CostUnknown are the family's shared accounting:
	// what its other tasks have spent, its budget, and whether any of it has an unknown cost.
	Spent, Others, Budget float64
	CostUnknown           bool
	// Subtask and ReadOnly are fixed when the task is accepted.
	Subtask, ReadOnly bool
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
	ModelID string
	Ref     string
	Subtask bool
}
type CallRef struct {
	ID, Name string
	Index    int
	// Exec pins the definition, binding and policy the call was offered under. Histories recorded before pinning
	// have none and use the engine's name-based policy, so they replay unchanged.
	Exec *extensions.Exec `json:",omitempty"`
}
type RefModelOutcome struct {
	Ref       string
	Calls     []CallRef
	Cost      float64
	CostKnown bool
	NotSent   bool
	Failure   string
	Invalid   bool
	// Interrupted marks a request whose worker stopped before its result was saved; the task may ask again.
	Interrupted bool `json:",omitempty"`
}
type RefToolInput struct {
	Step
	Ref, OperationID string
	Index            int
	ReadOnly         bool
	Exec             *extensions.Exec `json:",omitempty"`
}
type RefToolOutcome struct {
	Ref, Outcome, EffectClass string
	// Cost is spend the tool incurred outside a model decision.
	Cost        float64
	CostUnknown bool
	// Unresolved names the operations a question about this result is for, and Clears lists those an answer releases.
	Unresolved string
	Clears     []string
	// InteractionID and Prompt are set when a registry tool waits on the user's approval or account choice.
	InteractionID string `json:",omitempty"`
	Prompt        string `json:",omitempty"`
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
	Ref string
}

var (
	errTranscriptBlockInvalid = errors.New("task transcript block is invalid")
	errCallRefUnresolved      = errors.New("saved model call reference does not resolve")
)

func (a *Activities) appendMessages(ctx context.Context, id, parent string, messages []transcript.Message) (string, error) {
	data, e := json.Marshal(messages)
	if e != nil {
		return "", e
	}
	return a.Store.AppendHistory(ctx, id, parent, data)
}
func (a *Activities) history(ctx context.Context, id, ref string) ([]transcript.Message, error) {
	if ref == "" {
		return nil, nil
	}
	blocks, e := a.Store.ReadHistory(ctx, id, ref)
	if e != nil {
		return nil, e
	}
	var messages []transcript.Message
	for _, block := range blocks {
		var chunk []transcript.Message
		decoder := json.NewDecoder(bytes.NewReader(block))
		decoder.DisallowUnknownFields()
		if e = decoder.Decode(&chunk); e != nil {
			return nil, errTranscriptBlockInvalid
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
	return transcript.ToolCall{}, errCallRefUnresolved
}
func (a *Activities) SyncHistory(ctx context.Context, in HistoryInput) (out HistorySync, err error) {
	ctx, span := startActivity(ctx, "task.history.sync", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.history.sync"); span.End() }()
	// A saved result may settle during pause, but its parent must not consume
	// the delivery until resume. Completion/publication of an admitted reply
	// uses separate steps and remains allowed.
	control, e := a.Store.Control(ctx)
	if e != nil {
		return out, e
	}
	if e = control.Check(); e != nil {
		return out, e
	}
	snap, e := a.Reconcile(ctx, in.RunID)
	if e != nil {
		return out, e
	}
	out = HistorySync{
		Ref: in.Ref, Revision: snap.Run.Revision, Position: in.Position, Request: clip(snap.Run.Request, 1500),
		Spent: snap.Run.Spent, Subtask: snap.Run.ParentID != "", ReadOnly: snap.Run.ReadOnly,
	}
	var parent *state.Run
	if snap.Run.ParentID != "" {
		owner, e := a.Store.GetRun(ctx, snap.Run.ParentID)
		if e != nil {
			return out, e
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
				return out, e
			}
		}
	}
	messages := []transcript.Message{}
	q := in.Question
	for _, cmd := range snap.Commands {
		if cmd.Position <= in.Position {
			continue
		}
		switch cmd.Kind {
		case state.CommandStart, state.CommandSteer:
			if q != nil && q.CallID != "" {
				superseded := map[string]any{"superseded": true, "reason": prompts.Runtime("superseded", nil)}
				if q.Kind == state.QuestionWork {
					superseded["reason"] = prompts.Runtime("request-closed", nil)
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
				messages = append(messages, transcript.UserText(prompts.Runtime("revise-result", map[string]any{"Result": cmd.Revises})))
			}
			if cmd.Kind == state.CommandStart {
				out.Request = clip(cmd.Text, 1500)
			}
		case state.CommandAnswer:
			if q != nil && q.ID == cmd.QuestionID {
				if e := a.clearAnswered(ctx, in.RunID, q); e != nil {
					return out, e
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
					return out, e
				}
				messages = append(messages, transcript.ToolResultMessage(q.CallID, questionTool(q), result))
				q = nil
				out.QuestionResolved = true
			}
		}
		out.Position = cmd.Position
	}
	if len(messages) > 0 {
		out.Ref, e = a.appendMessages(ctx, in.RunID, out.Ref, messages)
		if e != nil {
			return out, e
		}
	}
	for _, cmd := range snap.Commands {
		if !cmd.Delivered {
			if e = a.Store.Delivered(ctx, cmd.ID); e != nil {
				return out, e
			}
		}
	}
	return out, nil
}
func (a *Activities) ModelRef(ctx context.Context, in RefModelInput) (out RefModelOutcome, err error) {
	ctx, span := startActivity(ctx, "task.history.model", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.history.model"); span.End() }()
	modelID := in.ModelID
	if modelID == "" {
		modelID = uuid.NewString()
	}
	checkpoint := state.TaskModelCheckpoint{ID: modelID, Revision: in.Revision, Parent: in.Ref, Subtask: in.Subtask}
	if _, readErr := a.Store.TaskModelCheckpoint(ctx, in.RunID, modelID); readErr == nil {
		return a.recoverModelRef(ctx, in.RunID, checkpoint)
	} else if !errors.Is(readErr, state.ErrNotFound) {
		return RefModelOutcome{Ref: in.Ref, NotSent: true}, readErr
	}
	if _, e := a.Store.Guard(ctx, in.RunID, in.Revision); e != nil {
		return RefModelOutcome{Ref: in.Ref, NotSent: true}, e
	}
	messages, e := a.history(ctx, in.RunID, in.Ref)
	if e != nil {
		return RefModelOutcome{Ref: in.Ref, NotSent: true}, e
	}
	view, e := managedContext(messages, in.Ref, false)
	if e != nil {
		return RefModelOutcome{Ref: in.Ref, NotSent: true, Failure: e.Error()}, nil
	}
	checkpoint.RecoveryUntil = a.receiptDeadline(ctx)
	if checkpoint.RecoveryUntil.IsZero() {
		checkpoint.RecoveryUntil = a.now().Add(stepTimeoutLong).UTC().Truncate(time.Microsecond)
	}
	if e = a.Store.StartTaskModelCheckpoint(ctx, in.RunID, checkpoint); e != nil {
		if errors.Is(e, docstore.ErrExists) {
			return a.recoverModelRef(ctx, in.RunID, checkpoint)
		}
		return RefModelOutcome{Ref: in.Ref, NotSent: true}, e
	}
	outcome, e := a.Model(ctx, ModelInput{Step: in.Step, ModelID: modelID, Messages: view, Interruptible: true, Subtask: in.Subtask})
	if e != nil {
		return RefModelOutcome{
			Ref:       in.Ref,
			Cost:      outcome.Response.Cost,
			CostKnown: outcome.Response.CostKnown,
			NotSent:   outcome.Response.NotSent,
		}, e
	}
	// Only an explicit context refusal permits a smaller second request. The
	// original checkpoint already fences the whole step: a crash during either
	// request recovers the receipt or settles unknown, never sends again.
	if outcome.ContextOverflow && ctx.Err() == nil {
		reduced, fitErr := managedContext(messages, in.Ref, true)
		if fitErr == nil && contextMessagesBytes(reduced) < contextMessagesBytes(view) {
			first := outcome.Response
			retry, retryErr := a.Model(ctx, ModelInput{Step: in.Step, ModelID: modelID, Messages: reduced, Interruptible: true, Subtask: in.Subtask, ContextRetry: true})
			if retryErr == nil {
				outcome = retry
				outcome.Response.Cost += first.Cost
				outcome.Response.CostKnown = (first.CostKnown || first.NotSent) && (retry.Response.CostKnown || retry.Response.NotSent)
				outcome.Response.NotSent = first.NotSent && retry.Response.NotSent
			} else if ctx.Err() != nil || expectedBoundary(retryErr) {
				return RefModelOutcome{
					Ref:       in.Ref,
					Cost:      first.Cost + retry.Response.Cost,
					CostKnown: (first.CostKnown || first.NotSent) && (retry.Response.CostKnown || retry.Response.NotSent),
					NotSent:   first.NotSent && retry.Response.NotSent,
				}, retryErr
			} else {
				outcome.Failure = retryErr.Error()
			}
		}
	}
	response := outcome.Response
	out = RefModelOutcome{Cost: response.Cost, CostKnown: response.CostKnown, NotSent: response.NotSent, Failure: outcome.Failure}
	var entries []transcript.Message
	if response.Message != nil {
		entries = append(entries, *response.Message)
	}
	if out.Failure == "" {
		entries = finishModelRound(in, response.Calls, response.Tools, entries, &out)
	}
	data, e := json.Marshal(out)
	var messagesJSON []byte
	if e == nil && len(entries) > 0 {
		messagesJSON, e = json.Marshal(entries)
	}
	if e == nil {
		checkpoint.Outcome = data
		save, cancel := receiptContext(ctx)
		out.Ref, e = a.Store.SaveTaskModelCheckpoint(save, in.RunID, checkpoint, messagesJSON)
		cancel()
		if e != nil {
			// A timed-out commit can already be visible. Check the same receipt
			// before reporting that the output could not be saved.
			read, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			saved, readErr := a.Store.TaskModelCheckpoint(read, in.RunID, checkpoint.ID)
			cancel()
			if readErr == nil && saved.Revision == in.Revision && saved.Parent == in.Ref && saved.Subtask == in.Subtask && bytes.Equal(saved.Outcome, data) {
				out.Ref, e = saved.Ref, nil
			}
		}
	}
	if e != nil {
		observability.RecordErrorCategory(ctx, "task.model.checkpoint", e)
		// Return known usage even if the checkpoint could not be committed.
		out.Ref, out.Calls, out.Failure = in.Ref, nil, modelSaveFailure
	}
	span.SetAttributes(attribute.Int("nori.tool.call_count", len(out.Calls)))
	return out, nil
}

func finishModelRound(in RefModelInput, calls []transcript.ToolCall, tools *extensions.Snapshot, entries []transcript.Message, out *RefModelOutcome) []transcript.Message {
	if len(calls) == 0 {
		out.Invalid = true
		correction := transcript.UserText(prompts.Runtime("completion-protocol", nil))
		return append(entries, correction)
	}
	if e := agent.ValidateCalls(calls, true, !in.Subtask, tools); e != nil {
		parts := []transcript.Part{}
		for _, call := range calls {
			rejected := map[string]any{"rejected": true, "reason": e.Error(), "executed": false}
			parts = append(parts, transcript.Part{ToolResult: &transcript.ToolResult{CallID: call.ID, Name: call.Name, Output: rejected}})
		}
		out.Invalid = true
		return append(entries, transcript.Message{Role: transcript.RoleUser, Parts: parts})
	}
	for i, call := range calls {
		ref := CallRef{ID: call.ID, Name: call.Name, Index: i}
		if definition, ok := tools.Lookup(call.Name); ok {
			exec := definition.Exec()
			ref.Exec = &exec
		}
		out.Calls = append(out.Calls, ref)
	}
	return entries
}
func (a *Activities) ToolRef(ctx context.Context, in RefToolInput) (out RefToolOutcome, err error) {
	ctx, span := startActivity(ctx, "task.history.tool", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.history.tool"); span.End() }()
	messages, e := a.history(ctx, in.RunID, in.Ref)
	if e != nil {
		return out, e
	}
	call, e := lastCall(messages, in.Index)
	if e != nil {
		return out, e
	}
	var result map[string]any
	// A compose call carries the read-only limit to each nested call instead.
	if in.ReadOnly && call.Name != "compose" && agent.PolicyFor(call.Name, in.Exec).Effect != "read" {
		result = map[string]any{
			"outcome":      "needs_input",
			"error":        prompts.Runtime("reads-only", nil),
			"effect_class": "read",
			"operation_id": in.OperationID,
		}
	} else {
		result, e = a.durableTool(ctx, ToolInput{Step: in.Step, OperationID: in.OperationID, Call: call, Exec: in.Exec, ReadOnly: in.ReadOnly})
		if e != nil {
			return out, e
		}
	}
	result["evidence_ref"] = in.OperationID
	if waiting, _ := result["awaiting_interaction"].(bool); waiting && call.Name == "compose" {
		// A compose script stopped at a nested call that needs the user: the task asks, exactly as for a direct call.
		result["outcome"] = "needs_interaction"
	}
	out.Ref, e = a.appendMessages(ctx, in.RunID, in.Ref, []transcript.Message{transcript.ToolResultMessage(call.ID, call.Name, result)})
	if e != nil {
		return out, errors.New("The tool result could not be saved to task history.")
	}
	out.Outcome, _ = result["outcome"].(string)
	out.EffectClass, _ = result["effect_class"].(string)
	out.Cost, _ = result["cost_usd"].(float64)
	out.CostUnknown, _ = result["cost_unknown"].(bool)
	if out.Outcome == "needs_interaction" {
		out.InteractionID, _ = result["interaction_id"].(string)
		out.Prompt, _ = result["prompt"].(string)
	}
	if out.Outcome == "uncertain" && out.EffectClass != "read" {
		out.Unresolved, out.Clears, e = a.nameUnresolved(ctx, in.RunID, messages)
		if e != nil {
			return out, e
		}
	}
	return out, nil
}
func (a *Activities) InputRef(ctx context.Context, in RefInput) (out state.Question, err error) {
	ctx, span := startActivity(ctx, "task.history.question", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.history.question"); span.End() }()
	messages, e := a.history(ctx, in.RunID, in.Ref)
	if e != nil {
		return out, e
	}
	call, e := lastCall(messages, in.Index)
	if e != nil {
		return out, e
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
		return out, e
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
		return "", e
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
	return ref, err
}
func (a *Activities) PublishRef(ctx context.Context, in RefPublication) (run state.Run, err error) {
	ctx, span := startActivity(ctx, "task.history.publish", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.history.publish"); span.End() }()
	if in.Ref != "" {
		in.ExpectedIncomplete = in.Status == state.RunFailed
		messages, e := a.history(ctx, in.RunID, in.Ref)
		if e != nil {
			return state.Run{}, e
		}
		call, e := lastCall(messages, 0)
		if e != nil {
			return state.Run{}, e
		}
		in.Text, _ = call.Args["answer"].(string)
		proposed := proposal(call)
		in.Answer = &state.ResultDraft{Checked: in.Status == state.RunCompleted, Evidence: proposed.Evidence, Unmet: proposed.Unmet}
		if ops, e := a.Store.Operations(ctx, in.RunID); e == nil {
			in.Answer.Artifacts, in.Answer.Cited = citedOutputs(ops, proposed.Evidence)
		}
	}
	if in.Status != state.RunCompleted && in.Text != "" && in.Detail != "" {
		in.Text += "\n\n" + in.Detail
	}
	run, e := a.Publish(ctx, in.Publication)
	return state.Run{ID: run.ID, Status: run.Status, Revision: run.Revision}, e
}
