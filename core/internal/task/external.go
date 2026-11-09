package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"nori/internal/computersession"
	"nori/internal/connections"
	"nori/internal/invoke"
	"nori/internal/permissions"
	"nori/internal/state"
)

// Executor holds what the Tool executor needs for registry tools beyond the engine's own: adapters, the user's
// Connections and Permission checks. A nil Executor leaves registry tools unavailable, never silently substituted.
type Executor struct {
	Adapters    *invoke.Registry
	Connections *connections.Service
	Permissions *permissions.Service
	// Sessions decides who controls the computer; nil leaves computer tools ungated.
	Sessions *computersession.Service
}

// computerGate gives the task the computer for an engine tool that runs on it, or ends the call unadmitted: while
// the user controls the computer the task waits for them to resume; while another task holds it, the call is
// refused so it can be retried.
func (a *Activities) computerGate(ctx context.Context, in ToolInput, effect string) (map[string]any, error) {
	if a.Executor == nil || a.Executor.Sessions == nil {
		return nil, nil
	}
	err := a.Executor.Sessions.Acquire(ctx, in.RunID)
	switch {
	case errors.Is(err, computersession.ErrUserControl):
		run, _ := a.Store.GetRun(ctx, in.RunID)
		prompt := "The user is controlling the computer. Nori continues this task when the user resumes it."
		interaction, err := a.Executor.Sessions.Handoff(ctx, in.RunID, in.Revision, "user_requested", prompt, clip(run.Request, 200))
		if err != nil {
			return nil, err
		}
		return denial(in, effect, "needs_interaction", prompt, map[string]any{"interaction_id": interaction.ID, "interaction_kind": "computer_takeover", "prompt": prompt}), nil
	case errors.Is(err, computersession.ErrBusy):
		return denial(in, effect, "failed", "Another task is using the computer right now. Continue with other work, then try again.", nil), nil
	}
	return nil, err
}

// handoff asks the user to take over the computer, for example to sign in. The task waits on the interaction.
func (a *Activities) handoff(ctx context.Context, in ToolInput) (map[string]any, error) {
	if a.Executor == nil || a.Executor.Sessions == nil || a.Executor.Permissions == nil {
		return denial(in, "read", "failed", "The computer is not available for a handoff in this deployment.", nil), nil
	}
	reason := invoke.String(in.Call.Args, "reason")
	if reason == "" {
		reason = "blocked"
	}
	prompt := invoke.String(in.Call.Args, "message")
	if prompt == "" {
		prompt = "Nori needs you to take over the computer."
	}
	run, _ := a.Store.GetRun(ctx, in.RunID)
	interaction, err := a.Executor.Sessions.Handoff(ctx, in.RunID, in.Revision, reason, prompt, clip(run.Request, 200))
	if err != nil {
		return nil, err
	}
	return denial(in, "read", "needs_interaction", prompt, map[string]any{"interaction_id": interaction.ID, "interaction_kind": "computer_takeover", "prompt": prompt}), nil
}

// externalPlan is what was resolved before admission: the adapter, the account access for this attempt, the
// prepared action and the authority that admits it.
type externalPlan struct {
	adapter   invoke.Adapter
	call      invoke.Call
	prepared  invoke.Prepared
	digest    string
	authority permissions.Decision
}

func denial(in ToolInput, effect, outcome, message string, extra map[string]any) map[string]any {
	output := map[string]any{"operation_id": in.OperationID, "outcome": outcome, "effect_class": effect, "error": message, "admitted": false}
	for k, v := range extra {
		output[k] = v
	}
	return output
}

// taskOrigin says who the task acts for: a direct user request, an automation occurrence or a goal.
func taskOrigin(run state.Run) (origin, automationID, goalID string) {
	if run.Origin != nil {
		switch {
		case run.Origin.AutomationID != "":
			return "automation", run.Origin.AutomationID, run.Origin.GoalID
		case run.Origin.GoalID != "":
			return "goal", "", run.Origin.GoalID
		}
	}
	return "user", "", ""
}

func interactionID(prefix, operationID, digest string) string {
	sum := sha256.Sum256([]byte(operationID + "\x00" + digest))
	return prefix + hex.EncodeToString(sum[:16])
}

