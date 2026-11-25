package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"nori/internal/agent"
	"nori/internal/observability"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

var (
	errOperationIdentityConflict = errors.New("operation recovery identity conflict")
	errOperationResultInvalid    = errors.New("invalid saved operation result")
)

type persistFunc func(outcome string, output map[string]any, cause error) (map[string]any, error)

// durableTool records authority and outcomes independently of best-effort debug
// traces. Retrying a step is never a substitute for an operation receipt.
func (a *Activities) durableTool(parent context.Context, in ToolInput) (result map[string]any, err error) {
	evidence := a.Store
	policy := agent.PolicyFor(in.Call.Name, in.Exec)
	effect := policy.Effect
	isExternal := in.Exec != nil && !in.Exec.Builtin
	rerunnable := effect == "read" && ((isExternal && in.Exec.Policy.Retry == "safe") ||
		(!isExternal && policy.Runtime && !policy.InterruptedCostUnknown && policy.InterruptedCostUSD == 0 && (in.Exec == nil || in.Exec.Policy.Retry == "safe"))) ||
		in.Call.Name == "compose" && policy.Runtime
	var (
		recovering *state.OperationRecord
		saved      map[string]any
		ctx        context.Context
		done       func()
		run        state.Run
		span       trace.Span
		plan       *externalPlan
		until      time.Time
	)
	for admitAttempt := 0; admitAttempt < 2; admitAttempt++ {
		effect = policy.Effect
		recovering, saved, plan = nil, nil, nil
		err = a.waitForReceipt(parent, in.RunID, func() (time.Time, error) {
			var err error
			recovering, saved, err = a.savedOperation(parent, in)
			if recovering != nil && !rerunnable {
				return recovering.RecoveryUntil, err
			}
			return time.Time{}, err
		})
		if err != nil || saved != nil {
			return saved, err
		}
		// A correction waits for paid model reads because interruption can lose the reported cost. Stop still cancels them.
		ctx, done, run, err = a.bounded(parent, in.Step, effect == "read" && !policy.InterruptedCostUnknown)
		if err != nil {
			return nil, err
		}
		ctx, span = startActivity(ctx, "task.tool", "TOOL", run, in.Revision)
		span.SetAttributes(
			attribute.String("tool.name", policy.Name), attribute.String("nori.operation.id", in.OperationID),
			attribute.String("nori.operation.effect_class", effect),
		)
		if in.ParentOperationID != "" {
			span.SetAttributes(attribute.String("nori.operation.parent_id", in.ParentOperationID))
		}
		until = a.receiptDeadline(parent)
		if in.Exec != nil && !in.Exec.Builtin {
			var stop map[string]any
			plan, stop, err = a.planExternal(ctx, in, recovering)
			if recovering != nil && (err != nil || stop != nil) {
				// The account or implementation is no longer usable; recovery can only settle what is known.
				plan, err = &externalPlan{}, nil
			} else if err != nil || stop != nil {
				span.End()
				done()
				return stop, err
			} else {
				effect = string(plan.prepared.Effect)
				span.SetAttributes(attribute.String("nori.tool.definition", in.Exec.Tool.ID), attribute.String("nori.tool.digest", in.Exec.Tool.Digest),
					attribute.String("nori.tool.version", in.Exec.Tool.Version), attribute.String("nori.binding.adapter", string(in.Exec.Binding.Adapter)))
				if plan.call.Grant != nil {
					span.SetAttributes(attribute.String("nori.connection.id", plan.call.Grant.Connection.ID))
				}
			}
		}
		if recovering == nil {
			if blocked, err := a.unresolvedWriteBlock(ctx, in.RunID, effect); err != nil || blocked != nil {
				span.End()
				done()
				return blocked, err
			}
			if err = a.Store.Admit(ctx, in.RunID, in.Revision, in.OperationID, until); err != nil {
				span.End()
				done()
				if errors.Is(err, docstore.ErrExists) && admitAttempt == 0 {
					// A timed-out attempt may have committed admission just before this one.
					continue
				}
				return nil, err
			}
		}
		break
	}
	defer done()
	defer span.End()
	createdAt := a.now().UTC().Truncate(time.Microsecond)
	op := state.OperationRecord{
		ID: in.OperationID, CallID: in.Call.ID, Name: in.Call.Name, Revision: in.Revision,
		EffectClass: effect, Outcome: "pending", RecoveryUntil: until, ParentOperationID: in.ParentOperationID,
		CreatedAt: &createdAt,
	}
	if in.Exec != nil {
		op.Tool = &state.DefinitionRef{ID: in.Exec.Tool.ID, Version: in.Exec.Tool.Version, Digest: in.Exec.Tool.Digest}
	}
	if plan != nil {
		// The whole prepared action is saved before the first attempt, so recovery can settle from it.
		op.Action = &state.Action{Digest: plan.digest, Text: clip(plan.prepared.Summary, 2000), Spec: plan.prepared.Spec,
			Intent: true, Precondition: plan.prepared.Precondition, Keys: plan.prepared.Keys}
		op.AuthorityRef = plan.authority.AuthorityRef
		if plan.call.Grant != nil {
			op.ConnectionID = plan.call.Grant.Connection.ID
		}
	} else if in.Call.Name == "web_search" {
		query, _ := in.Call.Args["query"].(string)
		summary := agent.FormatSearchQuerySummary(query)
		op.Action = &state.Action{Digest: searchActionDigest(summary), Text: clip(summary, 2000)}
	}
	if recovering != nil {
		op = *recovering
		// Old admission-only records have no descriptive evidence yet.
		if op.CallID == "" {
			op.CallID = in.Call.ID
			op.EffectClass = effect
			op.Outcome = "pending"
			if op.CreatedAt == nil {
				op.CreatedAt = &createdAt
			}
			if op.Action == nil && plan == nil && in.Call.Name == "web_search" {
				query, _ := in.Call.Args["query"].(string)
				summary := agent.FormatSearchQuerySummary(query)
				op.Action = &state.Action{Digest: searchActionDigest(summary), Text: clip(summary, 2000)}
			}
		}
		if op.Name == "" {
			op.Name = in.Call.Name
		}
	} else if err = evidence.PutOperation(ctx, in.RunID, op); err != nil {
		return nil, err
	}
	traceStep := state.TaskStep{
		ID: in.OperationID, Kind: "tool", Name: in.Call.Name, Status: "running", Revision: in.Revision,
		StartedAt: a.now().UTC(), Arguments: traceJSON(in.Call.Args), CostStatus: "not_applicable",
	}
	a.recordStep(in.RunID, traceStep)
	defer func() {
		traceStep.Result = traceJSON(result)
		a.finishStep(&traceStep, err)
		a.recordStep(in.RunID, traceStep)
	}()
	persist := func(outcome string, output map[string]any, cause error) (map[string]any, error) {
		if output == nil {
			output = map[string]any{}
		}
		output["operation_id"], output["outcome"], output["effect_class"] = in.OperationID, outcome, effect
		if cause != nil {
			output["error"] = traceText(cause.Error())
			recordActivityFailure(context.WithoutCancel(ctx), cause, "task.tool")
		}
		data, e := json.Marshal(output)
		if e != nil {
			return nil, e
		}
		save, cancel := receiptContext(ctx)
		defer cancel()
		op.ResultRef, e = evidence.AppendHistory(save, in.RunID, "", data)
		if e != nil {
			observability.RecordErrorCategory(save, "task.evidence", e)
			return nil, e
		}
		op.Outcome = outcome
		if outcome == "failed" && op.Cleared == "" {
			op.Cleared = failureCleared(in.Call, output)
		}
		if id, yes := output["artifact_id"].(string); yes && id != "" {
			op.ArtifactIDs = []string{id}
		}
		e = evidence.SettleOperation(save, in.RunID, op)
		if errors.Is(e, docstore.ErrExists) {
			// Another attempt won the settlement. Use that same result in the transcript.
			_, settled, readErr := a.savedOperation(save, in)
			if readErr != nil || settled != nil {
				return settled, readErr
			}
		}
		if e != nil {
			observability.RecordErrorCategory(save, "task.evidence", e)
			return nil, e
		}
		span.SetAttributes(attribute.String("nori.tool.outcome", outcome), attribute.Int("nori.operation.attempt", op.Attempt))
		observability.Event(save, "task.operation.recorded", attribute.String("nori.operation.id", op.ID), attribute.String("nori.tool.outcome", outcome))
		return output, nil
	}

	if plan != nil {
		return a.runExternal(ctx, in, &op, recovering != nil, plan, persist)
	}
	if policy.Runtime && in.Call.Name == "compose" {
		// Running a compose call again is safe: nested calls reuse their saved results.
		return a.composeTool(ctx, in, persist)
	}
	if policy.Runtime {
		return a.runtimeTool(ctx, in, &op, recovering != nil, rerunnable, policy, persist)
	}
	op.Cleared = state.ClearedDidNotRun
	return persist("failed", nil, errors.New("this tool is not available"))
}

