package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"nori/internal/agent"
	"nori/internal/computer"
	"nori/internal/observability"
	"nori/internal/state"
)

func toolEffect(name string) string {
	switch name {
	case "browser_navigate", "browser_screenshot", "web_search", "calendar_read", "screenshot_inspect", "upload_open", "upload_inspect", "result_save", "result_link", "results_find", "result_read", "result_open", "job_status", "job_wait":
		return "read"
	case "job_stop":
		// Stopping repeats safely, so an unresolved write never blocks it.
		return "control"
	}
	if effect := agent.WriteEffect(name); effect != "" {
		return effect
	}
	return "unknown"
}

// durableTool records authority and outcomes independently of best-effort debug
// traces. Retrying an activity is never a substitute for an operation receipt.
func (a *Activities) durableTool(parent context.Context, in ToolInput) (result map[string]any, err error) {
	evidence := a.Store
	effect := toolEffect(in.Call.Name)
	// A screenshot read is a paid model call whose interrupted cost can't be
	// established, so a correction waits for it. Stop still cancels it.
	ctx, done, run, err := a.bounded(parent, in.Step, effect == "read" && !agent.PaidRead(in.Call.Name))
	if err != nil {
		return nil, err
	}
	defer done()
	ctx, span := startActivity(ctx, "task.tool", "TOOL", run, in.Revision)
	defer span.End()
	span.SetAttributes(
		attribute.String("tool.name", safeToolName(in.Call.Name)), attribute.String("nori.operation.id", in.OperationID),
		attribute.String("nori.operation.effect_class", effect),
	)
	records, err := evidence.Operations(ctx, in.RunID)
	if err != nil {
		return nil, classified(err)
	}

	var recovering *state.OperationRecord
	for _, saved := range records {
		if saved.ID != in.OperationID {
			continue
		}
		if saved.Revision != in.Revision || (saved.CallID != "" && saved.CallID != in.Call.ID) || (saved.Name != "" && saved.Name != in.Call.Name) {
			return nil, classified(errors.New("operation recovery identity conflict"))
		}
		if saved.ResultRef != "" {
			blocks, e := evidence.ReadHistory(ctx, in.RunID, saved.ResultRef)
			if e != nil {
				return nil, classified(e)
			}
			if len(blocks) != 1 {
				return nil, classified(errors.New("invalid operation result reference"))
			}
			if e = json.Unmarshal(blocks[0], &result); e != nil {
				return nil, classified(e)
			}
			return result, nil
		}
		copy := saved
		recovering = &copy
		break
	}
	if recovering == nil {
		for _, saved := range records {
			if effect != "read" && effect != "control" && unresolvedWrite(saved) {
				return map[string]any{
					"operation_id": saved.ID, "outcome": "uncertain", "effect_class": saved.EffectClass,
					"error": fmt.Sprintf("An earlier %s call failed or has no known result and may have changed something, so no further "+
						"tool was admitted. Ask Austin with request_input. The runtime names the command in your question, and his answer lets later commands run.", saved.Name),
				}, nil
			}
		}
		if err = a.Store.Admit(ctx, in.RunID, in.Revision, in.OperationID); err != nil {
			return nil, classified(err)
		}
	}
	op := state.OperationRecord{ID: in.OperationID, CallID: in.Call.ID, Name: in.Call.Name, Revision: in.Revision, EffectClass: effect, Outcome: "pending"}
	if recovering != nil {
		op = *recovering
		// Old admission-only records have no descriptive evidence yet.
		if op.CallID == "" {
			op.CallID = in.Call.ID
		}
		if op.Name == "" {
			op.Name = in.Call.Name
		}
	} else if err = evidence.PutOperation(ctx, in.RunID, op); err != nil {
		return nil, classified(err)
	}
	trace := state.TaskStep{
		ID: in.OperationID, Kind: "tool", Name: in.Call.Name, Status: "running", Revision: in.Revision,
		StartedAt: time.Now().UTC(), Arguments: traceJSON(in.Call.Args), CostStatus: "not_applicable",
	}
	a.recordStep(in.RunID, trace)
	defer func() { trace.Result = traceJSON(result); finishStep(&trace, err); a.recordStep(in.RunID, trace) }()
	// Persist even if the request context was interrupted after tool execution.
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
			return nil, classified(e)
		}
		save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		op.ResultRef, e = evidence.AppendHistory(save, in.RunID, "", data)
		if e != nil {
			observability.RecordErrorCategory(save, "task.evidence", e)
			return nil, classified(e)
		}
		op.Outcome = outcome
		if outcome == "failed" && op.Cleared == "" {
			op.Cleared = failureCleared(in.Call, output)
		}
		if id, yes := output["artifact_id"].(string); yes && id != "" {
			op.ArtifactIDs = []string{id}
		}
		if e = evidence.PutOperation(save, in.RunID, op); e != nil {
			observability.RecordErrorCategory(save, "task.evidence", e)
			return nil, classified(e)
		}
		span.SetAttributes(attribute.String("nori.tool.outcome", outcome), attribute.Int("nori.operation.attempt", op.Attempt))
		observability.Event(save, "task.operation.recorded", attribute.String("nori.operation.id", op.ID), attribute.String("nori.tool.outcome", outcome))
		return output, nil
	}

	if agent.RuntimeTool(in.Call.Name) {
		return a.runtimeTool(ctx, in, &op, recovering != nil, persist)
	}
	if recovering != nil {
		inspect, stop := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer stop()
		// Recover by inspection only: a stopped guest remains stopped, and an
		// unknown/pending write is never submitted again.
		c, attachErr := computer.Attach(observability.SuppressErrorCapture(inspect), a.Agent.Computer, computerRun(in.Step))
		if attachErr == nil {
			defer c.Close()
			c.SetTaskID(in.RunID)
			receiptIDs := []string{in.OperationID}
			if op.Attempt > 1 {
				receiptIDs = []string{fmt.Sprintf("%s-a%d", in.OperationID, op.Attempt)}
			} else if op.Attempt == 0 && effect == "read" {
				receiptIDs = []string{in.OperationID + "-a2", in.OperationID}
			}
			for _, receiptID := range receiptIDs {
				receipt, readErr := c.Operation(inspect, receiptID)
				if readErr != nil {
					var rpc *computer.RPCError
					if errors.As(readErr, &rpc) && rpc.Status == 404 {
						continue
					}
					break
				}
				if receipt.Status != "completed" {
					break
				}
				output, outcome, recovered := a.savedToolReceipt(inspect, in, receipt)
				if recovered {
					return persist(outcome, output, nil)
				}
				break
			}
		}
		return map[string]any{
			"operation_id": op.ID, "outcome": "uncertain", "effect_class": op.EffectClass,
			"error": "The saved guest receipt does not establish this operation's result. Ask Austin; this operation was not repeated.",
		}, nil
	}
	// Only starting a job or a one-shot tool may start the computer. A job can't be
	// running on a stopped computer, so asking about one doesn't start it.
	connect := computer.Open
	if existingJobTool(in.Call.Name) {
		connect = computer.Attach
	}
	c, err := connect(observability.SuppressErrorCapture(ctx), a.Agent.Computer, computerRun(in.Step))
	if errors.Is(err, computer.ErrStopped) && existingJobTool(in.Call.Name) {
		return persist("failed", lostJob(in.Call), nil)
	}
	// Nothing has reached the computer yet, so these failures can't have changed anything.
	if err != nil {
		op.Cleared = state.ClearedDidNotRun
		return persist("failed", nil, err)
	}
	defer c.Close()
	c.SetTaskID(in.RunID)
	if err = c.RequireReceipts(); err != nil {
		op.Cleared = state.ClearedDidNotRun
		return persist("failed", nil, err)
	}
	if in.Call.Name == "job_start" {
		err = c.RequireJobs()
	}
	if err != nil {
		op.Cleared = state.ClearedDidNotRun
		return persist("failed", nil, err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		op.Attempt = attempt
		if _, err = a.Store.Guard(ctx, in.RunID, in.Revision); err != nil {
			_, saveErr := persist("skipped", nil, err)
			if saveErr != nil {
				return nil, saveErr
			}
			return nil, classified(err)
		}
		receiptID := in.OperationID
		if attempt > 1 {
			receiptID = fmt.Sprintf("%s-a%d", in.OperationID, attempt)
		}
		// Save the receipt identity before dispatch so a crash during a read
		// retry can inspect the second receipt without repeating either attempt.
		if err = evidence.PutOperation(ctx, in.RunID, op); err != nil {
			return nil, classified(err)
		}
		c.SetOperation(receiptID)
		callCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		output, callErr := a.Agent.Tool(observability.SuppressErrorCapture(callCtx), in.RunID, c, in.Call)
		cancel()
		if callErr == nil {
			return persist(toolOutcome(output), output, nil)
		}
		// Inspect a retained receipt without resubmitting an operation after a lost
		// response. A shell receipt proves the process result, not a remote effect.
		inspect, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		receipt, receiptErr := c.Operation(inspect, receiptID)
		if effect == "read" && ctx.Err() != nil {
			cancelErr := c.CancelRead(inspect, receiptID)
			stop()
			outcome := "skipped"
			if cancelErr != nil {
				outcome = "uncertain"
			}
			output, saveErr := persist(outcome, nil, callErr)
			if saveErr != nil {
				return nil, saveErr
			}
			observability.Event(context.WithoutCancel(ctx), "task.operation.interrupted",
				attribute.String("nori.operation.id", op.ID), attribute.Bool("nori.operation.stop_confirmed", cancelErr == nil))
			if cancelErr != nil {
				return output, nil
			}
			check, finish := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			current, e := a.Store.GetRun(check, in.RunID)
			finish()
			if e == nil && current.CancelRequested {
				return nil, classified(state.ErrCancelled)
			}
			if e == nil && current.Revision != in.Revision {
				return nil, classified(state.ErrSuperseded)
			}
			return nil, a.ioError(in.Step, callErr)
		}
		stop()

		if receiptErr == nil && receipt.Status == "completed" {
			if recovered, outcome, ok := a.savedToolReceipt(ctx, in, receipt); ok {
				return persist(outcome, recovered, nil)
			}
		}
		if effect == "read" && attempt == 1 && computer.Transient(callErr) && ctx.Err() == nil {
			// A new transport attempt is safe only for an allowlisted read. The
			// original operation identity and attempt count remain in its record.
			observability.Event(ctx, "task.operation.retry", attribute.String("nori.operation.id", op.ID), attribute.Int("nori.operation.attempt", attempt+1))
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return persist("skipped", nil, ctx.Err())
			case <-timer.C:
			}
			continue
		}
		outcome := "uncertain"
		if effect == "read" || (receiptErr == nil && receipt.Status == "completed") {
			outcome = "failed"
		}
		return persist(outcome, nil, callErr)
	}
	return nil, classified(errors.New("read attempts exhausted"))
}