// planExternal resolves a registry tool call before admission. A non-nil output ends the call without admitting
// an operation: the tool is unavailable, the account must be chosen or connected, arguments are invalid, the
// action is denied, or it waits on the user's approval.
func (a *Activities) planExternal(ctx context.Context, in ToolInput, recovering bool) (*externalPlan, map[string]any, error) {
	effect := string(in.Exec.Policy.Effect)
	if in.Exec.Binding.Target == "nori.computer/handoff" && !recovering {
		out, err := a.handoff(ctx, in)
		return nil, out, err
	}
	adapter, ok := a.Executor.adapter(in)
	if !ok {
		return nil, denial(in, effect, "failed", "This tool's implementation is not available in this worker version, so it did not run.", nil), nil
	}
	run, err := a.Store.GetRun(ctx, in.RunID)
	if err != nil {
		return nil, nil, err
	}
	call := invoke.Call{RunID: in.RunID, Revision: in.Revision, OperationID: in.OperationID, Exec: *in.Exec, Args: in.Call.Args,
		Upload: func(ctx context.Context, id string) (string, string, []byte, error) {
			attachment, data, err := a.Agent.TaskUpload(ctx, in.RunID, id)
			return attachment.Name, attachment.MediaType, data, err
		}}
	connectionID, epoch := "", int64(0)
	if in.Exec.ConnectionType != "" {
		if a.Executor.Connections == nil {
			return nil, denial(in, effect, "failed", "Connections are not available in this deployment.", nil), nil
		}
		requested := invoke.String(in.Call.Args, "account")
		record, _, err := a.Executor.Connections.Select(ctx, in.Exec.ConnectionType, in.Exec.RequiredCapability, requested)
		var choose *connections.ChooseAccount
		switch {
		case errors.As(err, &choose):
			if recovering {
				return nil, nil, err
			}
			out, err := a.chooseAccount(ctx, in, run, choose)
			return nil, out, err
		case errors.Is(err, connections.ErrUnknownAccount):
			return nil, denial(in, effect, "failed", fmt.Sprintf("No connected account matches %q. Use one of the connected accounts or ask the user; never switch accounts silently.", requested), map[string]any{"recovery": "choose_account"}), nil
		case errors.Is(err, connections.ErrInsufficientScope):
			return nil, denial(in, effect, "failed", "The connected account has not granted the access this needs. The user can grant it in Apps.", map[string]any{"recovery": "request_access", "integration_id": in.Exec.ConnectionType}), nil
		case errors.Is(err, connections.ErrNotConnected):
			return nil, denial(in, effect, "failed", "No account for this app is connected. The user can connect one in Apps.", map[string]any{"recovery": "connect", "integration_id": in.Exec.ConnectionType}), nil
		case err != nil:
			return nil, nil, err
		}
		grant, err := a.Executor.Connections.Grant(ctx, record.ID)
		if err != nil {
			return nil, denial(in, effect, "failed", "The account could not be used: "+err.Error(), map[string]any{"recovery": "repair", "connection_id": record.ID}), nil
		}
		call.Grant = &grant
		connectionID, epoch = record.ID, record.AuthorizationEpoch
	}
	prepared, err := adapter.Prepare(ctx, call)
	if err != nil {
		if errors.Is(err, invoke.ErrInvalidArguments) {
			return nil, denial(in, effect, "failed", err.Error(), nil), nil
		}
		if call.Grant != nil {
			a.Executor.Connections.ReportFailure(ctx, connectionID, err)
		}
		return nil, denial(in, effect, "failed", "The action could not be prepared: "+err.Error(), nil), nil
	}
	if prepared.Effect == "" {
		prepared.Effect = in.Exec.Policy.Effect
	}
	plan := &externalPlan{adapter: adapter, call: call, prepared: prepared, digest: invoke.Digest(call, connectionID, epoch, prepared)}
	if recovering {
		return plan, nil, nil
	}
	origin, automationID, goalID := taskOrigin(run)
	resourceIDs := make([]string, 0, len(prepared.Resources))
	for _, r := range prepared.Resources {
		resourceIDs = append(resourceIDs, r.ProviderID)
	}
	decision := permissions.Decision{Result: "allow", Source: "user_request"}
	if a.Executor.Permissions != nil {
		decision, err = a.Executor.Permissions.Check(ctx, permissions.Request{
			RunID: in.RunID, Revision: in.Revision, Origin: origin, AutomationID: automationID, GoalID: goalID, ReadOnly: run.ReadOnly,
			ToolID: in.Exec.Tool.ID, Effect: string(prepared.Effect), ConnectionID: connectionID, Epoch: epoch,
			ResourceIDs: resourceIDs, Recipients: prepared.Recipients, Digest: plan.digest,
		})
		if err != nil {
			return nil, nil, err
		}
	} else if prepared.Effect != "read" {
		decision = permissions.Decision{Result: "needs_interaction", Reason: "Permission checks are not configured; nothing may change without them."}
	}
	switch decision.Result {
	case "deny":
		return nil, denial(in, string(prepared.Effect), "denied", decision.Reason, nil), nil
	case "needs_interaction":
		if a.Executor.Permissions == nil {
			return nil, denial(in, string(prepared.Effect), "denied", decision.Reason, nil), nil
		}
		out, err := a.requestApproval(ctx, in, run, plan, connectionID)
		return nil, out, err
	}
	plan.authority = decision
	return plan, nil, nil
}

