package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nori/internal/connections"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/llmguard"
	"nori/internal/permissions"
	"nori/internal/state"
)

const ApprovalExpiry = 30 * time.Minute

type Executor struct {
	Adapters     *invoke.Registry
	Connections  *connections.Service
	Permissions  *permissions.Service
	AutoReviewer permissions.AutoReviewer
	Now          func() time.Time
	Sleep        func(context.Context, time.Duration) error
}

func (a *Activities) externalNow() time.Time {
	if a != nil && a.Executor != nil && a.Executor.Now != nil {
		return a.Executor.Now()
	}
	return a.now()
}

func (a *Activities) externalSleep(ctx context.Context, d time.Duration) error {
	if a != nil && a.Executor != nil && a.Executor.Sleep != nil {
		return a.Executor.Sleep(ctx, d)
	}
	return a.sleep(ctx, d)
}

type externalInspectSpend struct {
	mu      sync.Mutex
	cost    float64
	unknown bool
}

func (s *externalInspectSpend) add(cost float64, unknown bool) {
	if s == nil || (cost <= 0 && !unknown) {
		return
	}
	s.mu.Lock()
	if cost > 0 {
		s.cost += cost
	}
	if unknown {
		s.unknown = true
	}
	s.mu.Unlock()
}

func (s *externalInspectSpend) snapshot() (float64, bool) {
	if s == nil {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cost, s.unknown
}

type externalPlan struct {
	adapter          invoke.Adapter
	call             invoke.Call
	prepared         invoke.Prepared
	digest           string
	authority        permissions.Decision
	permReq          permissions.Request
	extraCost        float64
	extraCostUnknown bool
	execInspect      *externalInspectSpend
}

func addCostToOutput(out map[string]any, cost float64, unknown bool) {
	if out == nil || (cost <= 0 && !unknown) {
		return
	}
	if cost > 0 {
		prev, _ := out["cost_usd"].(float64)
		out["cost_usd"] = prev + cost
	}
	if unknown {
		out["cost_unknown"] = true
	}
}

func (a *Activities) chargeRunSpend(ctx context.Context, runID string, run state.Run, cost float64, unknown bool) {
	if a == nil || a.Store == nil || (cost <= 0 && !unknown) {
		return
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	current := run
	if latest, err := a.Store.GetRun(saveCtx, runID); err == nil {
		current = latest
	}
	_ = a.Store.Spend(saveCtx, state.SpendUpdate{
		RunID:       runID,
		Spent:       current.Spent + cost,
		Budget:      current.Budget,
		CostUnknown: current.CostUnknown || unknown,
	})
}

func denial(in ToolInput, effect, outcome, message string, extra map[string]any) map[string]any {
	output := map[string]any{"operation_id": in.OperationID, "outcome": outcome, "effect_class": effect, "error": message, "admitted": false}
	for k, v := range extra {
		output[k] = v
	}
	return output
}

func isUnattendedOrigin(run state.Run) bool {
	if run.Origin == nil {
		return false
	}
	return run.Origin.AutomationID != "" || run.Origin.ConversationID == AutomationConversationID
}

func taskOrigin(run state.Run) (origin, automationID string) {
	if isUnattendedOrigin(run) {
		return invoke.OriginAutomation, run.Origin.AutomationID
	}
	return invoke.OriginUser, ""
}

func isPersonalComputerTool(exec *extensions.Exec) bool {
	if exec == nil {
		return false
	}
	return strings.HasPrefix(exec.Tool.ID, "nori/computer/") ||
		strings.HasPrefix(exec.Binding.Target, "nori.computer/")
}

func interactionID(prefix, operationID, digest string) string {
	sum := sha256.Sum256([]byte(operationID + "\x00" + digest))
	return prefix + hex.EncodeToString(sum[:16])
}

func (a *Activities) planExternal(ctx context.Context, in ToolInput, saved *state.OperationRecord) (planOut *externalPlan, stopOut map[string]any, errOut error) {
	recovering := saved != nil
	effect := string(in.Exec.Policy.Effect)
	run, err := a.Store.GetRun(ctx, in.RunID)
	if err != nil {
		return nil, nil, err
	}
	var inPrepare atomic.Bool
	prepareSpend := &externalInspectSpend{}
	reviewSpend := &externalInspectSpend{}
	execInspect := &externalInspectSpend{}
	defer func() {
		pCost, pUnk := prepareSpend.snapshot()
		rCost, rUnk := reviewSpend.snapshot()
		totalCost := pCost + rCost
		totalUnk := pUnk || rUnk
		if planOut != nil {
			planOut.execInspect = execInspect
		}
		if totalCost <= 0 && !totalUnk {
			return
		}
		if planOut != nil {
			planOut.extraCost += totalCost
			planOut.extraCostUnknown = planOut.extraCostUnknown || totalUnk
			return
		}
		if stopOut != nil {
			addCostToOutput(stopOut, totalCost, totalUnk)
			return
		}
		if errOut != nil {
			a.chargeRunSpend(ctx, in.RunID, run, totalCost, totalUnk)
		}
	}()
	origin, automationID := taskOrigin(run)
	unattended := isUnattendedOrigin(run)
	proactiveComputer := unattended && isPersonalComputerTool(in.Exec)
	readOnly := run.ReadOnly || in.ReadOnly || proactiveComputer

	adapter, lookupErr := a.Executor.adapterForOrigin(in, origin)
	if errors.Is(lookupErr, invoke.ErrToolForbiddenForOrigin) {
		return nil, denial(in, effect, "denied", "Automations cannot create or modify automations or permission rules.", nil), nil
	}
	if adapter == nil {
		return nil, denial(in, effect, "failed", "This tool's implementation is not available in this worker version, so it did not run.", nil), nil
	}
	if checker, ok := adapter.(invoke.SignInChecker); ok && !recovering && a.Executor.Permissions != nil {
		start, err := checker.SignInNeeded(ctx, invoke.Call{RunID: in.RunID, Revision: in.Revision, OperationID: in.OperationID, Origin: origin, Exec: *in.Exec, Args: in.Call.Args}, "/work/"+in.RunID)
		if err != nil {
			return nil, denial(in, effect, "failed", "The server needs sign-in, which could not start: "+err.Error(), map[string]any{"recovery": "sign_in"}), nil
		}
		if start != nil {
			out, err := a.signInStarted(ctx, in, run, *start)
			return nil, out, err
		}
	}
	zone := ""
	if a.Executor.Connections != nil {
		if prefs, _, err := a.Executor.Connections.Preferences(ctx); err == nil {
			zone = prefs.Timezone
		}
	}
	userSources := a.verifiedUserSources(ctx, in.RunID, run)
	call := invoke.Call{
		RunID: in.RunID, Revision: in.Revision, OperationID: in.OperationID, Origin: origin, Exec: *in.Exec, Args: in.Call.Args,
		TimeZone: zone, ReadOnly: readOnly, UserSources: userSources,
		Upload: func(ctx context.Context, id string) (string, string, []byte, error) {
			attachment, data, err := a.Agent.TaskUpload(ctx, in.RunID, id)
			return attachment.Name, attachment.MediaType, data, err
		},
		InspectImage: func(ctx context.Context, label, instruction, question string, data []byte, mediaType string) (map[string]any, error) {
			if a.Agent == nil {
				return nil, errors.New("vision inspector is unavailable")
			}
			insp, iErr := a.agentForRun(ctx, run).InspectImage(ctx, "source", label, instruction, question, data, mediaType)
			var c float64
			var unk bool
			if insp != nil {
				c, _ = insp["cost_usd"].(float64)
				unk, _ = insp["cost_unknown"].(bool)
			}
			if c > 0 || unk {
				if inPrepare.Load() {
					prepareSpend.add(c, unk)
				} else {
					execInspect.add(c, unk)
				}
			}
			return insp, iErr
		},
		PublishFile: func(ctx context.Context, title, fileName, mediaType string, data []byte) (state.Result, error) {
			id := state.ResultID(in.RunID, in.OperationID, fileName)
			if err := a.Store.PutFile(ctx, state.ResultKey(id), mediaType, data); err != nil && !errors.Is(err, state.ErrFileExists) {
				return state.Result{}, err
			}
			return a.Store.SaveResult(ctx, state.Result{
				ID: id, RootID: id, Version: 1, RunID: in.RunID, Revision: in.Revision,
				Kind: state.ResultFile, Title: title, Name: fileName, MediaType: mediaType,
				Size: int64(len(data)), ArtifactID: id, CreatedAt: a.externalNow().UTC(),
			})
		},
	}
	if requester, ok := adapter.(invoke.TakeoverRequester); ok && !recovering && a.Executor.Permissions != nil {
		req, err := requester.TakeoverNeeded(ctx, call)
		if err != nil {
			if errors.Is(err, invoke.ErrInvalidArguments) {
				return nil, denial(in, effect, "failed", err.Error(), nil), nil
			}
			return nil, denial(in, effect, "failed", "Desktop takeover could not start: "+err.Error(), nil), nil
		}
		if req != nil {
			if proactiveComputer {
				return nil, denial(in, effect, "denied", "Proactive computer runs are read-only and cannot request desktop takeover.", nil), nil
			}
			out, err := a.requestTakeover(ctx, in, run, *req)
			return nil, out, err
		}
	}
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
			if record, ok := a.Executor.Connections.LackingCapability(ctx, in.Exec.ConnectionType, in.Exec.RequiredCapability, requested); ok && !recovering {
				out, err := a.signIn(ctx, in, run, record, in.Exec.RequiredCapability)
				return nil, out, err
			}
			return nil, denial(in, effect, "failed", "The connected account has not granted the access this needs. The user can grant it in Apps.", map[string]any{"recovery": "request_access", "integration_id": in.Exec.ConnectionType}), nil
		case errors.Is(err, connections.ErrNotConnected):
			if record, ok := a.Executor.Connections.NeedsSignIn(ctx, in.Exec.ConnectionType, requested); ok && !recovering {
				out, err := a.signIn(ctx, in, run, record, "")
				return nil, out, err
			}
			if requested == "" && !recovering {
				record, err := a.Executor.Connections.ConnectTarget(ctx, in.Exec.ConnectionType, interactionID("con_", in.OperationID, in.Exec.ConnectionType))
				if err == nil {
					out, err := a.signIn(ctx, in, run, record, in.Exec.RequiredCapability)
					return nil, out, err
				}
				if !errors.Is(err, connections.ErrNotConfigured) {
					return nil, nil, err
				}
			}
			return nil, denial(in, effect, "failed", "No account for this app is connected. The user can connect one in Apps.", map[string]any{"recovery": "connect", "integration_id": in.Exec.ConnectionType}), nil
		case err != nil:
			return nil, nil, err
		}
		grant, err := a.Executor.Connections.Grant(ctx, record.ID)
		if errors.Is(err, connections.ErrNotConnected) && !recovering {
			if again, ok := a.Executor.Connections.NeedsSignIn(ctx, in.Exec.ConnectionType, record.ID); ok {
				record = again
			}
			out, err := a.signIn(ctx, in, run, record, "")
			return nil, out, err
		}
		if err != nil {
			return nil, denial(in, effect, "failed", "The account could not be used: "+err.Error(), map[string]any{"recovery": "repair", "connection_id": record.ID}), nil
		}
		if grant.Connection.AuthorizationEpoch != record.AuthorizationEpoch {
			return nil, denial(in, effect, "failed", "The account changed while preparing this action. Try again with the connected account.", nil), nil
		}
		call.Grant = &grant
		connectionID, epoch = record.ID, record.AuthorizationEpoch
	}
	if recovering && saved.Action != nil && saved.Action.Intent && saved.EffectClass != string(extensions.EffectRead) {
		intent := saved.Action
		prepared := invoke.Prepared{Summary: intent.Text, Effect: extensions.Effect(saved.EffectClass), Precondition: intent.Precondition,
			Spec: intent.Spec, Keys: intent.Keys}
		return &externalPlan{adapter: adapter, call: call, prepared: prepared, digest: intent.Digest}, nil, nil
	}
	inPrepare.Store(true)
	prepared, err := adapter.Prepare(ctx, call)
	inPrepare.Store(false)
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
	// If the user edited and approved this exact action via replacement, apply
	// ONLY the allowlisted edited fields from the approved replacement record
	// and re-prepare so execution uses the approved replacement payload.
	if a.Executor.Permissions != nil {
		if repl, replErr := a.Executor.Permissions.FindApprovedReplacement(ctx, in.RunID, in.Revision, plan.digest); replErr == nil && repl != nil && len(repl.EditedFields) > 0 {
			editedCall := call
			editedCall.Args = cloneArgsMap(call.Args)
			applyAllowlistedEditsToArgs(editedCall.Args, repl.EditedFields)
			inPrepare.Store(true)
			editedPrepared, prepErr := adapter.Prepare(ctx, editedCall)
			inPrepare.Store(false)
			if prepErr != nil {
				return nil, denial(in, effect, "failed", "The approved edited action could not be prepared: "+prepErr.Error(), nil), nil
			}
			if editedPrepared.Effect == "" {
				editedPrepared.Effect = in.Exec.Policy.Effect
			}
			plan.call = editedCall
			plan.prepared = editedPrepared
			plan.digest = repl.ReplacementDigest
			prepared = editedPrepared
		}
	}
	resourceIDs := make([]string, 0, len(prepared.Resources))
	for _, r := range prepared.Resources {
		resourceIDs = append(resourceIDs, r.ProviderID)
	}
	permReq := permissions.Request{
		RunID: in.RunID, Revision: in.Revision, Origin: origin, AutomationID: automationID, ReadOnly: readOnly,
		ToolID: in.Exec.Tool.ID, Effect: string(prepared.Effect), ConnectionID: connectionID, Epoch: epoch,
		ResourceIDs: resourceIDs, Recipients: prepared.Recipients, Digest: plan.digest,
		RequireExplicitApproval: prepared.RequireExplicitApproval, OperationID: in.OperationID,
	}
	decision := permissions.Decision{Result: "allow", Source: "user_request"}
	if a.Executor.Permissions != nil {
		decision, err = a.Executor.Permissions.Check(ctx, permReq)
		if err != nil {
			return nil, nil, err
		}
	} else if readOnly && prepared.Effect != extensions.EffectRead {
		decision = permissions.Decision{Result: "deny", Reason: "This task is read-only and cannot change external or computer state."}
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
		out, err := a.requestApproval(ctx, in, run, plan, connectionID, decision.Reason)
		return nil, out, err
	}
	if prepared.RequireConfirmation && !decision.ApprovedByHuman && decision.Source != "approval" && decision.Source != "permission_rule" {
		if a.Executor.Permissions == nil {
			return nil, denial(in, string(prepared.Effect), "denied", "Permission checks are not configured; nothing may change without them.", nil), nil
		}
		out, err := a.requestApproval(ctx, in, run, plan, connectionID, "")
		return nil, out, err
	}
	skipAutoReview := prepared.SkipAutoReview && origin == "user" && isInternalNoriTool(in.Exec)
	if prepared.Effect != extensions.EffectRead && prepared.Effect != extensions.EffectControl &&
		!decision.ApprovedByHuman && decision.Source != "approval" && !skipAutoReview {
		reviewer := a.Executor.AutoReviewer
		if reviewer == nil && a.Executor.Permissions != nil {
			reviewer = permissions.DeterministicAutoReviewer{}
		}
		if reviewer != nil {
			reviewCtx := llmguard.WithCostObserver(ctx, func(costUSD float64, costKnown, sent bool) {
				if !sent {
					return
				}
				if costKnown {
					reviewSpend.add(costUSD, false)
				} else {
					reviewSpend.add(costUSD, true)
				}
			})
			review, reviewErr := reviewer.ReviewAction(reviewCtx, permissions.AutoReviewRequest{
				ToolID:         in.Exec.Tool.ID,
				Summary:        prepared.Summary,
				Effect:         string(prepared.Effect),
				ConnectionID:   connectionID,
				Recipients:     prepared.Recipients,
				Resources:      resourceIDs,
				Origin:         origin,
				RuleSummaries:  decision.RuleSummaries,
				DecisionSource: decision.Source,
				Now:            a.externalNow().UTC(),
				TimeZone:       call.TimeZone,
				UserSources:    a.verifiedUserSources(ctx, in.RunID, run),
			})
			if reviewErr != nil {
				review = permissions.AutoReviewDecision{
					Verdict: "ask",
					Reason:  "Action safety review could not complete; please confirm this action.",
				}
			}
			switch review.Verdict {
			case "deny":
				reason := review.Reason
				if reason == "" {
					reason = "Blocked by action-boundary safety review."
				}
				return nil, denial(in, string(prepared.Effect), "denied", reason, nil), nil
			case "ask":
				if a.Executor.Permissions == nil {
					return nil, denial(in, string(prepared.Effect), "denied", review.Reason, nil), nil
				}
				out, err := a.requestApproval(ctx, in, run, plan, connectionID, review.Reason)
				return nil, out, err
			}
		}
	}
	plan.authority = decision
	plan.permReq = permReq
	return plan, nil, nil
}

