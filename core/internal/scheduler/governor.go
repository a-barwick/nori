package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/connections"
	"nori/internal/permissions"
	"nori/internal/state"
)

const (
	// DefaultEstimatedRunCostUSD is the conservative per-run cost estimate used
	// when an automation does not specify a positive BudgetUSD.
	DefaultEstimatedRunCostUSD = 0.05

	// StatusNeedsConfirmation is returned on MutationReceipt.Status when a
	// proactive automation exceeds the user's daily proactive budget cap and
	// requires an explicit user Interaction approval before proceeding.
	StatusNeedsConfirmation = "needs_confirmation"

	// BudgetApprovalToolID identifies proactive daily spend confirmation
	// interactions in ApprovalPayload.tool_id. It intentionally does not use the
	// nori/assistant/automation prefix so clients render it as a budget approval
	// card with a warning callout rather than a routine-creation card.
	BudgetApprovalToolID = "nori/assistant/proactive-budget"

	// BudgetConfirmationExpiry matches standard approval interaction expiry.
	BudgetConfirmationExpiry = 30 * time.Minute
)

// BudgetInteractionContext attaches task metadata when an automation mutation
// is invoked from an interactive task tool call, so any opened budget approval
// interaction wakes the waiting task when the user responds.
type BudgetInteractionContext struct {
	TaskID              string
	InstructionRevision *int64
	ConversationID      string
	TaskTitle           string
}

type budgetInteractionCtxKey struct{}

// WithBudgetInteractionContext attaches task metadata to ctx for budget
// confirmation interactions opened during Save or Act.
func WithBudgetInteractionContext(ctx context.Context, meta BudgetInteractionContext) context.Context {
	return context.WithValue(ctx, budgetInteractionCtxKey{}, meta)
}

func budgetInteractionContext(ctx context.Context) BudgetInteractionContext {
	if meta, ok := ctx.Value(budgetInteractionCtxKey{}).(BudgetInteractionContext); ok {
		return meta
	}
	return BudgetInteractionContext{}
}

// EstimateDailyRuns estimates how many times a trigger will fire in a 24-hour period.
func EstimateDailyRuns(t Trigger, zone string, now time.Time) float64 {
	switch t.Kind {
	case "once", "event_relative":
		return 1
	case "watch":
		interval := t.MinIntervalSeconds
		if interval < 60 {
			interval = 60
		}
		return 86400.0 / float64(interval)
	case "recurring":
		if strings.TrimSpace(zone) == "" {
			zone = "UTC"
		}
		runs, err := Expand(t.LocalStart, zone, t.RRule, now.Add(-time.Second), 500)
		if err != nil || len(runs) == 0 {
			return 1
		}
		horizon := runs[0].Add(7 * 24 * time.Hour)
		count := 0
		for _, r := range runs {
			if r.Before(horizon) {
				count++
			} else {
				break
			}
		}
		if count == 0 {
			return 1
		}
		return float64(count) / 7.0
	default:
		return 1
	}
}

// EstimateDailyCostUSD returns the projected daily USD cost of a single automation.
func EstimateDailyCostUSD(t Trigger, budgetUSD float64, zone string, now time.Time) float64 {
	perRun := budgetUSD
	if perRun <= 0 {
		perRun = DefaultEstimatedRunCostUSD
	}
	runs := EstimateDailyRuns(t, zone, now)
	return math.Round(runs*perRun*10000) / 10000
}

func (s *Service) dailyProactiveCapUSD(ctx context.Context) float64 {
	if prefs, ok := s.loadPreferences(ctx); ok {
		return prefs.EffectiveProactivity().DailyProactiveBudgetUSD
	}
	return connections.DefaultDailyProactiveBudgetUSD
}

func (s *Service) localMidnight(ctx context.Context, now time.Time) time.Time {
	loc, err := time.LoadLocation(s.userTimezone(ctx))
	if err != nil || loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)
	midnight, _ := LocalTime(local.Year(), local.Month(), local.Day(), 0, 0, 0, loc)
	return midnight.UTC()
}

// ActualDailyProactiveSpend computes today's actual proactive spend (task LLM +
// surfacing outer-loop turns + Computer wake cost) since local midnight.
func (s *Service) ActualDailyProactiveSpend(ctx context.Context, now time.Time) (ProactiveSpend, error) {
	since := s.localMidnight(ctx, now)
	if s.DailyProactiveSpend != nil {
		return s.DailyProactiveSpend(ctx, since)
	}
	var spend ProactiveSpend
	if s.Tasks == nil {
		return spend, nil
	}
	runs, err := s.Tasks.RecentRuns(ctx, 200)
	if err != nil {
		return spend, err
	}
	for _, run := range runs {
		if run.StartedAt.Before(since) {
			continue
		}
		if run.Origin == nil || (run.Origin.ConversationID != AutomationConversationID && run.Origin.AutomationID == "") {
			continue
		}
		spend.TaskLLMUSD += run.Spent + run.SubtaskSpent
	}
	return spend, nil
}