func (e *Executor) adapter(in ToolInput) (invoke.Adapter, bool) {
	if e == nil {
		return nil, false
	}
	return e.Adapters.Lookup(in.Exec.Binding)
}

func (a *Activities) requestApproval(ctx context.Context, in ToolInput, run state.Run, plan *externalPlan, connectionID string) (map[string]any, error) {
	label := ""
	if plan.call.Grant != nil {
		label = plan.call.Grant.Connection.Account.Label
	}
	resources := make([]map[string]any, 0, len(plan.prepared.Resources))
	for _, r := range plan.prepared.Resources {
		ref := map[string]any{"connection_id": connectionID, "kind": r.Kind, "provider_id": r.ProviderID}
		if r.ProviderRevision != "" {
			ref["provider_revision"] = r.ProviderRevision
		}
		resources = append(resources, ref)
	}
	payload := map[string]any{
		"summary": plan.prepared.Summary, "effect": string(plan.prepared.Effect),
		"account": map[string]any{"connection_id": connectionID, "label": label}, "resources": resources,
	}
	if len(plan.prepared.Recipients) > 0 {
		payload["recipients"] = plan.prepared.Recipients
	}
	if len(plan.prepared.MaterialEffects) > 0 {
		payload["material_effects"] = plan.prepared.MaterialEffects
	}
	data, _ := json.Marshal(payload)
	revision := in.Revision
	expires := time.Now().Add(24 * time.Hour).UTC()
	interaction, err := a.Executor.Permissions.Open(ctx, permissions.Interaction{
		ID: interactionID("int_", in.OperationID, plan.digest), TaskID: in.RunID, InstructionRevision: &revision, Kind: "approval",
		Prompt: plan.prepared.Summary, Payload: data, ActionDigest: plan.digest, ExpiresAt: &expires, TaskTitle: clip(run.Request, 200),
		ConnectionID: connectionID,
	}, "")
	if err != nil {
		return nil, err
	}
	return denial(in, string(plan.prepared.Effect), "needs_interaction", "This action waits for the user's approval.", map[string]any{
		"interaction_id": interaction.ID, "interaction_kind": "approval", "prompt": "Approve this action? " + plan.prepared.Summary,
	}), nil
}

func (a *Activities) chooseAccount(ctx context.Context, in ToolInput, run state.Run, choose *connections.ChooseAccount) (map[string]any, error) {
	effect := string(in.Exec.Policy.Effect)
	if a.Executor.Permissions == nil {
		return denial(in, effect, "failed", choose.Error(), nil), nil
	}
	ids, labels := []string{}, []string{}
	for _, record := range choose.Eligible {
		ids = append(ids, record.ID)
		labels = append(labels, fmt.Sprintf("%s (%s)", record.Account.Label, record.ID))
	}
	data, _ := json.Marshal(map[string]any{"purpose": choose.Purpose, "eligible_connection_ids": ids})
	revision := in.Revision
	prompt := fmt.Sprintf("Which account should Nori use for %s?", choose.Purpose)
	interaction, err := a.Executor.Permissions.Open(ctx, permissions.Interaction{
		ID: interactionID("int_", in.OperationID, "account:"+choose.Purpose), TaskID: in.RunID, InstructionRevision: &revision,
		Kind: "account_selection", Prompt: prompt, Payload: data, TaskTitle: clip(run.Request, 200),
	}, choose.Purpose)
	if err != nil {
		return nil, err
	}
	return denial(in, effect, "needs_interaction", choose.Error()+": "+fmt.Sprint(labels), map[string]any{
		"interaction_id": interaction.ID, "interaction_kind": "account_selection", "prompt": prompt,
	}), nil
}