func (a *Activities) verifiedUserSources(ctx context.Context, runID string, run state.Run) []string {
	var out []string
	if a.Store != nil {
		if inputs, err := a.Store.ReadTaskSourceInputs(ctx, runID); err == nil {
			for _, input := range inputs {
				if input.Event != nil {
					continue
				}
				if text := strings.TrimSpace(input.Text); text != "" {
					out = append(out, text)
				}
			}
		}
	}
	if len(out) == 0 {
		if req := strings.TrimSpace(run.Request); req != "" {
			out = append(out, req)
		}
	}
	return out
}

func isInternalNoriTool(exec *extensions.Exec) bool {
	return exec != nil && exec.ConnectionType == "" && strings.HasPrefix(exec.Tool.ID, "nori/")
}

func approvalWarning(effect extensions.Effect, internalNori bool, reason string) string {
	trimmed := strings.TrimSpace(reason)
	generic := trimmed == "" || trimmed == "This action needs the user's approval."
	if internalNori {
		switch effect {
		case extensions.EffectSensitiveSend:
			base := "This routine will be allowed to send or change things on your connected accounts automatically."
			if generic {
				return base
			}
			return base + " " + trimmed
		default:
			if generic {
				return ""
			}
			return trimmed
		}
	}
	base := "This performs an external action."
	switch effect {
	case extensions.EffectSensitiveSend:
		base = "This sends or shares data outside this chat."
	case extensions.EffectReversibleWrite:
		base = "This modifies external records."
	case extensions.EffectUnknown:
		base = "This performs an unclassified external action."
	}
	if generic {
		return base
	}
	return base + " " + trimmed
}

