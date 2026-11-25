package inner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"nori/internal/extensions"
	"nori/internal/permissions"
	"nori/internal/state"
)

type storedAuthority struct {
	RunID        string    `json:"run_id"`
	Revision     int64     `json:"revision"`
	Source       string    `json:"source"`
	RuleIDs      []string  `json:"rule_ids,omitempty"`
	Interaction  string    `json:"interaction_id,omitempty"`
	Digest       string    `json:"digest"`
	ConnectionID string    `json:"connection_id,omitempty"`
	Epoch        int64     `json:"epoch"`
	At           time.Time `json:"at"`
}

type storedApproval struct {
	RunID       string    `json:"run_id"`
	Revision    int64     `json:"revision"`
	Digest      string    `json:"digest"`
	Decision    string    `json:"decision"`
	Interaction string    `json:"interaction_id"`
	At          time.Time `json:"at"`
}

// CheckInnerInvariants verifies all inner-loop safety and accounting invariants across
// FakeAdapter.Executed, FakeInnerModel.ModelCalls, and durable Store state:
//  1. Zero unapproved writes: every non-read call in Executed had a valid allow rule or an
//     approved interaction matching (RunID, Revision, ActionDigest) at execution time.
//  2. Zero duplicate external side effects: no (RunID, OperationID) with a write/send side
//     effect is ever executed twice.
//  3. Budget ceiling enforcement: no model call is ever issued once Spent + Others >= Budget.
//  4. Budget exhaustion semantics: budget-exhausted tasks terminate with RunFailed and
//     ExpectedIncomplete == true (unless explicitly cancelled or crashed before publishing).
func (d *Driver) CheckInnerInvariants(ctx context.Context) error {
	if d.storeFaults != nil {
		was := d.storeFaults.Enabled()
		if was {
			d.storeFaults.SetEnabled(false)
			defer d.storeFaults.SetEnabled(true)
		}
	}

	var violations []error

	// 1 & 2: Inspect every entry in FakeAdapter's append-only Executed ledger.
	executed := d.Adapter.Executed()
	seenWriteOps := make(map[providerOpKey]ExecutedCall)
	for i, call := range executed {
		if call.Effect == string(extensions.EffectRead) || call.Effect == string(extensions.EffectControl) {
			continue
		}

		// Invariant 2: Zero duplicate external side effects.
		opKey := providerOpKey{runID: call.RunID, operationID: call.OperationID}
		if prev, exists := seenWriteOps[opKey]; exists {
			violations = append(violations, fmt.Errorf(
				"duplicate external write execution for (run=%s, op=%s): first=%s (applied=%v), second=%s (applied=%v)",
				call.RunID, call.OperationID, prev.Outcome, prev.SideEffectApplied, call.Outcome, call.SideEffectApplied,
			))
		} else {
			seenWriteOps[opKey] = call
		}

		// Invariant 1: Zero unapproved writes.
		uid := call.UserID
		if uid == "" {
			var err error
			uid, err = d.userForRun(ctx, call.RunID)
			if err != nil {
				violations = append(violations, fmt.Errorf("executed[%d] (run=%s, op=%s): cannot locate owner user: %w", i, call.RunID, call.OperationID, err))
				continue
			}
		}
		rt := d.User(uid)
		if rt == nil {
			violations = append(violations, fmt.Errorf("executed[%d] (run=%s, op=%s): unknown user %q", i, call.RunID, call.OperationID, uid))
			continue
		}

		op, err := rt.Store.Operation(ctx, call.RunID, call.OperationID)
		if err != nil {
			violations = append(violations, fmt.Errorf("executed[%d] (run=%s, op=%s): missing OperationRecord: %w", i, call.RunID, call.OperationID, err))
			continue
		}
		if op.Action == nil || op.Action.Digest != call.ActionDigest {
			got := ""
			if op.Action != nil {
				got = op.Action.Digest
			}
			violations = append(violations, fmt.Errorf("executed[%d] (run=%s, op=%s): action digest mismatch: op=%q, executed=%q", i, call.RunID, call.OperationID, got, call.ActionDigest))
			continue
		}
		if op.AuthorityRef == "" {
			violations = append(violations, fmt.Errorf("executed[%d] (run=%s, op=%s): write executed without AuthorityRef", i, call.RunID, call.OperationID))
			continue
		}

		authRes, err := rt.Store.Resource(ctx, permissions.CollectionAuthority, op.AuthorityRef)
		if err != nil {
			violations = append(violations, fmt.Errorf("executed[%d] (run=%s, op=%s): missing authority record %q: %w", i, call.RunID, call.OperationID, op.AuthorityRef, err))
			continue
		}
		var auth storedAuthority
		if err := authRes.Decode(&auth); err != nil {
			violations = append(violations, fmt.Errorf("executed[%d] (run=%s, op=%s): decode authority %q: %w", i, call.RunID, call.OperationID, op.AuthorityRef, err))
			continue
		}
		if auth.RunID != call.RunID || auth.Revision != call.Revision || auth.Digest != call.ActionDigest {
			violations = append(violations, fmt.Errorf(
				"executed[%d] (run=%s, op=%s): authority binding mismatch: auth=(run=%s, rev=%d, digest=%s), exec=(run=%s, rev=%d, digest=%s)",
				i, call.RunID, call.OperationID, auth.RunID, auth.Revision, auth.Digest, call.RunID, call.Revision, call.ActionDigest,
			))
			continue
		}

		requiresExplicit := call.Effect == string(extensions.EffectUnknown) ||
			(call.Effect == string(extensions.EffectSensitiveSend) && rt.Permissions.UserSends != "allow")
		if requiresExplicit && auth.Source != "approval" && auth.Source != "permission_rule" {
			violations = append(violations, fmt.Errorf(
				"executed[%d] (run=%s, op=%s, tool=%s, effect=%s): unauthorized source %q (expected approval or permission_rule)",
				i, call.RunID, call.OperationID, call.ToolName, call.Effect, auth.Source,
			))
			continue
		}

		switch auth.Source {
		case "approval":
			apvKey := permissions.ApprovalKey(call.RunID, call.Revision, call.ActionDigest)
			apvRes, err := rt.Store.Resource(ctx, permissions.CollectionApprovals, apvKey)
			if err != nil {
				violations = append(violations, fmt.Errorf("executed[%d] (run=%s, op=%s): missing approval record %q: %w", i, call.RunID, call.OperationID, apvKey, err))
				continue
			}
			var apv storedApproval
			if err := apvRes.Decode(&apv); err != nil {
				violations = append(violations, fmt.Errorf("executed[%d] (run=%s, op=%s): decode approval %q: %w", i, call.RunID, call.OperationID, apvKey, err))
				continue
			}
			if apv.Decision != "approve" || apv.RunID != call.RunID || apv.Revision != call.Revision || apv.Digest != call.ActionDigest {
				violations = append(violations, fmt.Errorf("executed[%d] (run=%s, op=%s): invalid approval record %+v", i, call.RunID, call.OperationID, apv))
				continue
			}
			interaction, err := rt.Permissions.Interaction(ctx, apv.Interaction)
			if err != nil {
				violations = append(violations, fmt.Errorf("executed[%d] (run=%s, op=%s): missing interaction %q: %w", i, call.RunID, call.OperationID, apv.Interaction, err))
				continue
			}
			if interaction.Kind != "approval" || interaction.Status != "resolved" || interaction.Resolution == nil || interaction.Resolution.Kind != "approve" {
				violations = append(violations, fmt.Errorf("executed[%d] (run=%s, op=%s): interaction %q not approved (status=%s)", i, call.RunID, call.OperationID, interaction.ID, interaction.Status))
				continue
			}
			if interaction.TaskID != call.RunID || interaction.InstructionRevision == nil || *interaction.InstructionRevision != call.Revision || interaction.ActionDigest != call.ActionDigest {
				violations = append(violations, fmt.Errorf(
					"executed[%d] (run=%s, op=%s): interaction %q binding mismatch (task=%s, rev=%v, digest=%s)",
					i, call.RunID, call.OperationID, interaction.ID, interaction.TaskID, interaction.InstructionRevision, interaction.ActionDigest,
				))
				continue
			}

		case "permission_rule":
			if len(auth.RuleIDs) == 0 {
				violations = append(violations, fmt.Errorf("executed[%d] (run=%s, op=%s): permission_rule authority has empty RuleIDs", i, call.RunID, call.OperationID))
			} else {
				for _, ruleID := range auth.RuleIDs {
					if rule, rerr := rt.Permissions.Rule(ctx, ruleID); rerr == nil && rule.Effect != "allow" {
						violations = append(violations, fmt.Errorf(
							"executed[%d] (run=%s, op=%s): permission_rule authority cited non-allow rule %q (effect=%q)",
							i, call.RunID, call.OperationID, ruleID, rule.Effect,
						))
					}
				}
			}
		}
	}

	// 3: Inspect every entry in FakeInnerModel's ModelCalls ledger.
	modelCalls := d.Model.ModelCalls()
	for i, mc := range modelCalls {
		if mc.Budget > 0 && mc.SpentBefore >= mc.Budget {
			violations = append(violations, fmt.Errorf(
				"model_calls[%d] (run=%s, rev=%d): model call issued at or above budget ceiling (spent_before=%.4f >= budget=%.4f)",
				i, mc.RunID, mc.Revision, mc.SpentBefore, mc.Budget,
			))
		}
	}

	// 4: Verify budget-exhausted tasks terminated with RunFailed and ExpectedIncomplete == true.
	pubsByRun := make(map[string][]PublicationRecord)
	for _, p := range d.Publications() {
		pubsByRun[p.RunID] = append(pubsByRun[p.RunID], p)
	}
	for _, runID := range d.sortedRunIDs() {
		d.mu.Lock()
		budgetExhausted := d.budgetExhausted[runID]
		userID := d.runUsers[runID]
		d.mu.Unlock()

		if !budgetExhausted {
			continue
		}
		rt := d.User(userID)
		if rt == nil {
			continue
		}
		run, err := rt.Store.GetRun(ctx, runID)
		if err != nil {
			violations = append(violations, fmt.Errorf("budget-exhausted run %s: failed to read run: %w", runID, err))
			continue
		}
		if run.Status == state.RunRunning || run.CancelRequested {
			continue
		}
		if run.Status != state.RunFailed {
			violations = append(violations, fmt.Errorf("budget-exhausted run %s: terminated with status %q, want %q", runID, run.Status, state.RunFailed))
			continue
		}
		matched := false
		for _, pub := range pubsByRun[runID] {
			if pub.Status == state.RunFailed && pub.ExpectedIncomplete && !pub.BypassRouting {
				matched = true
				break
			}
		}
		if !matched {
			violations = append(violations, fmt.Errorf("budget-exhausted run %s: missing committed RunFailed publication with ExpectedIncomplete=true and BypassRouting=false (pubs=%+v)", runID, pubsByRun[runID]))
		}
	}

	return errors.Join(violations...)
}