// runExternal records the attempt, dispatches once and settles the outcome. An interrupted write is reconciled
// from provider state or stays uncertain; it is never blindly sent again.
func (a *Activities) runExternal(ctx context.Context, in ToolInput, op *state.OperationRecord, recovering bool, plan *externalPlan, persist persistFunc) (map[string]any, error) {
	connectionID := op.ConnectionID
	report := func(result invoke.Result) {
		if connectionID == "" || a.Executor.Connections == nil {
			return
		}
		if result.Err != nil && result.Outcome == "failed" {
			a.Executor.Connections.ReportFailure(ctx, connectionID, result.Err)
		} else if result.Err == nil {
			a.Executor.Connections.ReportSuccess(ctx, connectionID)
		}
	}
	settle := func(result invoke.Result) (map[string]any, error) {
		report(result)
		op.ProviderRefs = append(op.ProviderRefs, result.ProviderRefs...)
		output := result.Content
		if output == nil {
			output = map[string]any{}
		}
		if result.CostUSD > 0 {
			output["cost_usd"] = result.CostUSD
		}
		if result.CostUnknown {
			output["cost_unknown"] = true
		}
		if len(result.ProviderRefs) > 0 {
			output["provider_refs"] = result.ProviderRefs
		}
		outcome := result.Outcome
		if outcome == "" {
			outcome = "confirmed"
		}
		if (outcome == "failed" || outcome == "uncertain") && result.Cleared != "" && op.Cleared == "" {
			op.Cleared = result.Cleared
		}
		return persist(outcome, output, result.Err)
	}
	if recovering && plan.adapter == nil {
		outcome := "uncertain"
		if in.Exec.Policy.Effect == "read" {
			outcome = "failed"
		}
		return persist(outcome, map[string]any{"reconciled": false}, errors.New("the worker stopped before this call's result was saved, and the account or implementation is no longer available to check it"))
	}
	if recovering {
		if reconciler, ok := plan.adapter.(invoke.Reconciler); ok {
			if result, ok := reconciler.Reconcile(ctx, plan.call, plan.prepared); ok {
				return settle(result)
			}
		}
		if in.Exec.Policy.Retry != "safe" {
			return persist("uncertain", map[string]any{"reconciled": false}, errors.New("the worker stopped before this action's result was saved, and the provider's state could not confirm it"))
		}
	}
	if _, err := a.Store.Guard(ctx, in.RunID, in.Revision); err != nil {
		if _, saveErr := persist("skipped", nil, err); saveErr != nil {
			return nil, saveErr
		}
		return nil, classified(err)
	}
	op.Attempt++
	if err := a.Store.PutOperation(ctx, in.RunID, *op); err != nil {
		return nil, classified(err)
	}
	plan.call.Attempt = op.Attempt
	timeout := time.Duration(in.Exec.Policy.TimeoutMS) * time.Millisecond
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	result := plan.adapter.Execute(callCtx, plan.call, plan.prepared)
	cancel()
	// A safe read refused with 429 or 503 is retried a bounded number of times, honouring Retry-After up to a cap.
	// Writes are never retried here: a refusal that arrives after a partial effect is settled by reconciliation.
	for retry := 1; retry <= 2 && in.Exec.Policy.Retry == "safe" && result.Err != nil && ctx.Err() == nil; retry++ {
		var temporary *invoke.TemporaryError
		if !errors.As(result.Err, &temporary) {
			break
		}
		wait := min(max(temporary.After, time.Duration(retry*retry)*time.Second), 20*time.Second)
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
		if ctx.Err() != nil {
			break
		}
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		result = plan.adapter.Execute(callCtx, plan.call, plan.prepared)
		cancel()
	}
	if result.Outcome == "" {
		result.Outcome = "confirmed"
		if result.Err != nil {
			result.Outcome = "failed"
		}
	}
	if ctx.Err() != nil && result.Outcome == "failed" && in.Exec.Policy.Effect != "read" {
		// Cancellation or a timeout is not proof the provider did nothing.
		result.Outcome = "uncertain"
	}
	if result.Outcome == "uncertain" {
		// A lost response is resolved by reading provider state before anyone is asked; it is never resent.
		if reconciler, ok := plan.adapter.(invoke.Reconciler); ok {
			check, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
			reconciled, ok := reconciler.Reconcile(check, plan.call, plan.prepared)
			cancel()
			if ok {
				result = reconciled
			}
		}
	}
	return settle(result)
}