func (e *Executor) adapter(in ToolInput) (invoke.Adapter, bool) {
	if e == nil {
		return nil, false
	}
	return e.Adapters.Lookup(in.Exec.Binding)
}

func (e *Executor) adapterForOrigin(in ToolInput, origin string) (invoke.Adapter, error) {
	if e == nil || e.Adapters == nil {
		return nil, nil
	}
	return e.Adapters.LookupForOrigin(in.Exec.Binding, origin, in.Exec.Tool.ID, in.Call.Name)
}

func extractEditableFields(args map[string]any) map[string]string {
	if len(args) == 0 {
		return nil
	}
	out := map[string]string{}
	if s, ok := args["subject"].(string); ok {
		out["subject"] = s
	}
	if b, ok := args["body"].(string); ok {
		out["body"] = b
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (a *Activities) requestApproval(ctx context.Context, in ToolInput, run state.Run, plan *externalPlan, connectionID, reason string) (map[string]any, error) {
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
	toolID := ""
	if in.Exec != nil {
		toolID = in.Exec.Tool.ID
	}
	if toolID != "" {
		payload["tool_id"] = toolID
	}
	if warning := approvalWarning(plan.prepared.Effect, isInternalNoriTool(in.Exec), reason); warning != "" {
		payload["warning"] = warning
	}
	if len(plan.prepared.Recipients) > 0 {
		payload["recipients"] = plan.prepared.Recipients
	}
	if len(plan.prepared.MaterialEffects) > 0 {
		payload["material_effects"] = plan.prepared.MaterialEffects
	}
	if plan.prepared.RequireExplicitApproval {
		payload["approval_once"] = true
	}
	if editable := extractEditableFields(plan.call.Args); len(editable) > 0 {
		payload["editable_fields"] = editable
	}
	unattended := isUnattendedOrigin(run)
	now := a.externalNow().UTC()
	expires := now.Add(ApprovalExpiry).UTC()
	interID := interactionID("int_", in.OperationID, plan.digest)
	draftID := ""
	if unattended {
		var prefsPtr *connections.Preferences
		if a.Executor.Connections != nil {
			if prefs, _, err := a.Executor.Connections.Preferences(ctx); err == nil {
				prefsPtr = &prefs
			}
		}
		expires = ProactiveDraftExpiry(now, prefsPtr)
		draftID = proactiveDraftID(interID)
		payload["non_blocking"] = true
		payload["draft_id"] = draftID
	}
	data, _ := json.Marshal(payload)
	revision := in.Revision
	toolTitle := in.Call.Name
	if toolTitle == "" {
		toolTitle = toolID
	}
	prompt := plan.prepared.Summary
	if title := strings.TrimSpace(plan.prepared.PromptTitle); title != "" {
		prompt = title
	}
	convID, autoID := "", ""
	if run.Origin != nil {
		convID = run.Origin.ConversationID
		autoID = run.Origin.AutomationID
	}
	interaction, err := a.Executor.Permissions.Open(ctx, permissions.Interaction{
		ID: interID, TaskID: in.RunID, InstructionRevision: &revision, ConversationID: convID, AutomationID: autoID, Kind: "approval",
		Prompt: prompt, Payload: data, ActionDigest: plan.digest, ExpiresAt: &expires, TaskTitle: state.TaskTitle(run),
		ConnectionID: connectionID, ToolID: toolID, ToolTitle: toolTitle, ActionClass: string(plan.prepared.Effect), Recipients: plan.prepared.Recipients,
	}, "")
	if err != nil {
		return nil, err
	}
	if unattended {
		draft := ProactiveDraft{
			ID:             draftID,
			InteractionID:  interaction.ID,
			SourceRunID:    in.RunID,
			Revision:       in.Revision,
			AutomationID:   autoID,
			ConversationID: convID,
			ActionDigest:   plan.digest,
			ToolName:       in.Call.Name,
			ToolID:         toolID,
			Exec:           *in.Exec,
			Args:           cloneArgsMap(plan.call.Args),
			ConnectionID:   connectionID,
			Summary:        plan.prepared.Summary,
			Prompt:         prompt,
			Effect:         string(plan.prepared.Effect),
			Recipients:     append([]string(nil), plan.prepared.Recipients...),
			Status:         DraftStatusPending,
			CreatedAt:      now,
			ExpiresAt:      expires,
		}
		if _, err := saveProactiveDraft(ctx, a.Executor.Permissions.Store, draft, now); err != nil {
			return nil, err
		}
		// Return non-blocking draft_saved outcome so the unattended automation run
		// & occurrence complete cleanly without holding MaxActiveRuns overnight.
		return denial(in, string(plan.prepared.Effect), "draft_saved", "Saved as a pending draft for approval. This automation run can finish without waiting.", map[string]any{
			"draft_saved": true, "draft_id": draftID, "interaction_id": interaction.ID, "prompt": prompt, "expires_at": expires.Format(time.RFC3339),
		}), nil
	}
	return denial(in, string(plan.prepared.Effect), "needs_interaction", "This action waits for the user's approval.", map[string]any{
		"interaction_id": interaction.ID, "interaction_kind": "approval", "prompt": prompt,
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
		Kind: "account_selection", Prompt: prompt, Payload: data, TaskTitle: state.TaskTitle(run),
	}, choose.Purpose)
	if err != nil {
		return nil, err
	}
	return denial(in, effect, "needs_interaction", choose.Error()+": "+fmt.Sprint(labels), map[string]any{
		"interaction_id": interaction.ID, "interaction_kind": "account_selection", "prompt": prompt,
	}), nil
}

func (a *Activities) signIn(ctx context.Context, in ToolInput, run state.Run, record connections.Record, capability string) (map[string]any, error) {
	effect := string(in.Exec.Policy.Effect)
	if a.Executor.Permissions == nil {
		return denial(in, effect, "failed", "The account needs to sign in again. The user can do that in Apps.", map[string]any{"recovery": "connect", "connection_id": record.ID}), nil
	}
	revision := in.Revision
	label := record.Account.Label
	if label == "" {
		label = record.Title
	}
	prompt := fmt.Sprintf("Sign in to %s again so Nori can continue this task.", label)
	var capabilities, scopes []string
	if capability != "" {
		capabilities, scopes = []string{capability}, a.Executor.Connections.ScopesFor(record.IntegrationID, capability)
		prompt = fmt.Sprintf("Give Nori more access to %s so it can continue this task.", label)
	}
	if record.Account.ProviderID == "" {
		prompt = fmt.Sprintf("Connect your %s account so Nori can continue this task.", record.Title)
	}
	returnTo := "/work/" + in.RunID
	payload := map[string]any{"integration_id": record.IntegrationID, "connection_id": record.ID, "return_to": returnTo}
	attempt, err := a.Executor.Connections.StartRepair(ctx, record.ID, interactionID("rep_", in.OperationID, record.ID+capability), returnTo, capabilities)
	switch {
	case err == nil:
		payload["auth"], payload["auth_attempt_id"], payload["authorize_url"], payload["expires_at"] = "oauth", attempt.ID, attempt.AuthorizeURL, attempt.ExpiresAt
		if len(scopes) > 0 {
			payload["requested_scopes"] = scopes
		}
	case errors.Is(err, connections.ErrInvalidAction):
		payload["auth"] = "app_password"
	default:
		return denial(in, effect, "failed", "The account needs to sign in again: "+err.Error(), map[string]any{"recovery": "repair", "connection_id": record.ID}), nil
	}
	data, _ := json.Marshal(payload)
	interaction, err := a.Executor.Permissions.Open(ctx, permissions.Interaction{
		ID: interactionID("int_", in.OperationID, "sign_in:"+record.ID), TaskID: in.RunID, InstructionRevision: &revision,
		Kind: "sign_in", Prompt: prompt, Payload: data, TaskTitle: state.TaskTitle(run),
	}, "")
	if err != nil {
		return nil, err
	}
	return denial(in, effect, "needs_interaction", "The account must sign in again before this can run.", map[string]any{
		"interaction_id": interaction.ID, "interaction_kind": "sign_in", "prompt": prompt, "connection_id": record.ID,
	}), nil
}

func (a *Activities) signInStarted(ctx context.Context, in ToolInput, run state.Run, start invoke.SignInStart) (map[string]any, error) {
	revision := in.Revision
	prompt := fmt.Sprintf("Sign in to %s so Nori can continue this task.", start.Label)
	data, _ := json.Marshal(map[string]any{"auth": "oauth", "integration_id": start.IntegrationID, "connection_id": start.ConnectionID,
		"auth_attempt_id": start.AttemptID, "authorize_url": start.AuthorizeURL, "expires_at": start.ExpiresAt, "return_to": "/work/" + in.RunID})
	interaction, err := a.Executor.Permissions.Open(ctx, permissions.Interaction{
		ID: interactionID("int_", in.OperationID, "sign_in:"+start.ConnectionID), TaskID: in.RunID, InstructionRevision: &revision,
		Kind: "sign_in", Prompt: prompt, Payload: data, TaskTitle: state.TaskTitle(run),
	}, "")
	if err != nil {
		return nil, err
	}
	return denial(in, string(in.Exec.Policy.Effect), "needs_interaction", "The server needs sign-in before this can run.", map[string]any{
		"interaction_id": interaction.ID, "interaction_kind": "sign_in", "prompt": prompt, "connection_id": start.ConnectionID,
	}), nil
}

func (a *Activities) requestTakeover(ctx context.Context, in ToolInput, run state.Run, req invoke.TakeoverRequest) (map[string]any, error) {
	revision := in.Revision
	payload := map[string]any{
		"reason":      req.Reason,
		"computer_id": req.ComputerID,
		"task_id":     in.RunID,
	}
	if req.URL != "" {
		payload["url"] = req.URL
	}
	data, _ := json.Marshal(payload)
	interaction, err := a.Executor.Permissions.Open(ctx, permissions.Interaction{
		ID: interactionID("int_", in.OperationID, "takeover:"+req.Prompt), TaskID: in.RunID, InstructionRevision: &revision,
		Kind: "computer_takeover", Prompt: req.Prompt, Payload: data, TaskTitle: state.TaskTitle(run),
	}, "")
	if err != nil {
		return nil, err
	}
	return denial(in, string(extensions.EffectControl), "needs_interaction", "Waiting for the user to take control of their computer desktop and click Done.", map[string]any{
		"interaction_id": interaction.ID, "interaction_kind": "computer_takeover", "prompt": req.Prompt,
	}), nil
}

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
		execCost, execUnknown := plan.execInspect.snapshot()
		totalCost := max(result.CostUSD, execCost) + plan.extraCost
		totalUnknown := result.CostUnknown || execUnknown || plan.extraCostUnknown
		if totalCost > 0 {
			output["cost_usd"] = totalCost
		}
		if totalUnknown {
			output["cost_unknown"] = true
		}
		if len(result.ProviderRefs) > 0 {
			output["provider_refs"] = result.ProviderRefs
		}
		outcome := result.Outcome
		if outcome == "" {
			outcome = "confirmed"
		}
		if outcome == "failed" && errors.Is(result.Err, invoke.ErrInvalidArguments) && result.Cleared == "" {
			result.Cleared = state.ClearedDidNotRun
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
				if result.Outcome == "failed" && result.Cleared == "" {
					result.Cleared = state.ClearedDidNotRun
				}
				return settle(result)
			}
		}
		if in.Exec.Policy.Retry != "safe" {
			return persist("uncertain", map[string]any{"reconciled": false}, errors.New("the worker stopped before this action's result was saved, and the provider's state could not confirm it"))
		}
	}
	if _, err := a.Store.Guard(ctx, in.RunID, in.Revision); err != nil {
		out := map[string]any{}
		addCostToOutput(out, plan.extraCost, plan.extraCostUnknown)
		if _, saveErr := persist("skipped", out, err); saveErr != nil {
			a.chargeRunSpend(ctx, in.RunID, state.Run{ID: in.RunID}, plan.extraCost, plan.extraCostUnknown)
			return nil, saveErr
		}
		a.chargeRunSpend(ctx, in.RunID, state.Run{ID: in.RunID}, plan.extraCost, plan.extraCostUnknown)
		return nil, err
	}
	// Re-check standing permission rules at execution time (not only plan time).
	if !recovering && a.Executor.Permissions != nil &&
		plan.prepared.Effect != extensions.EffectRead && plan.prepared.Effect != extensions.EffectControl {
		recheck, err := a.Executor.Permissions.RecheckStandingRules(ctx, plan.permReq, plan.authority)
		if err != nil {
			a.chargeRunSpend(ctx, in.RunID, state.Run{ID: in.RunID}, plan.extraCost, plan.extraCostUnknown)
			return nil, err
		}
		if recheck.Result != "allow" {
			out := map[string]any{"admitted": false, "rechecked": true}
			addCostToOutput(out, plan.extraCost, plan.extraCostUnknown)
			return persist("denied", out, errors.New(recheck.Reason))
		}
	}
	op.Attempt++
	if err := a.Store.PutOperation(ctx, in.RunID, *op); err != nil {
		a.chargeRunSpend(ctx, in.RunID, state.Run{ID: in.RunID}, plan.extraCost, plan.extraCostUnknown)
		return nil, err
	}
	plan.call.Attempt = op.Attempt
	timeout := time.Duration(in.Exec.Policy.TimeoutMS) * time.Millisecond
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	result := plan.adapter.Execute(callCtx, plan.call, plan.prepared)
	cancel()
	for retry := 1; retry <= 2 && in.Exec.Policy.Retry == "safe" && result.Err != nil && ctx.Err() == nil; retry++ {
		var temporary *invoke.TemporaryError
		if !errors.As(result.Err, &temporary) {
			break
		}
		wait := min(max(temporary.After, time.Duration(retry*retry)*time.Second), 20*time.Second)
		if err := a.externalSleep(ctx, wait); err != nil {
			break
		}
		if ctx.Err() != nil {
			break
		}
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		result = plan.adapter.Execute(callCtx, plan.call, plan.prepared)
		cancel()
	}
	if errors.Is(ctx.Err(), context.Canceled) && in.Exec.Policy.Effect == extensions.EffectRead && in.Exec.Policy.Retry == "safe" {
		execCost, execUnknown := plan.execInspect.snapshot()
		a.chargeRunSpend(ctx, in.RunID, state.Run{ID: in.RunID}, max(result.CostUSD, execCost)+plan.extraCost, result.CostUnknown || execUnknown || plan.extraCostUnknown)
		return nil, context.Cause(ctx)
	}
	if result.Outcome == "" {
		result.Outcome = "confirmed"
		if result.Err != nil {
			result.Outcome = "failed"
		}
	}
	if ctx.Err() != nil && result.Outcome == "failed" && in.Exec.Policy.Effect != "read" {
		result.Outcome = "uncertain"
	}
	if result.Outcome == "uncertain" {
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