func (a *Activities) savedOperation(ctx context.Context, in ToolInput) (*state.OperationRecord, map[string]any, error) {
	saved, err := a.Store.Operation(ctx, in.RunID, in.OperationID)
	if errors.Is(err, state.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if saved.Revision != in.Revision || (saved.CallID != "" && saved.CallID != in.Call.ID) || (saved.Name != "" && saved.Name != in.Call.Name) {
		return nil, nil, errOperationIdentityConflict
	}
	if saved.ResultRef == "" {
		return &saved, nil, nil
	}
	blocks, err := a.Store.ReadHistory(ctx, in.RunID, saved.ResultRef)
	if err != nil {
		return nil, nil, err
	}
	var result map[string]any
	if len(blocks) != 1 || json.Unmarshal(blocks[0], &result) != nil || result == nil {
		return nil, nil, errOperationResultInvalid
	}
	observability.Event(ctx, "task.operation.checkpoint_reused", attribute.String("nori.operation.id", saved.ID))
	return nil, result, nil
}

func (a *Activities) unresolvedWriteBlock(ctx context.Context, runID, effect string) (map[string]any, error) {
	if effect == "read" || effect == "control" {
		return nil, nil
	}
	records, err := a.Store.Operations(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, saved := range records {
		if unresolvedWrite(saved) {
			return map[string]any{
				"operation_id": saved.ID, "outcome": "uncertain", "effect_class": saved.EffectClass,
				"error": prompts.Runtime("write-uncertain", map[string]any{"Tool": saved.Name}),
			}, nil
		}
	}
	return nil, nil
}

// toolOutcome maps what a tool returned to its operation record.
func toolOutcome(output map[string]any) string {
	if available, ok := output["available"].(bool); ok && !available {
		return "failed"
	}
	return "confirmed"
}

func (a *Activities) runtimeTool(ctx context.Context, in ToolInput, op *state.OperationRecord, recovering, rerunnable bool, policy agent.ToolPolicy, persist persistFunc) (map[string]any, error) {
	if recovering && !rerunnable {
		outcome := "failed"
		if policy.Effect != "read" && policy.Effect != "control" {
			outcome = "uncertain"
		}
		return persist(outcome, map[string]any{"cost_unknown": policy.InterruptedCostUnknown, "cost_usd": policy.InterruptedCostUSD}, errors.New("the worker stopped before the operation result was saved"))
	}
	run, guardErr := a.Store.Guard(ctx, in.RunID, in.Revision)
	if guardErr != nil {
		if _, saveErr := persist("skipped", nil, guardErr); saveErr != nil {
			return nil, saveErr
		}
		return nil, guardErr
	}
	op.Attempt++
	if err := a.Store.PutOperation(ctx, in.RunID, *op); err != nil {
		if recovering {
			if _, settled, readErr := a.savedOperation(ctx, in); readErr == nil && settled != nil {
				return settled, nil
			}
		}
		return nil, err
	}
	var output map[string]any
	var err error
	if in.Call.Name == "operation_read" {
		// The saved outputs belong to the task's evidence, which only the activities hold.
		id, _ := in.Call.Args["operation_id"].(string)
		offset := parseNonNegativeIntArg(in.Call.Args["offset"])
		limit := parseNonNegativeIntArg(in.Call.Args["limit"])
		output, err = a.operationReadPage(ctx, in.RunID, id, offset, limit)
	} else if in.Call.Name == "task_history_read" {
		ref, _ := in.Call.Args["history_ref"].(string)
		before, _ := in.Call.Args["before"].(string)
		offset, _ := in.Call.Args["offset"].(string)
		output, err = a.taskHistoryRead(ctx, in.RunID, ref, before, offset)
	} else {
		output, err = a.agentForRun(run).CallRuntimeTool(observability.SuppressErrorCapture(ctx), in.RunID, in.Call.Name, in.Call.Args)
	}
	if rerunnable && errors.Is(ctx.Err(), context.Canceled) {
		return nil, context.Cause(ctx)
	}
	if err != nil {
		return persist("failed", output, err)
	}
	return persist(toolOutcome(output), output, nil)
}

func searchActionDigest(summary string) string {
	sum := sha256.Sum256([]byte("web_search:" + summary))
	return hex.EncodeToString(sum[:])
}

func nativeWebSearchOutcome(status string) string {
	switch status {
	case "failed":
		return "failed"
	case "in_progress", "searching":
		return "attempted"
	default:
		return "confirmed"
	}
}

// recordNativeWebSearches records provider-executed native web_search_call items
// (such as OpenAI Responses API built-in web_search) as read-only operations on the
// run so Work Actions and operation_read reflect every search query cleanly.
func (a *Activities) recordNativeWebSearches(
	ctx context.Context,
	runID string,
	revision int64,
	modelID string,
	calls []agent.WebSearchCall,
	citations []agent.URLCitation,
) {
	if len(calls) == 0 {
		return
	}
	save, cancel := receiptContext(ctx)
	defer cancel()
	now := a.now().UTC().Truncate(time.Microsecond)
	for i, ws := range calls {
		opID := fmt.Sprintf("%s-ws-%s-%d", runID, modelID, i+1)
		callID := ws.ID
		if !state.ValidEvidenceID(callID) {
			callID = opID
		}
		summary := agent.FormatWebSearchSummary(ws)
		outcome := nativeWebSearchOutcome(ws.Status)
		payload := map[string]any{
			"operation_id": opID,
			"outcome":      outcome,
			"effect_class": "read",
			"provider":     a.Agent.Model.Provider(),
			"native":       true,
			"summary":      summary,
		}
		if ws.Action != "" {
			payload["action"] = ws.Action
		}
		if ws.Query != "" {
			payload["query"] = ws.Query
		}
		if len(ws.Queries) > 0 {
			payload["queries"] = ws.Queries
		}
		if ws.URL != "" {
			payload["url"] = ws.URL
		}
		if ws.Pattern != "" {
			payload["pattern"] = ws.Pattern
		}
		if len(ws.Sources) > 0 {
			payload["sources"] = ws.Sources
		}
		if len(citations) > 0 {
			payload["citations"] = citations
		}
		var resultRef string
		if data, err := json.Marshal(payload); err == nil {
			if ref, err := a.Store.AppendHistory(save, runID, "", data); err == nil {
				resultRef = ref
			} else {
				slog.WarnContext(save, "native web_search history append failed", "operation_id", opID, "err", err)
			}
		}
		op := state.OperationRecord{
			ID:          opID,
			CallID:      callID,
			Name:        "web_search",
			EffectClass: "read",
			Outcome:     outcome,
			ResultRef:   resultRef,
			Revision:    revision,
			Attempt:     1,
			Action:      &state.Action{Digest: searchActionDigest(summary), Text: clip(summary, 2000)},
			CreatedAt:   &now,
		}
		if err := a.Store.PutOperation(save, runID, op); err != nil {
			slog.WarnContext(save, "native web_search operation record failed", "operation_id", opID, "err", err)
			continue
		}
		observability.Event(save, "task.operation.recorded", attribute.String("nori.operation.id", op.ID), attribute.String("nori.tool.outcome", outcome))
	}
}
