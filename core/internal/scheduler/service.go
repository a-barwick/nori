package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/permissions"
	"nori/internal/state"
)

var ErrStale = errors.New("the automation changed while it was being scheduled")

// EventSource reads a calendar event's current start for event-relative triggers. It reads one event's time and
// status only.
type EventSource interface {
	EventStart(ctx context.Context, ref ResourceRef) (start time.Time, revision, status string, err error)
}

// Service is opened per user.
type Service struct {
	Store       state.ResourceStore
	Tasks       state.RunStore
	Permissions *permissions.Service
	Events      EventSource
	// Accept submits a task command through the existing durable acceptance path.
	Accept func(ctx context.Context, cmd state.Command) (state.Run, error)
	// Results lists result IDs for a task.
	Results func(ctx context.Context, taskID string) []string
	// Timezone is the user's preference used when a trigger names none.
	Timezone func(ctx context.Context) string
	Now      func() time.Time
	// Arm asks the hosting adapter to wake this user's process at a due time. Duplicate or lost arms are harmless;
	// the next pass recomputes from durable state.
	Arm func(ctx context.Context, at time.Time)
}

func (s *Service) arm(ctx context.Context) func(state.MutationReceipt, error) (state.MutationReceipt, error) {
	return func(receipt state.MutationReceipt, err error) (state.MutationReceipt, error) {
		if err != nil || s.Arm == nil {
			return receipt, err
		}
		var a Automation
		if json.Unmarshal(receipt.Resource, &a) == nil && a.NextAt != nil {
			s.Arm(context.WithoutCancel(ctx), *a.NextAt)
		}
		return receipt, err
	}
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

func decodeAutomation(r state.Resource) (stored, error) {
	var a stored
	if err := r.Decode(&a); err != nil {
		return a, err
	}
	a.Revision = r.Revision
	created, updated := r.CreatedAt, r.UpdatedAt
	a.CreatedAt, a.UpdatedAt = &created, &updated
	if a.ConnectionIDs == nil {
		a.ConnectionIDs = []string{}
	}
	if a.DestinationIDs == nil {
		a.DestinationIDs = []string{}
	}
	if a.AuthorityRuleIDs == nil {
		a.AuthorityRuleIDs = []string{}
	}
	return a, nil
}

func (s *Service) automations(ctx context.Context, status string) ([]stored, error) {
	var out []stored
	after := ""
	for {
		page, next, err := s.Store.Resources(ctx, CollectionAutomations, map[string]string{"status": status}, after, 200)
		if err != nil {
			return nil, err
		}
		for _, r := range page {
			a, err := decodeAutomation(r)
			if err != nil {
				return nil, err
			}
			out = append(out, a)
		}
		if next == "" {
			return out, nil
		}
		after = next
	}
}

// List returns automation projections, with the latest occurrence attached.
func (s *Service) List(ctx context.Context, status string) ([]Automation, error) {
	all, err := s.automations(ctx, status)
	if err != nil {
		return nil, err
	}
	out := make([]Automation, 0, len(all))
	for _, a := range all {
		out = append(out, s.project(ctx, a))
	}
	return out, nil
}

func (s *Service) project(ctx context.Context, a stored) Automation {
	p := a.Automation
	if a.LastOccurrenceID != "" {
		if occurrence, err := s.Occurrence(ctx, a.LastOccurrenceID); err == nil {
			p.LastOccurrence = &occurrence
		}
	}
	return p
}

func (s *Service) Get(ctx context.Context, id string) (Automation, error) {
	r, err := s.Store.Resource(ctx, CollectionAutomations, id)
	if err != nil {
		return Automation{}, err
	}
	a, err := decodeAutomation(r)
	if err != nil {
		return Automation{}, err
	}
	return s.project(ctx, a), nil
}

func (s *Service) validate(ctx context.Context, value Editable) error {
	if strings.TrimSpace(value.Title) == "" || strings.TrimSpace(value.Instruction) == "" || len(value.Instruction) > state.MaxMessageBytes-1024 {
		return fmt.Errorf("%w: title and instruction are required", state.ErrInvalidResource)
	}
	switch value.MissedRun {
	case "skip", "latest", "all":
	default:
		return fmt.Errorf("%w: missed_run must be skip, latest or all", state.ErrInvalidResource)
	}
	if value.MaxActiveRuns < 1 || value.MaxActiveRuns > 10 || value.BudgetUSD < 0 || value.ExecutionWindowSeconds < 0 {
		return fmt.Errorf("%w: max_active_runs, budget and window are out of range", state.ErrInvalidResource)
	}
	t := value.Trigger
	switch t.Kind {
	case "once":
		if t.At == nil {
			return fmt.Errorf("%w: a one-time trigger needs at", state.ErrInvalidResource)
		}
	case "recurring":
		if _, err := Expand(t.LocalStart, t.Timezone, t.RRule, time.Time{}, 1); err != nil {
			return fmt.Errorf("%w: %v", state.ErrInvalidResource, err)
		}
	case "event_relative":
		if t.Event == nil || t.OffsetSeconds == nil || t.Event.ProviderID == "" {
			return fmt.Errorf("%w: an event-relative trigger needs the event and an offset", state.ErrInvalidResource)
		}
	case "watch":
		if t.Source == nil || t.PredicateRef == "" || t.MinIntervalSeconds < 60 {
			return fmt.Errorf("%w: a monitor needs a source, a condition and an interval of at least 60 seconds", state.ErrInvalidResource)
		}
	default:
		return fmt.Errorf("%w: unknown trigger", state.ErrInvalidResource)
	}
	for _, id := range value.DestinationIDs {
		if id == InAppDestination {
			continue
		}
		if _, err := s.Store.Resource(ctx, CollectionDestinations, id); err != nil {
			return fmt.Errorf("%w: unknown delivery destination %s", state.ErrInvalidResource, id)
		}
	}
	return nil
}

// proposedRules turns the requested standing scope into explicit permission rules for this automation. Nothing
// beyond what is shown here is granted (decision D2 governs the default offered for unattended writes).
func proposedRules(id string, value Editable) []permissions.RuleEditable {
	if value.RequestedAuthority == nil || len(value.RequestedAuthority.ActionClasses) == 0 {
		return nil
	}
	req := value.RequestedAuthority
	tools := req.ToolIDs
	if len(tools) == 0 {
		tools = []string{permissions.Wildcard}
	}
	connections := value.ConnectionIDs
	if len(connections) == 0 {
		connections = []string{permissions.Wildcard}
	}
	return []permissions.RuleEditable{{
		Effect: "allow", ToolIDs: tools, ConnectionIDs: connections, ResourceIDs: nonNil(req.ResourceIDs),
		ActionClasses: req.ActionClasses, Recipients: req.Recipients, AutomationID: id, GoalID: value.GoalID,
		MaxOperations: req.MaxOperations, Title: "Standing authority for " + value.Title,
	}}
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// nextRuns lists upcoming instants of a trigger after a time.
func (s *Service) nextRuns(t Trigger, after time.Time, limit int, eventStart *time.Time) ([]time.Time, error) {
	switch t.Kind {
	case "once":
		if t.At.After(after) {
			return []time.Time{t.At.UTC()}, nil
		}
		return nil, nil
	case "recurring":
		return Expand(t.LocalStart, t.Timezone, t.RRule, after, limit)
	case "event_relative":
		if eventStart == nil {
			return nil, nil
		}
		at := eventStart.Add(time.Duration(*t.OffsetSeconds) * time.Second).UTC()
		if at.After(after) {
			return []time.Time{at}, nil
		}
		return nil, nil
	case "watch":
		var out []time.Time
		next := after
		for range limit {
			next = next.Add(time.Duration(t.MinIntervalSeconds) * time.Second)
			out = append(out, next.UTC())
		}
		return out, nil
	}
	return nil, nil
}

var missedText = map[string]string{
	"skip":   "If Nori could not run at the scheduled time, that run is skipped and shown as missed.",
	"latest": "If several runs were missed, only the most recent one runs when Nori catches up; the others are shown as missed.",
	"all":    "Every missed run still runs when Nori catches up, in order.",
}

// Preview shows the next runs, the exact standing authority and warnings before saving. It is read-only.
func (s *Service) Preview(ctx context.Context, value Editable, unattendedMode string) (Preview, error) {
	if err := s.validate(ctx, value); err != nil {
		return Preview{}, err
	}
	zone := value.Trigger.Timezone
	if zone == "" && s.Timezone != nil {
		zone = s.Timezone(ctx)
	}
	if zone == "" {
		zone = "UTC"
	}
	var eventStart *time.Time
	if value.Trigger.Kind == "event_relative" && s.Events != nil {
		if start, _, _, err := s.Events.EventStart(ctx, *value.Trigger.Event); err == nil {
			eventStart = &start
		}
	}
	runs, err := s.nextRuns(value.Trigger, s.now(), 5, eventStart)
	if err != nil {
		return Preview{}, err
	}
	p := Preview{NextRuns: runs, Timezone: zone, ProposedRules: []json.RawMessage{}, MissedRunExplanation: missedText[value.MissedRun], Warnings: []Warning{}}
	if p.NextRuns == nil {
		p.NextRuns = []time.Time{}
	}
	var summaries []string
	sends := false
	for _, rule := range proposedRules("new", value) {
		rule.AutomationID = ""
		data, _ := json.Marshal(rule)
		p.ProposedRules = append(p.ProposedRules, data)
		summaries = append(summaries, permissions.Summarize(rule))
		sends = sends || slices.Contains(rule.ActionClasses, "sensitive_send")
	}
	if len(summaries) == 0 {
		p.AuthoritySummary = "This automation may read with the user's connected accounts. Any change or send will ask the user first."
	} else {
		p.AuthoritySummary = strings.Join(summaries, " ")
	}
	if !sends && unattendedMode != "require_rule" {
		p.Warnings = append(p.Warnings, Warning{Code: "unattended_send_requires_rule", Message: "Sends and changes made while the user is away will wait for approval unless this automation is given standing permission for them."})
	}
	if value.Trigger.Kind == "recurring" && len(runs) > 1 {
		loc, _ := time.LoadLocation(value.Trigger.Timezone)
		for i := 1; i < len(runs); i++ {
			_, a := runs[i-1].In(loc).Zone()
			_, b := runs[i].In(loc).Zone()
			if a != b {
				p.Warnings = append(p.Warnings, Warning{Code: "dst_shift", Message: "A daylight-saving change falls between upcoming runs; the local time stays the same."})
				break
			}
		}
	}
	if value.Trigger.Kind == "event_relative" && eventStart == nil {
		p.Warnings = append(p.Warnings, Warning{Code: "needs_connection", Message: "The event could not be read, so the reminder time is not known yet."})
	}
	return p, nil
}

func automationID(requestID string) string {
	return "auto_" + strings.ReplaceAll(uuid.NewSHA1(uuid.NameSpaceOID, []byte("automation:"+requestID)).String(), "-", "")[:20]
}

func (s *Service) ruleIDs(id string, count int) []string {
	out := make([]string, 0, count)
	for i := range count {
		out = append(out, fmt.Sprintf("rule_%s_%d", strings.TrimPrefix(id, "auto_"), i))
	}
	return out
}

// Save creates (id empty) or replaces an automation. An edit advances the generation; occurrences of the old
// generation that were not accepted are superseded, and the cursor never moves back.
func (s *Service) Save(ctx context.Context, id, requestID string, expected int64, value Editable) (state.MutationReceipt, error) {
	if err := s.validate(ctx, value); err != nil {
		return state.MutationReceipt{}, err
	}
	method, target, status := "PUT", "/v1/automations/"+id, 200
	if id == "" {
		id, method, target, status = automationID(requestID), "POST", "/v1/automations", 201
	}
	rules := proposedRules(id, value)
	now := s.now()
	return s.arm(ctx)(s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: method, Target: target, ExpectedRevision: expected, Payload: value,
		Collection: CollectionAutomations, ID: id,
		Apply: func(current *state.Resource, read func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			var a stored
			revision := int64(1)
			if current != nil {
				var err error
				if a, err = decodeAutomation(*current); err != nil {
					return state.MutationResult{}, err
				}
				if a.Status == "cancelled" {
					return state.MutationResult{}, fmt.Errorf("%w: a cancelled automation cannot be edited", state.ErrInvalidResource)
				}
				revision = current.Revision + 1
				a.Generation++
			} else {
				a.Status = "enabled"
				a.Cursor = &now
			}
			a.ID, a.Title, a.Instruction, a.Trigger = id, value.Title, value.Instruction, value.Trigger
			a.ConnectionIDs, a.BudgetUSD, a.MaxActiveRuns, a.MissedRun = nonNil(value.ConnectionIDs), value.BudgetUSD, value.MaxActiveRuns, value.MissedRun
			a.ExecutionWindowSeconds, a.DestinationIDs, a.EndAt, a.MaxOccurrences = value.ExecutionWindowSeconds, nonNil(value.DestinationIDs), value.EndAt, value.MaxOccurrences
			a.GoalID, a.SkillRef, a.SnoozedUntil = value.GoalID, value.SkillRef, nil
			if value.Trigger.Kind == "event_relative" {
				a.EventStart, a.EventCheckedAt = nil, nil
			}
			writes := []state.Resource{}
			deletes := []state.ResourceKey{}
			oldRules := a.AuthorityRuleIDs
			a.AuthorityRuleIDs = s.ruleIDs(id, len(rules))
			var summaries []string
			for i, rule := range rules {
				ruleID := a.AuthorityRuleIDs[i]
				existing, err := read(permissions.CollectionRules, ruleID)
				if err != nil {
					return state.MutationResult{}, err
				}
				ruleRevision := int64(1)
				if existing != nil {
					ruleRevision = existing.Revision + 1
				}
				resource, saved, err := s.Permissions.RuleWrites(ruleID, ruleRevision, rule)
				if err != nil {
					return state.MutationResult{}, err
				}
				writes = append(writes, resource)
				summaries = append(summaries, saved.Summary)
			}
			for _, old := range oldRules {
				if !slices.Contains(a.AuthorityRuleIDs, old) {
					deletes = append(deletes, state.ResourceKey{Collection: permissions.CollectionRules, ID: old})
				}
			}
			a.AuthoritySummary = strings.Join(summaries, " ")
			if a.AuthoritySummary == "" {
				a.AuthoritySummary = "Reads only; any change or send asks the user first."
			}
			a.NextAt = s.computeNext(a, now)
			resource, err := state.NewResource(CollectionAutomations, id, revision, a, map[string]string{"status": a.Status, "goal_id": a.GoalID}, now)
			if err != nil {
				return state.MutationResult{}, err
			}
			a.Revision = revision
			data, _ := json.Marshal(a.Automation)
			return state.MutationResult{Writes: append([]state.Resource{resource}, writes...), Deletes: deletes,
				Receipt: state.MutationReceipt{Status: "applied", Resource: data, HTTPStatus: status}}, nil
		},
	}))
}