// CheckRuntimeBudget reports whether today's actual proactive spend has reached
// or exceeded the user's daily proactive budget cap.
func (s *Service) CheckRuntimeBudget(ctx context.Context, now time.Time) (exceeded bool, spend ProactiveSpend, capUSD float64, err error) {
	capUSD = s.dailyProactiveCapUSD(ctx)
	if capUSD <= 0 {
		return false, ProactiveSpend{}, capUSD, nil
	}
	spend, err = s.ActualDailyProactiveSpend(ctx, now)
	if err != nil {
		return false, spend, capUSD, err
	}
	return spend.Total() >= capUSD, spend, capUSD, nil
}

func budgetActionDigest(actionKind, automationID, title string, t Trigger, budgetUSD float64) string {
	digestAutoID := automationID
	if actionKind == "create" {
		// Create retries from a resumed task have a new requestID/automationID;
		// bind the approval digest to the action kind + automation parameters.
		digestAutoID = ""
	}
	payload, _ := json.Marshal(struct {
		Action       string  `json:"action"`
		AutomationID string  `json:"automation_id,omitempty"`
		Title        string  `json:"title"`
		Trigger      Trigger `json:"trigger"`
		BudgetUSD    float64 `json:"budget_usd"`
	}{
		Action:       actionKind,
		AutomationID: digestAutoID,
		Title:        title,
		Trigger:      t,
		BudgetUSD:    budgetUSD,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func budgetConfirmationInteractionID(actionDigest string) string {
	return "int_budget_" + strings.ReplaceAll(uuid.NewSHA1(uuid.NameSpaceOID, []byte("budget-confirm:"+actionDigest)).String(), "-", "")[:20]
}

func budgetConfirmationRetryInteractionID(actionDigest, requestID string) string {
	return "int_budget_" + strings.ReplaceAll(uuid.NewSHA1(uuid.NameSpaceOID, []byte("budget-confirm:"+actionDigest+":"+requestID)).String(), "-", "")[:20]
}

func localCalendarDay(t time.Time, zone string) string {
	loc, err := time.LoadLocation(strings.TrimSpace(zone))
	if err != nil || loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02")
}

// projectedEnabledDailyCostUSD sums projected daily cost across active enabled
// automations, excluding skipID (used when editing an existing automation),
// exhausted/already-fired automations, and unrelated one-shot reminders.
func (s *Service) projectedEnabledDailyCostUSD(ctx context.Context, skipID string, candidate Trigger, now time.Time) (float64, error) {
	enabled, err := s.automations(ctx, string(AutomationEnabled))
	if err != nil {
		return 0, err
	}
	candidateDay := ""
	if candidate.Kind == "once" && candidate.At != nil {
		candidateDay = localCalendarDay(*candidate.At, s.resolveTriggerTimezone(ctx, candidate))
	}
	total := 0.0
	for _, a := range enabled {
		if a.ID == skipID {
			continue
		}
		if a.MaxOccurrences > 0 && a.Created >= a.MaxOccurrences {
			continue
		}
		if a.EndAt != nil && !a.EndAt.After(now) {
			continue
		}
		zone := s.resolveTriggerTimezone(ctx, a.Trigger)
		switch a.Trigger.Kind {
		case "once":
			// Completed or past one-time reminders remain status=enabled with NextAt=nil;
			// they will never run again and must not count toward daily proactive spend.
			if a.Created > 0 || a.Trigger.At == nil || !a.Trigger.At.After(now) || (a.NextAt == nil && a.LastOccurrenceID != "") {
				continue
			}
			// Future one-time reminders only stack with another one-time reminder on the
			// same local calendar day, not as permanent daily recurring spend.
			if candidate.Kind != "once" || candidateDay == "" || localCalendarDay(*a.Trigger.At, zone) != candidateDay {
				continue
			}
		case "event_relative":
			if a.Created > 0 {
				continue
			}
			if a.EventStart != nil && a.Trigger.OffsetSeconds != nil {
				at := a.EventStart.Add(time.Duration(*a.Trigger.OffsetSeconds) * time.Second)
				if !at.After(now) {
					continue
				}
			}
			if candidate.Kind != "event_relative" {
				continue
			}
		}
		total += EstimateDailyCostUSD(a.Trigger, a.BudgetUSD, zone, now)
	}
	return total, nil
}

// enforceBudgetGovernor checks whether adding/updating/resuming an automation
// would exceed the daily proactive budget cap. If so, it verifies whether a
// real user-approved permissions.Interaction already exists for the exact
// action digest; otherwise it opens a new approval interaction and returns a
// needs_confirmation MutationReceipt.
func (s *Service) enforceBudgetGovernor(
	ctx context.Context,
	actionKind string,
	automationID string,
	requestID string,
	title string,
	trigger Trigger,
	budgetUSD float64,
	confirmationInteractionID string,
) (*state.MutationReceipt, error) {
	capUSD := s.dailyProactiveCapUSD(ctx)
	if capUSD <= 0 {
		return nil, nil
	}
	now := s.now()
	existingCost, err := s.projectedEnabledDailyCostUSD(ctx, automationID, trigger, now)
	if err != nil {
		return nil, err
	}
	zone := s.resolveTriggerTimezone(ctx, trigger)
	candidateCost := EstimateDailyCostUSD(trigger, budgetUSD, zone, now)
	projectedTotal := math.Round((existingCost+candidateCost)*10000) / 10000
	if projectedTotal <= capUSD {
		return nil, nil
	}

	digest := budgetActionDigest(actionKind, automationID, title, trigger, budgetUSD)
	interactionID := strings.TrimSpace(confirmationInteractionID)
	if interactionID == "" {
		interactionID = budgetConfirmationInteractionID(digest)
	}

	// Verify real user approval through permissions.Service (never model self-confirmation).
	needFreshID := false
	if s.Permissions != nil {
		existing, err := s.Permissions.Interaction(ctx, interactionID)
		if err == nil {
			if existing.Status == "resolved" && existing.Resolution != nil &&
				existing.Resolution.Kind == "approve" && existing.ActionDigest == digest {
				return nil, nil
			}
			if existing.Status != "pending" && strings.TrimSpace(confirmationInteractionID) == "" {
				// Prior interaction for this digest expired or was declined; open a fresh one.
				needFreshID = true
			}
		} else if !errors.Is(err, state.ErrNotFound) {
			return nil, err
		}
	}
	if needFreshID {
		interactionID = budgetConfirmationRetryInteractionID(digest, requestID)
		if s.Permissions != nil {
			if existing, err := s.Permissions.Interaction(ctx, interactionID); err == nil &&
				existing.Status == "resolved" && existing.Resolution != nil &&
				existing.Resolution.Kind == "approve" && existing.ActionDigest == digest {
				return nil, nil
			}
		}
	}

	prompt := fmt.Sprintf(
		"%q is projected to bring daily proactive spend to $%.2f/day, above your $%.2f/day limit. Approve to continue?",
		title, projectedTotal, capUSD,
	)
	if s.Permissions != nil {
		summary := fmt.Sprintf("Allow %q to run proactively.", title)
		warning := fmt.Sprintf(
			"Projected daily proactive spend is $%.2f/day, above your $%.2f/day limit.",
			projectedTotal, capUSD,
		)
		payload, _ := json.Marshal(map[string]any{
			"summary":             summary,
			"tool_id":             BudgetApprovalToolID,
			"warning":             warning,
			"effect":              "reversible_write",
			"account":             map[string]any{"connection_id": "", "label": ""},
			"resources":           []any{},
			"approval_once":       true,
			"projected_daily_usd": projectedTotal,
			"daily_cap_usd":       capUSD,
			"reason":              "budget_exceeded",
			"automation_title":    title,
		})
		meta := budgetInteractionContext(ctx)
		expires := now.Add(BudgetConfirmationExpiry).UTC()
		if _, err := s.Permissions.Open(ctx, permissions.Interaction{
			ID:                  interactionID,
			TaskID:              meta.TaskID,
			InstructionRevision: meta.InstructionRevision,
			ConversationID:      meta.ConversationID,
			AutomationID:        automationID,
			Kind:                "approval",
			Prompt:              "Approve proactive spend?",
			Payload:             payload,
			ActionDigest:        digest,
			ExpiresAt:           &expires,
			TaskTitle:           meta.TaskTitle,
			ToolID:              BudgetApprovalToolID,
			ToolTitle:           "Proactive spend limit",
			ActionClass:         "reversible_write",
		}, "proactive_budget_confirmation"); err != nil {
			return nil, err
		}
	}

	conf := BudgetConfirmation{
		Status:            StatusNeedsConfirmation,
		InteractionID:     interactionID,
		ActionDigest:      digest,
		ProjectedDailyUSD: projectedTotal,
		DailyCapUSD:       capUSD,
		Reason:            "budget_exceeded",
		Prompt:            prompt,
	}
	raw, _ := json.Marshal(conf)
	return &state.MutationReceipt{
		RequestID:  requestID,
		Accepted:   false,
		Status:     StatusNeedsConfirmation,
		Resource:   raw,
		HTTPStatus: 202,
	}, nil
}
