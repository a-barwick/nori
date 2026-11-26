package task

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
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

type HistoryInput struct {
	Step
	Ref      string
	Position int64
	Question *state.Question
}
type HistorySync struct {
	Ref                   string
	Revision, Position    int64
	QuestionResolved      bool
	Request               string
	Spent, Others, Budget float64
	CostUnknown           bool
	Subtask, ReadOnly     bool
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
	Exec     *extensions.Exec `json:",omitempty"`
}
type RefModelOutcome struct {
	Ref             string
	Calls           []CallRef
	Cost            float64
	CostKnown       bool
	NotSent         bool
	Failure         string
	Invalid         bool
	HomeUnavailable bool `json:",omitempty"`
	Interrupted     bool `json:",omitempty"`
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
	Cost                      float64
	CostUnknown               bool
	Unresolved                string
	Clears                    []string
	InteractionID             string `json:",omitempty"`
	Prompt                    string `json:",omitempty"`
	ToolSig                   string `json:",omitempty"`
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

func toolCallSignature(name string, args map[string]any, outcome string) string {
	rawArgs, _ := json.Marshal(args)
	h := sha256.New()
	_, _ = h.Write([]byte(name))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(rawArgs)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(outcome))
	return outcome + ":" + hex.EncodeToString(h.Sum(nil)[:8])
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
	if len(response.WebSearches) > 0 {
		a.recordNativeWebSearches(ctx, in.RunID, in.Revision, modelID, response.WebSearches, response.Citations)
	}
	out = RefModelOutcome{
		Cost: response.Cost, CostKnown: response.CostKnown, NotSent: response.NotSent,
		Failure: outcome.Failure, HomeUnavailable: outcome.HomeUnavailable,
	}
	var entries []transcript.Message
	if response.Message != nil {
		entries = append(entries, *response.Message)
	}
	if out.Failure == "" && !out.HomeUnavailable {
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

type RefToolBatchItem struct {
	OperationID string
	Index       int
	Exec        *extensions.Exec `json:",omitempty"`
}

type RefToolBatchInput struct {
	Step
	Ref      string
	ReadOnly bool
	Items    []RefToolBatchItem
}

type RefToolBatchOutcome struct {
	Ref         string
	Items       []RefToolOutcome
	TotalCost   float64
	CostUnknown bool
	AnyRevised  bool
	FirstErr    error
	FirstErrIdx int
}

func lastCallFromBlocks(blocks [][]byte, index int) (transcript.ToolCall, error) {
	for b := len(blocks) - 1; b >= 0; b-- {
		var chunk []transcript.Message
		decoder := json.NewDecoder(bytes.NewReader(blocks[b]))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&chunk); err != nil {
			return transcript.ToolCall{}, errTranscriptBlockInvalid
		}
		for i := len(chunk) - 1; i >= 0; i-- {
			if chunk[i].Role != transcript.RoleModel {
				continue
			}
			calls := chunk[i].ToolCalls()
			if index >= 0 && index < len(calls) {
				return calls[index], nil
			}
			return transcript.ToolCall{}, errCallRefUnresolved
		}
	}
	return transcript.ToolCall{}, errCallRefUnresolved
}

func (a *Activities) ToolBatchRef(ctx context.Context, in RefToolBatchInput) (out RefToolBatchOutcome, err error) {
	ctx, span := startActivity(ctx, "task.history.tool_batch", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.history.tool_batch"); span.End() }()
	blocks, e := a.Store.ReadHistory(ctx, in.RunID, in.Ref)
	if e != nil {
		return out, e
	}
	calls := make([]transcript.ToolCall, len(in.Items))
	for i, item := range in.Items {
		call, cErr := lastCallFromBlocks(blocks, item.Index)
		if cErr != nil {
			return out, cErr
		}
		calls[i] = call
	}

	results := make([]map[string]any, len(in.Items))
	errs := make([]error, len(in.Items))
	var wg sync.WaitGroup
	for i, item := range in.Items {
		wg.Add(1)
		go func(idx int, it RefToolBatchItem, call transcript.ToolCall) {
			defer wg.Done()
			res, tErr := a.durableTool(ctx, ToolInput{
				Step:        in.Step,
				OperationID: it.OperationID,
				Call:        call,
				Exec:        it.Exec,
				ReadOnly:    in.ReadOnly,
			})
			results[idx] = res
			errs[idx] = tErr
		}(i, item, calls[i])
	}
	wg.Wait()

	for i, tErr := range errs {
		if tErr != nil {
			out.FirstErr = tErr
			out.FirstErrIdx = i
			return out, tErr
		}
	}

	msgs := make([]transcript.Message, len(in.Items))
	out.Items = make([]RefToolOutcome, len(in.Items))
	for i, item := range in.Items {
		result := results[i]
		if result == nil {
			result = map[string]any{}
		}
		result["evidence_ref"] = item.OperationID
		msgs[i] = transcript.ToolResultMessage(calls[i].ID, calls[i].Name, result)
		var itemOut RefToolOutcome
		itemOut.Outcome, _ = result["outcome"].(string)
		itemOut.EffectClass, _ = result["effect_class"].(string)
		itemOut.Cost, _ = result["cost_usd"].(float64)
		itemOut.CostUnknown, _ = result["cost_unknown"].(bool)
		itemOut.ToolSig = toolCallSignature(calls[i].Name, calls[i].Args, itemOut.Outcome)
		if itemOut.Outcome == "needs_interaction" {
			itemOut.InteractionID, _ = result["interaction_id"].(string)
			itemOut.Prompt, _ = result["prompt"].(string)
		}
		out.TotalCost += itemOut.Cost
		out.CostUnknown = out.CostUnknown || itemOut.CostUnknown
		out.Items[i] = itemOut
	}
	out.Ref, e = a.appendMessages(ctx, in.RunID, in.Ref, msgs)
	if e != nil {
		return out, errors.New("The tool results could not be saved to task history.")
	}
	return out, nil
}

func (a *Activities) ToolRef(ctx context.Context, in RefToolInput) (out RefToolOutcome, err error) {
	ctx, span := startActivity(ctx, "task.history.tool", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.history.tool"); span.End() }()
	blocks, e := a.Store.ReadHistory(ctx, in.RunID, in.Ref)
	if e != nil {
		return out, e
	}
	call, e := lastCallFromBlocks(blocks, in.Index)
	if e != nil {
		return out, e
	}
	var result map[string]any
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
	out.ToolSig = toolCallSignature(call.Name, call.Args, out.Outcome)
	if out.Outcome == "needs_interaction" {
		out.InteractionID, _ = result["interaction_id"].(string)
		out.Prompt, _ = result["prompt"].(string)
	}
	if out.Outcome == "uncertain" && out.EffectClass != "read" {
		messages, hErr := a.history(ctx, in.RunID, in.Ref)
		if hErr != nil {
			return out, hErr
		}
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
		named = clip(named, 4096)
		text = named + "\n\n" + clip(text, state.MaxMessageBytes-len(named)-8)
	}
	inputKind, _ := call.Args["input"].(string)
	inputKind = strings.TrimSpace(inputKind)
	var options []state.QuestionOption
	if rawOpts, ok := call.Args["options"].([]any); ok {
		for _, item := range rawOpts {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id, _ := m["id"].(string)
			label, _ := m["label"].(string)
			id = strings.TrimSpace(id)
			label = strings.TrimSpace(label)
			if id != "" && label != "" {
				options = append(options, state.QuestionOption{ID: id, Label: label})
			}
		}
	}
	return state.Question{Kind: "input", Text: text, CallID: call.ID, Revision: in.Revision, Clears: clears, Input: inputKind, Options: options}, nil
}
func (a *Activities) AppendToolResult(ctx context.Context, in AppendResult) (ref string, err error) {
	ctx, span := startActivity(ctx, "task.history.append_result", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.history.append_result"); span.End() }()
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
			if in.Status == state.RunCompleted {
				in.Parts = append(in.Parts, a.reminderParts(ctx, in.RunID, ops)...)
			}
		}
	}
	if in.Status != state.RunCompleted && in.Text != "" && in.Detail != "" {
		in.Text += "\n\n" + in.Detail
	}
	run, e := a.Publish(ctx, in.Publication)
	return state.Run{ID: run.ID, Status: run.Status, Revision: run.Revision}, e
}

func (a *Activities) reminderParts(ctx context.Context, runID string, ops []state.OperationRecord) []state.Part {
	var parts []state.Part
	seen := map[string]bool{}
	for _, op := range ops {
		if op.Outcome != "confirmed" || op.ResultRef == "" {
			continue
		}
		if op.Name != "automation_create" && (op.Tool == nil || op.Tool.ID != "nori/assistant/automation-create") {
			continue
		}
		blocks, err := a.Store.ReadHistory(ctx, runID, op.ResultRef)
		if err != nil || len(blocks) != 1 {
			continue
		}
		var out struct {
			ReminderCard *struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				Text        string `json:"text"`
				ScheduledAt string `json:"scheduled_at"`
				Fallback    string `json:"fallback"`
			} `json:"reminder_card"`
		}
		if json.Unmarshal(blocks[0], &out) != nil || out.ReminderCard == nil || out.ReminderCard.ID == "" {
			continue
		}
		card := out.ReminderCard
		if seen[card.ID] {
			continue
		}
		seen[card.ID] = true
		fallback := strings.TrimSpace(card.Fallback)
		if fallback == "" {
			fallback = "Reminder set · " + card.Name
		}
		parts = append(parts, state.Part{
			Type:        state.PartReminder,
			ID:          card.ID,
			Name:        card.Name,
			Text:        card.Text,
			ScheduledAt: card.ScheduledAt,
			Fallback:    fallback,
		})
	}
	return parts
}