// computeNext finds the next due instant after the cursor, honoring snooze, end and occurrence limits.
func (s *Service) computeNext(a stored, now time.Time) *time.Time {
	if a.Status != "enabled" || (a.MaxOccurrences > 0 && a.Created >= a.MaxOccurrences) {
		return nil
	}
	after := now.Add(-time.Second)
	if a.Cursor != nil {
		after = *a.Cursor
	}
	if a.Trigger.Kind == "watch" && a.Cursor != nil {
		next := a.Cursor.Add(time.Duration(a.Trigger.MinIntervalSeconds) * time.Second)
		return &next
	}
	runs, err := s.nextRuns(a.Trigger, after, 1, a.EventStart)
	if err != nil || len(runs) == 0 {
		return nil
	}
	next := runs[0]
	if a.EndAt != nil && next.After(*a.EndAt) {
		return nil
	}
	if a.SnoozedUntil != nil && a.SnoozedUntil.After(next) {
		next = *a.SnoozedUntil
	}
	return &next
}

// Act applies pause, resume, cancel, run_now or snooze.
func (s *Service) Act(ctx context.Context, id, requestID string, expected int64, action Action) (state.MutationReceipt, error) {
	now := s.now()
	return s.arm(ctx)(s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "POST", Target: "/v1/automations/" + id + "/actions", ExpectedRevision: expected, Payload: action,
		Collection: CollectionAutomations, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			a, err := decodeAutomation(*current)
			if err != nil {
				return state.MutationResult{}, err
			}
			var extra []state.Resource
			var work *state.WorkRef
			switch {
			case action.Action == "pause" && a.Status == "enabled":
				a.Status = "paused"
			case action.Action == "resume" && a.Status == "paused":
				a.Status = "enabled"
				// Slots that fell while paused are recorded as missed rather than replayed.
				a.Cursor = &now
			case action.Action == "cancel" && a.Status != "cancelled":
				a.Status = "cancelled"
			case action.Action == "snooze" && a.Status == "enabled" && action.Until != nil && action.Until.After(now):
				a.SnoozedUntil = action.Until
			case action.Action == "run_now" && a.Status != "cancelled":
				occurrence := s.newOccurrence(a, now, current.Revision+1, "run_now")
				resource, err := state.NewResource(CollectionOccurrences, occurrence.ID, 1, occurrence, map[string]string{"automation_id": a.ID, "status": occurrence.Status}, now)
				if err != nil {
					return state.MutationResult{}, err
				}
				extra = append(extra, resource)
				a.LastOccurrenceID = occurrence.ID
				// The occurrence's task ID is fixed now, so the client can open the run as soon as it starts.
				work = &state.WorkRef{TaskID: taskID(occurrence.ID)}
			default:
				return state.MutationResult{}, fmt.Errorf("%w: %s does not apply to a %s automation", state.ErrInvalidResource, action.Action, a.Status)
			}
			a.NextAt = s.computeNext(a, now)
			revision := current.Revision + 1
			resource, err := state.NewResource(CollectionAutomations, id, revision, a, map[string]string{"status": a.Status, "goal_id": a.GoalID}, now)
			if err != nil {
				return state.MutationResult{}, err
			}
			a.Revision = revision
			data, _ := json.Marshal(a.Automation)
			receipt := state.MutationReceipt{Status: "applied", Resource: data, HTTPStatus: 200}
			if work != nil {
				receipt.Status, receipt.HTTPStatus, receipt.Work = "queued", 202, work
			}
			return state.MutationResult{Writes: append([]state.Resource{resource}, extra...), Receipt: receipt}, nil
		},
	}))
}

