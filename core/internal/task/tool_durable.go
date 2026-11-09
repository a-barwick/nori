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
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

type persistFunc func(outcome string, output map[string]any, cause error) (map[string]any, error)

// durableTool records authority and outcomes independently of best-effort debug
// traces. Retrying an activity is never a substitute for an operation receipt.
func (a *Activities) durableTool(parent context.Context, in ToolInput) (result map[string]any, err error) {
	evidence := a.Store
	policy := agent.PolicyFor(in.Call.Name, in.Exec)
	effect := policy.Effect
	// A settled result remains evidence after a correction. Reuse it before
	// applying the current revision and pause checks to any further work.
	var recovering *state.OperationRecord
	var saved map[string]any
	err = a.waitForReceipt(parent, in.RunID, func() (time.Time, error) {
		var err error
		recovering, saved, err = a.savedOperation(parent, in)
		if recovering != nil {
			return recovering.RecoveryUntil, err
		}
		return time.Time{}, err
	})
	if errors.Is(err, state.ErrCancelled) {
		return nil, classified(err)
	}
	if err != nil || saved != nil {
		return saved, err
	}
	// A correction waits for paid model reads because interruption can lose the reported cost. Stop still cancels them.
	ctx, done, run, err := a.bounded(parent, in.Step, effect == "read" && !policy.InterruptedCostUnknown)
	if err != nil {
		return nil, err
	}
	defer done()
	ctx, span := startActivity(ctx, "task.tool", "TOOL", run, in.Revision)
	defer span.End()
	span.SetAttributes(
		attribute.String("tool.name", policy.Name), attribute.String("nori.operation.id", in.OperationID),
		attribute.String("nori.operation.effect_class", effect),
	)
	until := receiptDeadline(parent)
	var plan *externalPlan
	if in.Exec != nil && !in.Exec.Builtin {
		var stop map[string]any
		plan, stop, err = a.planExternal(ctx, in, recovering != nil)
		if recovering != nil && (err != nil || stop != nil) {
			// The account or implementation is no longer usable; recovery can only settle what is known.
			plan, err = &externalPlan{}, nil
		} else if err != nil || stop != nil {
			return stop, err
		} else {
			effect = string(plan.prepared.Effect)
			span.SetAttributes(attribute.String("nori.tool.definition", in.Exec.Tool.ID), attribute.String("nori.tool.digest", in.Exec.Tool.Digest))
			if plan.call.Grant != nil {
				span.SetAttributes(attribute.String("nori.connection.id", plan.call.Grant.Connection.ID))
			}
		}
	}
	if recovering == nil && plan == nil && !policy.Runtime && in.Call.Name != "complete" {
		if gated, err := a.computerGate(ctx, in, effect); err != nil || gated != nil {
			return gated, err
		}
	}
	if recovering == nil {
		if blocked, err := a.unresolvedWriteBlock(ctx, in.RunID, effect); err != nil || blocked != nil {
			return blocked, err
		}
		if err = a.Store.Admit(ctx, in.RunID, in.Revision, in.OperationID, until); err != nil {
			if errors.Is(err, docstore.ErrExists) {
				// A timed-out attempt may have committed admission just before this one.
				return nil, err
			}
			return nil, classified(err)
		}
	}
	op := state.OperationRecord{
		ID: in.OperationID, CallID: in.Call.ID, Name: in.Call.Name, Revision: in.Revision,
		EffectClass: effect, Outcome: "pending", RecoveryUntil: until,
	}
	if in.Exec != nil {
		op.Tool = &state.DefinitionRef{ID: in.Exec.Tool.ID, Version: in.Exec.Tool.Version, Digest: in.Exec.Tool.Digest}
	}
	if plan != nil {
		op.Action = &state.Action{Digest: plan.digest, Text: clip(plan.prepared.Summary, 2000), Spec: plan.prepared.Spec}
		op.AuthorityRef = plan.authority.AuthorityRef
		if plan.call.Grant != nil {
			op.ConnectionID = plan.call.Grant.Connection.ID
		}
	}
	if recovering != nil {
		op = *recovering
		// Old admission-only records have no descriptive evidence yet.
		if op.CallID == "" {
			op.CallID = in.Call.ID
			op.EffectClass = effect
			op.Outcome = "pending"
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
	if policy.Runtime {
		return a.runtimeTool(ctx, in, &op, recovering != nil, policy, persist)
	}
	if recovering != nil {
		return a.recoverComputerTool(ctx, in, op, effect, persist)
	}
	return a.computerTool(ctx, in, &op, effect, persist)
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
		return nil, nil, classified(errors.New("operation recovery identity conflict"))
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
		return nil, nil, classified(errors.New("invalid saved operation result"))
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

// recoverComputerTool recovers an interrupted computer call by inspection only: a stopped guest remains stopped,
// and an unknown or pending write is never submitted again.
func (a *Activities) recoverComputerTool(ctx context.Context, in ToolInput, op state.OperationRecord, effect string, persist persistFunc) (map[string]any, error) {
	inspect, stop := context.WithTimeout(observability.SuppressErrorCapture(ctx), 15*time.Second)
	defer stop()
	until, _ := inspect.Deadline()
	receiptIDs := []string{in.OperationID}
	if op.Attempt > 1 {
		receiptIDs = []string{fmt.Sprintf("%s-a%d", in.OperationID, op.Attempt)}
	} else if op.Attempt == 0 && effect == "read" {
		receiptIDs = []string{in.OperationID + "-a2", in.OperationID}
	}
	var c *computer.Client
	defer func() {
		if c != nil {
			c.Close()
		}
	}()
	var output map[string]any
	var outcome string
	err := a.waitForReceipt(inspect, in.RunID, func() (time.Time, error) {
		if c == nil {
			if a.Agent.Computer == nil {
				return time.Time{}, nil
			}
			attached, err := computer.Attach(inspect, a.Agent.Computer, computerRun(in.Step))
			if errors.Is(err, computer.ErrStopped) {
				return time.Time{}, nil
			}
			if err != nil {
				return until, nil
			}
			c = attached
			c.SetTaskID(in.RunID)
		}
		for _, receiptID := range receiptIDs {
			receipt, readErr := c.Operation(inspect, receiptID)
			if readErr != nil {
				var rpc *computer.RPCError
				if errors.As(readErr, &rpc) && rpc.Status == 404 {
					continue
				}
				c.Close()
				c = nil
				return until, nil
			}
			if receipt.Status != "completed" {
				// The guest reports an in-flight receipt as uncertain. It can
				// finish after the activity that dispatched it has timed out.
				return until, nil
			}
			output, outcome, _ = a.savedToolReceipt(inspect, in, receipt)
			return time.Time{}, nil
		}
		return until, nil
	})
	if output != nil {
		return persist(outcome, output, nil)
	}
	if errors.Is(err, state.ErrCancelled) {
		return nil, classified(err)
	}
	if ctx.Err() != nil {
		return nil, a.ioError(in.Step, ctx.Err())
	}
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	output = map[string]any{
		"operation_id": op.ID, "outcome": "uncertain", "effect_class": op.EffectClass,
		"error": prompts.Runtime("receipt-unknown", nil),
	}
	if op.Outcome == "pending" {
		// Keep a durable uncertain result so an answer can resolve the hold.
		return persist("uncertain", output, nil)
	}
	return output, nil
}

// computerTool runs a call on the task's computer. Only an allowlisted read is attempted a second time.
func (a *Activities) computerTool(ctx context.Context, in ToolInput, op *state.OperationRecord, effect string, persist persistFunc) (map[string]any, error) {
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
	if !existingJobTool(in.Call.Name) {
		// A question or takeover stops the computer for this run without changing its revision; reopen the drained
		// run so calls after the user answers are admitted. Calls that were in flight stay cancelled.
		if err = c.Reopen(ctx); err != nil {
			op.Cleared = state.ClearedDidNotRun
			return persist("failed", nil, err)
		}
	}
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
		if err = a.Store.PutOperation(ctx, in.RunID, *op); err != nil {
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
			return a.interruptedRead(ctx, in, op, cancelErr, callErr, persist)
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
		// Saving a result changes nothing outside Nori and is safe to repeat, so a failure is a plain failure.
		outcome := "uncertain"
		if effect == "read" || in.Call.Name == "result_save" || (receiptErr == nil && receipt.Status == "completed") {
			outcome = "failed"
		}
		return persist(outcome, nil, callErr)
	}
	return nil, classified(errors.New("read attempts exhausted"))
}

func (a *Activities) interruptedRead(ctx context.Context, in ToolInput, op *state.OperationRecord, cancelErr, callErr error, persist persistFunc) (map[string]any, error) {
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
			"description": prompts.Runtime("screenshot-saved", nil),
		}, "confirmed", true
	}
	var output map[string]any
	if json.Unmarshal(receipt.Body, &output) != nil || output == nil {
		return nil, "", false
	}
	return output, toolOutcome(output), true
}