// savedToolReceipt projects retained output without issuing another guest tool.
// A shell receipt establishes its process result, not an unobserved remote effect.
func (a *Activities) savedToolReceipt(ctx context.Context, in ToolInput, receipt computer.OperationReceipt) (map[string]any, string, bool) {
	if receipt.HTTPStatus < 200 || receipt.HTTPStatus >= 300 {
		return nil, "", false
	}
	if in.Call.Name == "browser_screenshot" {
		if len(receipt.Body) < 8 || !bytes.Equal(receipt.Body[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
			return nil, "", false
		}
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(receipt.ID)).String()
		if err := a.Store.PutPNG(ctx, in.RunID, id, receipt.Body); err != nil {
			return nil, "", false
		}
		if err := a.Store.Artifact(ctx, in.RunID, state.Artifact{ID: id, Kind: "screenshot"}); err != nil {
			return nil, "", false
		}
		return map[string]any{
			"run_id": in.RunID, "artifact_id": id,
			"description": "Private browser screenshot saved for Austin. Call screenshot_inspect with this artifact_id to read it.",
		}, "confirmed", true
	}
	var output map[string]any
	if json.Unmarshal(receipt.Body, &output) != nil || output == nil {
		return nil, "", false
	}
	return output, toolOutcome(output), true
}