// occurrenceID is stable for the automation, the nominal slot (or source event) and the schedule generation.
func occurrenceID(automation string, generation int64, slot time.Time, source string) string {
	key := fmt.Sprintf("%s|%d|%s|%s", automation, generation, slot.UTC().Format(time.RFC3339), source)
	return "occ_" + strings.ReplaceAll(uuid.NewSHA1(uuid.NameSpaceOID, []byte(key)).String(), "-", "")[:24]
}

func (s *Service) newOccurrence(a stored, slot time.Time, revision int64, source string) occurrenceDoc {
	instruction := fmt.Sprintf("Scheduled automation %q, run for %s.\n\n%s", a.Title, slot.UTC().Format(time.RFC3339), a.Instruction)
	monitor := a.Trigger.Kind == "watch"
	if monitor {
		instruction += fmt.Sprintf("\n\nThis is a monitor of %s %s (condition %s). Previous observation: %s\nIf nothing meaningful changed since the previous observation, answer exactly: %s",
			a.Trigger.Source.Kind, a.Trigger.Source.ProviderID, a.Trigger.PredicateRef, fallback(a.Checkpoint, "none yet"), NoChange)
	}
	if a.GoalID != "" {
		instruction += "\n\nThis serves goal " + a.GoalID + "."
	}
	return occurrenceDoc{
		Occurrence: Occurrence{
			ID: occurrenceID(a.ID, a.Generation, slot, source), AutomationID: a.ID, AutomationRevision: revision, Generation: a.Generation,
			ScheduledAt: slot.UTC(), SourceRevision: a.EventRevision, Status: "pending", TaskIDs: []string{}, DeliveryIDs: []string{},
		},
		Instruction: instruction, BudgetUSD: a.BudgetUSD, GoalID: a.GoalID, Monitor: monitor,
	}
}

