package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go.temporal.io/sdk/activity"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/genai"
	"nori/internal/agent"
	"nori/internal/state"
)

// Only immutable references and bounded control metadata cross Temporal history.
// The history chain itself retains every exact Responses item, including opaque
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
	Grants             int
	QuestionResolved   bool
	Request            string
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
	Ref string
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
	Failure   string
	Invalid   bool
}
type RefToolInput struct {
	Step
	Ref, OperationID string
	Index            int
	ReadOnly         bool
}
type RefToolOutcome struct{ Ref, Outcome, EffectClass string }
type RefInput struct {
	Step
	Ref   string
	Index int
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
	store, ok := a.Store.(state.TaskEvidenceStore)
	if !ok {
		return nil, errors.New("task evidence storage unavailable")
	}
	return store, nil
}
func (a *Activities) appendContents(ctx context.Context, id, parent string, contents []*genai.Content) (string, error) {
	store, e := a.evidence()
	if e != nil {
		return "", e
	}
	data, e := json.Marshal(contents)
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
func (a *Activities) history(ctx context.Context, id, ref string) ([]*genai.Content, error) {
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
	var contents []*genai.Content
	for _, block := range blocks {
		var chunk []*genai.Content
		if e = json.Unmarshal(block, &chunk); e != nil {
			return nil, errors.New("task transcript block is invalid")
		}
		contents = append(contents, chunk...)
	}
	return contents, nil
}
func lastCall(contents []*genai.Content, index int) (*genai.FunctionCall, error) {
	for i := len(contents) - 1; i >= 0; i-- {
		if contents[i] == nil || contents[i].Role != "model" {
			continue
		}
		calls := []*genai.FunctionCall{}
		for _, part := range contents[i].Parts {
			if part != nil && part.FunctionCall != nil {
				calls = append(calls, part.FunctionCall)
			}
		}
		if index >= 0 && index < len(calls) {
			return calls[index], nil
		}
		break
	}
	return nil, errors.New("saved model call reference does not resolve")
}
func (a *Activities) SyncHistory(ctx context.Context, in HistoryInput) (out HistorySync, err error) {
	ctx, span := startActivity(ctx, "task.history.sync", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.history.sync"); span.End() }()
	snap, e := a.Reconcile(ctx, in.RunID)
	if e != nil {
		return out, e
	}
	out = HistorySync{Ref: in.Ref, Revision: snap.Run.Revision, Position: in.Position, Request: clip(snap.Run.Request, 1500)}
	if in.Ref == "" {
		contents, e := a.Context(ctx, in.RunID)
		if e != nil {
			return out, e
		}
		if len(contents) > 0 {
			out.Ref, e = a.appendContents(ctx, in.RunID, "", contents)
			if e != nil {
				return out, classified(e)
			}
		}
	}
	contents := []*genai.Content{}
	q := in.Question
	for _, cmd := range snap.Commands {
		if cmd.Position <= in.Position {
			continue
		}
		switch cmd.Kind {
		case "start", "steer", "input":
			if q != nil && q.CallID != "" {
				contents = append(contents, toolResponse(q.CallID, "request_input", map[string]any{"superseded": true, "reason": "Austin changed the task instructions."}))
			}
			q = nil
			out.QuestionResolved = true
			contents = append(contents, &genai.Content{Role: "user", Parts: []*genai.Part{{Text: cmd.Text}}})
			if cmd.Kind == "start" {
				out.Request = clip(cmd.Text, 1500)
			}
		case "answer":
			if q != nil && q.ID == cmd.QuestionID {
				if q.CallID != "" {
					contents = append(contents, toolResponse(q.CallID, "request_input", map[string]any{"answer": cmd.Text, "question_id": cmd.QuestionID}))
				} else {
					contents = append(contents, &genai.Content{Role: "user", Parts: []*genai.Part{{Text: cmd.Text}}})
				}
				q = nil
				out.QuestionResolved = true
			}
		case "continue":
			if q != nil && q.ID == cmd.QuestionID && q.Kind == "budget" {
				out.Grants++
				q = nil
				out.QuestionResolved = true
				contents = append(contents, &genai.Content{Role: "user", Parts: []*genai.Part{{Text: fmt.Sprintf("Austin approved another $%.2f of model spend. Continue the task.", agent.BudgetUSD)}}})
			}
		}
		out.Position = cmd.Position
	}
	if len(contents) > 0 {
		out.Ref, e = a.appendContents(ctx, in.RunID, out.Ref, contents)
		if e != nil {
			return out, classified(e)
		}
	}
	return out, nil
}
func (a *Activities) ModelRef(ctx context.Context, in RefModelInput) (out RefModelOutcome, err error) {
	ctx, span := startActivity(ctx, "task.history.model", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.history.model"); span.End() }()
	contents, e := a.history(ctx, in.RunID, in.Ref)
	if e != nil {
		return out, classified(e)
	}
	outcome, e := a.Model(agent.CompletionContext(ctx), ModelInput{Step: in.Step, Contents: contents, Interruptible: true})
	if e != nil {
		return out, e
	}
	decision := outcome.Decision
	out = RefModelOutcome{Ref: in.Ref, Cost: decision.Cost, CostKnown: decision.CostKnown, Failure: outcome.Failure}
	// Accounting is returned even if persisting the response fails. Activity errors
	// would discard this value, so persistence failure is an ordinary failure field.
	if decision.Content != nil {
		out.Ref, e = a.appendContents(ctx, in.RunID, in.Ref, []*genai.Content{decision.Content})
		if e != nil {
			out.Ref = in.Ref
			out.Failure = "model output could not be retained; paid request will not be replayed"
			return out, nil
		}
	}
	if out.Failure != "" {
		return out, nil
	}
	if len(decision.Calls) == 0 {
		out.Invalid = true
		out.Ref, e = a.appendContents(ctx, in.RunID, out.Ref, []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "Plain text does not finish this task. Call complete(answer,evidence,unmet), or request_input for a missing prerequisite."}}}})
		if e != nil {
			out.Failure = "completion protocol correction could not be retained"
		}
		return out, nil
	}
	if e = agent.ValidateCalls(decision.Calls, true); e != nil {
		parts := []*genai.Part{}
		for _, call := range decision.Calls {
			parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: map[string]any{"rejected": true, "reason": e.Error(), "executed": false}}})
		}
		out.Invalid = true
		out.Ref, e = a.appendContents(ctx, in.RunID, out.Ref, []*genai.Content{{Role: "user", Parts: parts}})
		if e != nil {
			out.Failure = "rejected batch results could not be retained"
		}
		return out, nil
	}
	for i, call := range decision.Calls {
		out.Calls = append(out.Calls, CallRef{ID: call.ID, Name: call.Name, Index: i})
	}
	span.SetAttributes(attribute.Int("nori.tool.call_count", len(out.Calls)))
	return out, nil
}
func (a *Activities) ToolRef(ctx context.Context, in RefToolInput) (out RefToolOutcome, err error) {
	ctx, span := startActivity(ctx, "task.history.tool", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.history.tool"); span.End() }()
	contents, e := a.history(ctx, in.RunID, in.Ref)
	if e != nil {
		return out, classified(e)
	}
	call, e := lastCall(contents, in.Index)
	if e != nil {
		return out, classified(e)
	}
	var result map[string]any
	if in.ReadOnly && call.Name != "browser_navigate" && call.Name != "browser_screenshot" {
		result = map[string]any{"outcome": "needs_input", "error": "Completion correction permits reads only. Ask Austin before further writes or shell execution.", "effect_class": "read", "operation_id": in.OperationID}
	} else {
		result, e = a.Tool(ctx, ToolInput{Step: in.Step, OperationID: in.OperationID, Call: call, Durable: true})
		if e != nil {
			return out, e
		}
	}
	// The model receives the immutable result reference as well as the tool data.
	result["evidence_ref"] = in.OperationID
	out.Ref, e = a.appendContents(ctx, in.RunID, in.Ref, []*genai.Content{toolResponse(call.ID, call.Name, result)})
	if e != nil {
		return out, classified(errors.New("tool result transcript could not be retained; operation will not be reissued"))
	}
	out.Outcome, _ = result["outcome"].(string)
	out.EffectClass, _ = result["effect_class"].(string)
	return out, nil
}
func (a *Activities) InputRef(ctx context.Context, in RefInput) (out state.Question, err error) {
	ctx, span := startActivity(ctx, "task.history.question", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.history.question"); span.End() }()
	contents, e := a.history(ctx, in.RunID, in.Ref)
	if e != nil {
		return out, classified(e)
	}
	call, e := lastCall(contents, in.Index)
	if e != nil {
		return out, classified(e)
	}
	text, _ := call.Args["question"].(string)
	return state.Question{Kind: "input", Text: text, CallID: call.ID, Revision: in.Revision}, nil
}
func (a *Activities) AppendToolResult(ctx context.Context, in AppendResult) (ref string, err error) {
	ctx, span := startActivity(ctx, "task.history.append_result", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.history.append_result"); span.End() }()
	// A rejected completion can already have its result saved before a
	// publication loses a revision race. Keep exactly one output for each call.
	contents, e := a.history(ctx, in.RunID, in.Ref)
	if e != nil {
		return "", classified(e)
	}
	for _, content := range contents {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part != nil && part.FunctionResponse != nil && part.FunctionResponse.ID == in.CallID {
				return in.Ref, nil
			}
		}
	}
	ref, err = a.appendContents(ctx, in.RunID, in.Ref, []*genai.Content{toolResponse(in.CallID, in.Name, in.Response)})
	return ref, classified(err)
}
func (a *Activities) PublishRef(ctx context.Context, in RefPublication) (run state.Run, err error) {
	ctx, span := startActivity(ctx, "task.history.publish", "CHAIN", a.activityRun(ctx, in.RunID), in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.history.publish"); span.End() }()
	if in.Ref != "" {
		in.ExpectedIncomplete = in.Status == "failed"
		contents, e := a.history(ctx, in.RunID, in.Ref)
		if e != nil {
			return state.Run{}, classified(e)
		}
		call, e := lastCall(contents, 0)
		if e != nil {
			return state.Run{}, classified(e)
		}
		in.Text, _ = call.Args["answer"].(string)
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
