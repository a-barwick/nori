package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"nori/internal/connections"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/permissions"
	"nori/internal/state"
)

const (
	AutomationConversationID   = "automation"
	CollectionProactiveDrafts  = "proactive_drafts"
	CollectionBriefFoldSignals = "brief_fold_signals"

	DraftStatusPending  = "pending"
	DraftStatusExecuted = "executed"
	DraftStatusDeclined = "declined"
	DraftStatusSkipped  = "skipped"
	DraftStatusExpired  = "expired"
)

var (
	ErrDraftNotApproved = errors.New("proactive draft interaction is not approved")
	ErrDraftExpired     = errors.New("proactive draft has expired")
	ErrDraftNotPending  = errors.New("proactive draft is no longer pending")
)

// ProactiveDraft stores a non-blocking draft action produced by an unattended
// automation run so the run can complete without holding a MaxActiveRuns slot.
type ProactiveDraft struct {
	ID             string          `json:"id"`
	InteractionID  string          `json:"interaction_id"`
	SourceRunID    string          `json:"source_run_id"`
	Revision       int64           `json:"revision"`
	AutomationID   string          `json:"automation_id,omitempty"`
	ConversationID string          `json:"conversation_id,omitempty"`
	ActionDigest   string          `json:"action_digest"`
	ToolName       string          `json:"tool_name"`
	ToolID         string          `json:"tool_id"`
	Exec           extensions.Exec `json:"exec"`
	Args           map[string]any  `json:"args"`
	ConnectionID   string          `json:"connection_id,omitempty"`
	Summary        string          `json:"summary"`
	Prompt         string          `json:"prompt"`
	Effect         string          `json:"effect"`
	Recipients     []string        `json:"recipients,omitempty"`
	Status         string          `json:"status"`
	ExecutionRunID string          `json:"execution_run_id,omitempty"`
	FoldReason     string          `json:"fold_reason,omitempty"`
	FoldSignalID   string          `json:"fold_signal_id,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	ExpiresAt      time.Time       `json:"expires_at"`
	ResolvedAt     *time.Time      `json:"resolved_at,omitempty"`
}

// BriefFoldSignal is a single-use signal recorded when a proactive draft
// expires or is declined/skipped. The Morning Brief folds it in once, then
// marks it consumed and drops it.
type BriefFoldSignal struct {
	ID            string     `json:"id"`
	DraftID       string     `json:"draft_id"`
	InteractionID string     `json:"interaction_id"`
	AutomationID  string     `json:"automation_id,omitempty"`
	Reason        string     `json:"reason"` // "expired", "declined", "skipped"
	Summary       string     `json:"summary"`
	Consumed      bool       `json:"consumed"`
	CreatedAt     time.Time  `json:"created_at"`
	ConsumedAt    *time.Time `json:"consumed_at,omitempty"`
}

// DigestExecutionRequest is passed to a coordinator-wired StartRun callback so
// approving a non-blocking draft spawns a short, digest-scoped execution run.
type DigestExecutionRequest struct {
	DraftID       string
	InteractionID string
	ActionDigest  string
	SourceRunID   string
	Revision      int64
	AutomationID  string
	ToolName      string
	ToolID        string
	Exec          extensions.Exec
	Args          map[string]any
	ConnectionID  string
	Summary       string
	EditedFields  map[string]string
}

// DraftExecutionResult summarizes the outcome of approving and executing a
// non-blocking proactive draft.
type DraftExecutionResult struct {
	Draft          ProactiveDraft
	InteractionID  string
	ActionDigest   string
	ExecutionRunID string
	Result         *invoke.Result
}

// NextBriefTime computes the next Morning Brief instant strictly after now in
// the user's timezone, handling calendar day transitions across DST cleanly.
func NextBriefTime(now time.Time, timezone, hhmm string) time.Time {
	loc := time.UTC
	if tz := strings.TrimSpace(timezone); tz != "" {
		if loaded, err := time.LoadLocation(tz); err == nil {
			loc = loaded
		}
	}
	hour, minute := 8, 0
	if hStr, mStr, ok := strings.Cut(strings.TrimSpace(hhmm), ":"); ok {
		if h, err := strconv.Atoi(hStr); err == nil && h >= 0 && h <= 23 {
			if m, err := strconv.Atoi(mStr); err == nil && m >= 0 && m <= 59 {
				hour, minute = h, m
			}
		}
	}
	localNow := now.In(loc)
	candidate := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), hour, minute, 0, 0, loc)
	if !candidate.After(localNow) {
		nextDay := localNow.AddDate(0, 0, 1)
		candidate = time.Date(nextDay.Year(), nextDay.Month(), nextDay.Day(), hour, minute, 0, 0, loc)
	}
	return candidate.UTC()
}

// ProactiveDraftExpiry computes the explicit TTL for a non-blocking unattended
// approval: next Morning Brief + 24 hours.
func ProactiveDraftExpiry(now time.Time, prefs *connections.Preferences) time.Time {
	zone := connections.DefaultTimezone
	briefTime := connections.DefaultMorningBriefTime
	if prefs != nil {
		if prefs.Timezone != "" {
			zone = prefs.Timezone
		}
		eff := prefs.EffectiveProactivity()
		if eff.MorningBriefTime != "" {
			briefTime = eff.MorningBriefTime
		}
	}
	return NextBriefTime(now, zone, briefTime).Add(24 * time.Hour).UTC()
}

func proactiveDraftID(interactionID string) string {
	sum := sha256.Sum256([]byte("proactive-draft:" + interactionID))
	return "drf_" + hex.EncodeToString(sum[:16])
}

func briefFoldSignalID(draftID string) string {
	sum := sha256.Sum256([]byte("brief-fold:" + draftID))
	return "fld_" + hex.EncodeToString(sum[:16])
}

func saveProactiveDraft(ctx context.Context, store state.ResourceStore, draft ProactiveDraft, now time.Time) (ProactiveDraft, error) {
	if store == nil {
		return ProactiveDraft{}, errors.New("proactive draft store is not configured")
	}
	if draft.ID == "" {
		draft.ID = proactiveDraftID(draft.InteractionID)
	}
	if draft.Status == "" {
		draft.Status = DraftStatusPending
	}
	if draft.CreatedAt.IsZero() {
		draft.CreatedAt = now.UTC()
	}
	idx := map[string]string{
		"interaction_id": draft.InteractionID,
		"automation_id":  draft.AutomationID,
		"status":         draft.Status,
	}
	res, err := state.NewResource(CollectionProactiveDrafts, draft.ID, 0, draft, idx, now.UTC())
	if err != nil {
		return ProactiveDraft{}, err
	}
	if _, err := store.PutResource(ctx, res, 0); err != nil && !errors.Is(err, state.ErrRevisionConflict) {
		return ProactiveDraft{}, err
	}
	return draft, nil
}

// ProactiveDraftService manages the lifecycle of non-blocking unattended drafts:
// lookup, digest-scoped execution on approval, expiry, and single-use brief
// fold signals on decline/skip/expiry.
type ProactiveDraftService struct {
	Store       state.ResourceStore
	Permissions *permissions.Service
	Executor    *Executor
	StartRun    func(ctx context.Context, req DigestExecutionRequest) (string, error)
	Now         func() time.Time
}

func (s *ProactiveDraftService) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *ProactiveDraftService) GetDraft(ctx context.Context, id string) (ProactiveDraft, int64, error) {
	r, err := s.Store.Resource(ctx, CollectionProactiveDrafts, id)
	if err != nil {
		return ProactiveDraft{}, 0, err
	}
	var draft ProactiveDraft
	if err := r.Decode(&draft); err != nil {
		return ProactiveDraft{}, 0, err
	}
	return draft, r.Revision, nil
}

func (s *ProactiveDraftService) GetDraftByInteraction(ctx context.Context, interactionID string) (ProactiveDraft, int64, error) {
	page, _, err := s.Store.Resources(ctx, CollectionProactiveDrafts, map[string]string{"interaction_id": interactionID}, "", 5)
	if err != nil {
		return ProactiveDraft{}, 0, err
	}
	if len(page) > 0 {
		var draft ProactiveDraft
		if err := page[0].Decode(&draft); err != nil {
			return ProactiveDraft{}, 0, err
		}
		return draft, page[0].Revision, nil
	}
	// If interactionID is an edited replacement interaction, look up draft_id from its payload.
	if s.Permissions != nil {
		if inter, err := s.Permissions.Interaction(ctx, interactionID); err == nil {
			var payload struct {
				DraftID string `json:"draft_id"`
			}
			if json.Unmarshal(inter.Payload, &payload) == nil && payload.DraftID != "" {
				return s.GetDraft(ctx, payload.DraftID)
			}
		}
	}
	return ProactiveDraft{}, 0, state.ErrNotFound
}

// ExecuteApprovedDraft verifies that the draft's interaction (or its edited
// replacement) is approved, re-checks standing rules at execution time, applies
// any allowlisted edited fields, and either spawns a short digest-scoped run
// (via StartRun) or executes the single approved action directly via Executor.
func (s *ProactiveDraftService) ExecuteApprovedDraft(ctx context.Context, interactionID string) (DraftExecutionResult, error) {
	if s == nil || s.Store == nil || s.Permissions == nil {
		return DraftExecutionResult{}, errors.New("proactive draft service is not configured")
	}
	draft, rev, err := s.GetDraftByInteraction(ctx, interactionID)
	if err != nil {
		return DraftExecutionResult{}, err
	}
	if draft.Status != DraftStatusPending {
		return DraftExecutionResult{}, ErrDraftNotPending
	}
	now := s.now()
	if now.After(draft.ExpiresAt) {
		_, _, _ = s.recordFoldAndDrop(ctx, draft, rev, DraftStatusExpired, "expired", now)
		return DraftExecutionResult{}, ErrDraftExpired
	}

	inter, err := s.Permissions.Interaction(ctx, interactionID)
	if err != nil {
		return DraftExecutionResult{}, err
	}
	// Follow SupersededBy if the user edited the draft before approving.
	if inter.Status == "superseded" && inter.SupersededBy != "" {
		inter, err = s.Permissions.Interaction(ctx, inter.SupersededBy)
		if err != nil {
			return DraftExecutionResult{}, err
		}
	}
	if inter.Status != "resolved" || inter.Resolution == nil || inter.Resolution.Kind != "approve" {
		return DraftExecutionResult{}, ErrDraftNotApproved
	}

	var editedFields map[string]string
	if repl, replErr := s.Permissions.ApprovedReplacementByInteraction(ctx, inter.ID); replErr == nil && repl != nil {
		editedFields = repl.EditedFields
	}

	args := cloneArgsMap(draft.Args)
	if len(editedFields) > 0 {
		if err := permissions.ValidateEditedFields(editedFields); err != nil {
			return DraftExecutionResult{}, err
		}
		applyAllowlistedEditsToArgs(args, editedFields)
	}

	// Re-check standing rules at execution time before running or spawning.
	recheckReq := permissions.Request{
		RunID:        draft.SourceRunID,
		Revision:     draft.Revision,
		Origin:       invoke.OriginAutomation,
		AutomationID: draft.AutomationID,
		ToolID:       draft.ToolID,
		Effect:       draft.Effect,
		ConnectionID: draft.ConnectionID,
		Recipients:   draft.Recipients,
		Digest:       inter.ActionDigest,
	}
	recheck, err := s.Permissions.RecheckStandingRules(ctx, recheckReq, permissions.Decision{
		Result:          "allow",
		Source:          "approval",
		ApprovedByHuman: true,
	})
	if err != nil {
		return DraftExecutionResult{}, err
	}
	if recheck.Result != "allow" {
		return DraftExecutionResult{}, fmt.Errorf("execution blocked by standing rule: %s", recheck.Reason)
	}

	execReq := DigestExecutionRequest{
		DraftID:       draft.ID,
		InteractionID: inter.ID,
		ActionDigest:  inter.ActionDigest,
		SourceRunID:   draft.SourceRunID,
		Revision:      draft.Revision,
		AutomationID:  draft.AutomationID,
		ToolName:      draft.ToolName,
		ToolID:        draft.ToolID,
		Exec:          draft.Exec,
		Args:          args,
		ConnectionID:  draft.ConnectionID,
		Summary:       draft.Summary,
		EditedFields:  editedFields,
	}

	var out DraftExecutionResult
	out.InteractionID = inter.ID
	out.ActionDigest = inter.ActionDigest

	if s.StartRun != nil {
		runID, err := s.StartRun(ctx, execReq)
		if err != nil {
			return DraftExecutionResult{}, err
		}
		draft.ExecutionRunID = runID
		out.ExecutionRunID = runID
	} else if s.Executor != nil {
		res, err := s.RunDigestExecution(ctx, execReq)
		if err != nil {
			return DraftExecutionResult{}, err
		}
		out.Result = &res
	}

	draft.Status = DraftStatusExecuted
	draft.ResolvedAt = &now
	updated, err := s.updateDraft(ctx, draft, rev, now)
	if err != nil {
		return DraftExecutionResult{}, err
	}
	out.Draft = updated
	return out, nil
}

// RunDigestExecution executes a single digest-scoped approved draft directly
// against its adapter after preparing with the approved args and re-checking
// permissions.
func (s *ProactiveDraftService) RunDigestExecution(ctx context.Context, req DigestExecutionRequest) (invoke.Result, error) {
	if s == nil || s.Executor == nil || s.Executor.Adapters == nil {
		return invoke.Result{}, errors.New("executor is not configured")
	}
	adapter, ok := s.Executor.Adapters.Lookup(req.Exec.Binding)
	if !ok {
		return invoke.Result{}, errors.New("tool adapter is not available")
	}
	call := invoke.Call{
		RunID:       req.SourceRunID,
		OperationID: "draft-exec-" + req.DraftID,
		Origin:      invoke.OriginUser, // human explicitly approved this exact digest
		Exec:        req.Exec,
		Args:        cloneArgsMap(req.Args),
	}
	if req.ConnectionID != "" && s.Executor.Connections != nil {
		grant, err := s.Executor.Connections.Grant(ctx, req.ConnectionID)
		if err != nil {
			return invoke.Result{}, err
		}
		call.Grant = &grant
	}
	prepared, err := adapter.Prepare(ctx, call)
	if err != nil {
		return invoke.Result{}, err
	}
	if prepared.Effect == "" {
		prepared.Effect = req.Exec.Policy.Effect
	}
	if s.Permissions != nil {
		dec, err := s.Permissions.Check(ctx, permissions.Request{
			RunID:                   req.SourceRunID,
			Revision:                req.Revision,
			Origin:                  invoke.OriginAutomation,
			AutomationID:            req.AutomationID,
			ToolID:                  req.ToolID,
			Effect:                  string(prepared.Effect),
			ConnectionID:            req.ConnectionID,
			Recipients:              prepared.Recipients,
			Digest:                  req.ActionDigest,
			RequireExplicitApproval: prepared.RequireExplicitApproval,
			OperationID:             call.OperationID,
		})
		if err != nil {
			return invoke.Result{}, err
		}
		if dec.Result != "allow" {
			return invoke.Result{}, fmt.Errorf("approved draft execution denied: %s", dec.Reason)
		}
	}
	return adapter.Execute(ctx, call, prepared), nil
}

// HandleDeclineOrSkip transitions a pending proactive draft to declined/skipped
// and records a single fold-into-brief signal, then drops the draft from pending.
func (s *ProactiveDraftService) HandleDeclineOrSkip(ctx context.Context, interactionID, reason string) (ProactiveDraft, BriefFoldSignal, error) {
	draft, rev, err := s.GetDraftByInteraction(ctx, interactionID)
	if err != nil {
		return ProactiveDraft{}, BriefFoldSignal{}, err
	}
	status := DraftStatusDeclined
	foldReason := "declined"
	if strings.EqualFold(strings.TrimSpace(reason), "skip") || strings.EqualFold(strings.TrimSpace(reason), "skipped") {
		status = DraftStatusSkipped
		foldReason = "skipped"
	}
	if draft.Status != DraftStatusPending {
		if draft.FoldSignalID != "" {
			if r, readErr := s.Store.Resource(ctx, CollectionBriefFoldSignals, draft.FoldSignalID); readErr == nil {
				var sig BriefFoldSignal
				if r.Decode(&sig) == nil {
					return draft, sig, nil
				}
			}
		}
		return draft, BriefFoldSignal{}, ErrDraftNotPending
	}
	return s.recordFoldAndDrop(ctx, draft, rev, status, foldReason, s.now())
}

// ExpirePendingDrafts sweeps pending proactive drafts whose TTL has elapsed,
// marks them expired, and records a single fold-into-brief signal for each.
func (s *ProactiveDraftService) ExpirePendingDrafts(ctx context.Context, now time.Time) ([]BriefFoldSignal, error) {
	if now.IsZero() {
		now = s.now()
	}
	now = now.UTC()
	var signals []BriefFoldSignal
	after := ""
	for {
		page, next, err := s.Store.Resources(ctx, CollectionProactiveDrafts, map[string]string{"status": DraftStatusPending}, after, 100)
		if err != nil {
			return nil, err
		}
		for _, r := range page {
			var draft ProactiveDraft
			if r.Decode(&draft) != nil {
				continue
			}
			if !now.After(draft.ExpiresAt) {
				continue
			}
			_, sig, err := s.recordFoldAndDrop(ctx, draft, r.Revision, DraftStatusExpired, "expired", now)
			if err != nil && !errors.Is(err, state.ErrRevisionConflict) {
				return signals, err
			}
			if err == nil {
				signals = append(signals, sig)
			}
		}
		if next == "" {
			return signals, nil
		}
		after = next
	}
}

func (s *ProactiveDraftService) recordFoldAndDrop(ctx context.Context, draft ProactiveDraft, rev int64, status, foldReason string, now time.Time) (ProactiveDraft, BriefFoldSignal, error) {
	sigID := briefFoldSignalID(draft.ID)
	sig := BriefFoldSignal{
		ID:            sigID,
		DraftID:       draft.ID,
		InteractionID: draft.InteractionID,
		AutomationID:  draft.AutomationID,
		Reason:        foldReason,
		Summary:       draft.Summary,
		Consumed:      false,
		CreatedAt:     now,
	}
	sigRes, err := state.NewResource(CollectionBriefFoldSignals, sigID, 0, sig, map[string]string{
		"consumed":      "false",
		"automation_id": draft.AutomationID,
	}, now)
	if err != nil {
		return ProactiveDraft{}, BriefFoldSignal{}, err
	}
	if _, err := s.Store.PutResource(ctx, sigRes, 0); err != nil && !errors.Is(err, state.ErrRevisionConflict) {
		return ProactiveDraft{}, BriefFoldSignal{}, err
	}

	draft.Status = status
	draft.FoldReason = foldReason
	draft.FoldSignalID = sigID
	draft.ResolvedAt = &now
	updated, err := s.updateDraft(ctx, draft, rev, now)
	if err != nil {
		return ProactiveDraft{}, BriefFoldSignal{}, err
	}
	return updated, sig, nil
}

func (s *ProactiveDraftService) updateDraft(ctx context.Context, draft ProactiveDraft, expectedRev int64, now time.Time) (ProactiveDraft, error) {
	idx := map[string]string{
		"interaction_id": draft.InteractionID,
		"automation_id":  draft.AutomationID,
		"status":         draft.Status,
	}
	res, err := state.NewResource(CollectionProactiveDrafts, draft.ID, 0, draft, idx, now)
	if err != nil {
		return ProactiveDraft{}, err
	}
	if _, err := s.Store.PutResource(ctx, res, expectedRev); err != nil {
		return ProactiveDraft{}, err
	}
	return draft, nil
}

// PendingFoldSignals returns all unconsumed fold-into-brief signals.
func (s *ProactiveDraftService) PendingFoldSignals(ctx context.Context) ([]BriefFoldSignal, error) {
	var out []BriefFoldSignal
	after := ""
	for {
		page, next, err := s.Store.Resources(ctx, CollectionBriefFoldSignals, map[string]string{"consumed": "false"}, after, 100)
		if err != nil {
			return nil, err
		}
		for _, r := range page {
			var sig BriefFoldSignal
			if r.Decode(&sig) == nil && !sig.Consumed {
				out = append(out, sig)
			}
		}
		if next == "" {
			return out, nil
		}
		after = next
	}
}

// MarkFoldSignalConsumed marks a fold-into-brief signal consumed so it is
// dropped after folding into a single Morning Brief.
func (s *ProactiveDraftService) MarkFoldSignalConsumed(ctx context.Context, signalID string) error {
	r, err := s.Store.Resource(ctx, CollectionBriefFoldSignals, signalID)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil
		}
		return err
	}
	var sig BriefFoldSignal
	if err := r.Decode(&sig); err != nil {
		return err
	}
	if sig.Consumed {
		return nil
	}
	now := s.now()
	sig.Consumed = true
	sig.ConsumedAt = &now
	next, err := state.NewResource(CollectionBriefFoldSignals, signalID, 0, sig, map[string]string{
		"consumed":      "true",
		"automation_id": sig.AutomationID,
	}, now)
	if err != nil {
		return err
	}
	if _, err := s.Store.PutResource(ctx, next, r.Revision); err != nil && !errors.Is(err, state.ErrRevisionConflict) {
		return err
	}
	return nil
}

func cloneArgsMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	raw, err := json.Marshal(in)
	if err != nil {
		out := make(map[string]any, len(in))
		for k, v := range in {
			out[k] = v
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func applyAllowlistedEditsToArgs(args map[string]any, edited map[string]string) {
	if subj, ok := edited["subject"]; ok {
		args["subject"] = subj
	}
	if body, ok := edited["body"]; ok {
		args["body"] = body
	}
}