// NoChange is the exact answer a monitor run gives when nothing meaningful changed; it produces no delivery.
const NoChange = "No meaningful change."

func fallback(value, other string) string {
	if strings.TrimSpace(value) == "" {
		return other
	}
	return value
}

func decodeOccurrence(r state.Resource) (occurrenceDoc, error) {
	var o occurrenceDoc
	if err := r.Decode(&o); err != nil {
		return o, err
	}
	if o.TaskIDs == nil {
		o.TaskIDs = []string{}
	}
	if o.DeliveryIDs == nil {
		o.DeliveryIDs = []string{}
	}
	return o, nil
}

func (s *Service) Occurrence(ctx context.Context, id string) (Occurrence, error) {
	r, err := s.Store.Resource(ctx, CollectionOccurrences, id)
	if err != nil {
		return Occurrence{}, err
	}
	o, err := decodeOccurrence(r)
	if err != nil {
		return Occurrence{}, err
	}
	occurrence := o.Occurrence
	for _, deliveryID := range o.DeliveryIDs {
		if d, err := s.Delivery(ctx, deliveryID); err == nil {
			occurrence.Deliveries = append(occurrence.Deliveries, d)
		}
	}
	return occurrence, nil
}

// Occurrences lists an automation's run history, newest first.
func (s *Service) Occurrences(ctx context.Context, automationID string) ([]Occurrence, error) {
	page, _, err := s.Store.Resources(ctx, CollectionOccurrences, map[string]string{"automation_id": automationID}, "", 200)
	if err != nil {
		return nil, err
	}
	out := make([]Occurrence, 0, len(page))
	for _, r := range page {
		o, err := s.Occurrence(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	slices.SortFunc(out, func(a, b Occurrence) int { return b.ScheduledAt.Compare(a.ScheduledAt) })
	return out, nil
}
