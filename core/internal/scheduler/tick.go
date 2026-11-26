package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"nori/internal/state"
)

const defaultSkipGrace = time.Minute

type skipTickArmKey struct{}

func WithoutTickArm(ctx context.Context) context.Context {
	return context.WithValue(ctx, skipTickArmKey{}, true)
}

func skipTickArm(ctx context.Context) bool {
	v, _ := ctx.Value(skipTickArmKey{}).(bool)
	return v
}

func (s *Service) Tick(ctx context.Context) (*time.Time, error) {
	now := s.now()
	enabled, err := s.automations(ctx, "enabled")
	if err != nil {
		return nil, err
	}
	var earliest *time.Time
	for _, a := range enabled {
		next, err := s.schedule(ctx, a, now)
		if err != nil && !errors.Is(err, ErrStale) && !errors.Is(err, state.ErrRevisionConflict) {
			slog.WarnContext(ctx, "scheduler automation failed", "automation_id", a.ID, "err", err)
		}
		if next != nil && (earliest == nil || next.Before(*earliest)) {
			earliest = next
		}
	}
	if err := s.drain(ctx, now); err != nil {
		return earliest, err
	}
	if earliest != nil && s.Arm != nil && !skipTickArm(ctx) {
		s.Arm(ctx, *earliest)
	}
	return earliest, nil
}

func (s *Service) schedule(ctx context.Context, a stored, now time.Time) (*time.Time, error) {
	if a.Trigger.Kind == "event_relative" {
		a = s.refreshEvent(ctx, a, now)
	}
	next := s.computeNext(ctx, a, now)
	if next == nil || next.After(now) {
		if a.NextAt == nil && next != nil || a.NextAt != nil && (next == nil || !a.NextAt.Equal(*next)) {
			s.saveNext(ctx, a, next)
		}
		return next, nil
	}
	after := now.Add(-time.Second)
	if a.Cursor != nil {
		after = *a.Cursor
	}
	var slots []time.Time
	if a.Trigger.Kind == "watch" {
		slots = []time.Time{*next}
	} else {
		runs, err := s.nextRuns(ctx, a.Trigger, after, 500, a.EventStart)
		if err != nil {
			return nil, err
		}
		for _, run := range runs {
			if run.After(now) {
				break
			}
			if a.EndAt != nil && run.After(*a.EndAt) {
				break
			}
			slots = append(slots, run)
		}
	}
	snoozed := a.SnoozedUntil != nil && !a.SnoozedUntil.After(now)
	if len(slots) == 0 && snoozed && a.Cursor != nil {
		slots = []time.Time{*a.SnoozedUntil}
	}
	if len(slots) == 0 {
		return next, nil
	}
	active, err := s.activeCount(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	window := time.Duration(a.ExecutionWindowSeconds) * time.Second
	skipWindow := window
	if a.MissedRun == "skip" && skipWindow == 0 {
		skipWindow = defaultSkipGrace
	}
	type plan struct {
		slot   time.Time
		status string
		reason string
	}
	var plans []plan
	for i, slot := range slots {
		last := i == len(slots)-1
		late := skipWindow > 0 && now.Sub(slot) > skipWindow && !snoozed
		switch {
		case a.MissedRun == "latest" && !last:
			plans = append(plans, plan{slot, "skipped", "missed_window"})
		case late:
			plans = append(plans, plan{slot, "skipped", "missed_window"})
		case a.MissedRun == "skip" && !last:
			plans = append(plans, plan{slot, "skipped", "missed_window"})
		case active >= a.MaxActiveRuns:
			plans = append(plans, plan{slot, "skipped", "max_active_runs"})
		default:
			plans = append(plans, plan{slot, "pending", ""})
			active++
		}
		if a.MaxOccurrences > 0 && a.Created+len(plans) >= a.MaxOccurrences {
			break
		}
	}
	cursor := plans[len(plans)-1].slot
	request := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("schedule|%s|%d|%s", a.ID, a.Generation, cursor.Format(time.RFC3339Nano)))).String()
	_, err = s.Store.Mutate(ctx, state.Mutation{
		RequestID: request, Method: "SCHEDULE", Target: a.ID, Payload: cursor, Collection: CollectionAutomations, ID: a.ID, SkipRevisionCheck: true,
		Apply: func(current *state.Resource, read func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			latest, err := decodeAutomation(*current)
			if err != nil {
				return state.MutationResult{}, err
			}
			if latest.Generation != a.Generation || latest.Status != "enabled" || !sameTime(latest.Cursor, a.Cursor) {
				return state.MutationResult{}, ErrStale
			}
			writes := []state.Resource{}
			for _, p := range plans {
				occurrence := s.newOccurrence(latest, p.slot, current.Revision+1, "")
				occurrence.Status, occurrence.Reason = p.status, p.reason
				if existing, err := read(CollectionOccurrences, occurrence.ID); err != nil {
					return state.MutationResult{}, err
				} else if existing != nil {
					continue
				}
				resource, err := state.NewResource(CollectionOccurrences, occurrence.ID, 1, occurrence, map[string]string{"automation_id": latest.ID, "status": occurrence.Status}, s.now())
				if err != nil {
					return state.MutationResult{}, err
				}
				writes = append(writes, resource)
				latest.LastOccurrenceID = occurrence.ID
			}
			latest.Created += len(plans)
			latest.Cursor = &cursor
			latest.SnoozedUntil = nil
			latest.NextAt = s.computeNext(ctx, latest, s.now())
			if latest.Trigger.Kind == "once" {
				latest.NextAt = nil
			}
			resource, err := state.NewResource(CollectionAutomations, latest.ID, current.Revision+1, latest, map[string]string{"status": latest.Status, "goal_id": latest.GoalID}, s.now())
			if err != nil {
				return state.MutationResult{}, err
			}
			return state.MutationResult{Writes: append([]state.Resource{resource}, writes...), Receipt: state.MutationReceipt{Status: "applied", Resource: []byte("{}")}}, nil
		},
	})
	if err != nil {
		return nil, err
	}
	updated, err := s.Store.Resource(ctx, CollectionAutomations, a.ID)
	if err != nil {
		return nil, err
	}
	latest, err := decodeAutomation(updated)
	if err != nil {
		return nil, err
	}
	return latest.NextAt, nil
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func (s *Service) saveNext(ctx context.Context, a stored, next *time.Time) {
	r, err := s.Store.Resource(ctx, CollectionAutomations, a.ID)
	if err != nil {
		return
	}
	latest, err := decodeAutomation(r)
	if err != nil || latest.Generation != a.Generation {
		return
	}
	latest.NextAt = next
	latest.EventStart, latest.EventRevision, latest.EventStatus, latest.EventCheckedAt = a.EventStart, a.EventRevision, a.EventStatus, a.EventCheckedAt
	resource, err := state.NewResource(CollectionAutomations, a.ID, 0, latest, r.Index, s.now())
	if err == nil {
		_, _ = s.Store.PutResource(ctx, resource, r.Revision)
	}
}

func (s *Service) refreshEvent(ctx context.Context, a stored, now time.Time) stored {
	if s.Events == nil || a.Trigger.Event == nil {
		return a
	}
	due := a.NextAt != nil && a.NextAt.Sub(now) < time.Hour
	if a.EventCheckedAt != nil && now.Sub(*a.EventCheckedAt) < 15*time.Minute && !due {
		return a
	}
	if a.EventCheckedAt != nil && now.Sub(*a.EventCheckedAt) < time.Minute {
		return a
	}
	start, revision, status, err := s.Events.EventStart(ctx, *a.Trigger.Event)
	checked := now
	a.EventCheckedAt = &checked
	if err != nil {
		slog.WarnContext(ctx, "scheduler automation event read failed; keeping last known time", "automation_id", a.ID, "err", err)
		return a
	}
	a.EventStart, a.EventRevision, a.EventStatus = &start, revision, status
	if status == "cancelled" {
		a.EventStart = nil
	}
	return a
}

func (s *Service) activeCount(ctx context.Context, automationID string) (int, error) {
	count := 0
	for _, status := range []string{"pending", "accepted", "running"} {
		resources, err := s.allResources(ctx, CollectionOccurrences, map[string]string{"automation_id": automationID, "status": status}, 50)
		if err != nil {
			return 0, err
		}
		count += len(resources)
	}
	return count, nil
}

func (s *Service) drain(ctx context.Context, now time.Time) error {
	for _, status := range []string{"pending", "accepted", "running"} {
		after := ""
		for {
			page, next, err := s.Store.Resources(ctx, CollectionOccurrences, map[string]string{"status": status}, after, 100)
			if err != nil {
				return err
			}
			for _, r := range page {
				o, err := decodeOccurrence(r)
				if err != nil {
					return err
				}
				if err := s.advance(ctx, r, o, now); err != nil {
					slog.WarnContext(ctx, "scheduler occurrence advance failed", "automation_id", o.AutomationID, "occurrence_id", o.ID, "err", err)
				}
			}
			if next == "" {
				break
			}
			after = next
		}
	}
	return nil
}

func taskID(occurrence string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("occurrence-task:"+occurrence)).String()
}

func (s *Service) advance(ctx context.Context, r state.Resource, o occurrenceDoc, now time.Time) error {
	automation, err := s.Store.Resource(ctx, CollectionAutomations, o.AutomationID)
	if err != nil {
		return err
	}
	a, err := decodeAutomation(automation)
	if err != nil {
		return err
	}
	switch o.Status {
	case "pending":
		if a.Generation != o.Generation || a.Status == "cancelled" {
			return s.settle(ctx, r, o, "skipped", "superseded", "", nil)
		}
		if a.Status == "paused" {
			return s.settle(ctx, r, o, "skipped", "paused", "", nil)
		}
		if window := time.Duration(a.ExecutionWindowSeconds) * time.Second; window > 0 && now.Sub(o.ScheduledAt) > window && o.Reason == "" {
			return s.settle(ctx, r, o, "expired", "missed_window", "", nil)
		}
		id := taskID(o.ID)
		run, err := s.Accept(ctx, state.Command{
			ID: id, RunID: id, Kind: state.CommandStart, Text: o.Instruction, BudgetUSD: o.BudgetUSD,
			Origin: &state.TaskOrigin{AutomationID: o.AutomationID, OccurrenceID: o.ID, GoalID: o.GoalID},
		})
		if err != nil && !errors.Is(err, state.ErrDuplicate) {
			return err
		}
		started := now
		o.Status, o.StartedAt, o.TaskIDs = "accepted", &started, []string{id}
		if run.Status == state.RunRunning {
			o.Status = "running"
		}
		return s.put(ctx, r, o)
	case "accepted", "running":
		if len(o.TaskIDs) == 0 {
			return nil
		}
		run, err := s.Tasks.GetRun(ctx, o.TaskIDs[0])
		if err != nil {
			return err
		}
		switch run.Status {
		case state.RunRunning:
			if o.Status != "running" {
				o.Status = "running"
				return s.put(ctx, r, o)
			}
			return nil
		case state.RunCompleted:
			return s.finish(ctx, r, o, a, run, "completed")
		case state.RunCancelled:
			return s.finish(ctx, r, o, a, run, "cancelled")
		default:
			return s.finish(ctx, r, o, a, run, "failed")
		}
	}
	return nil
}

func (s *Service) put(ctx context.Context, r state.Resource, o occurrenceDoc) error {
	next, err := state.NewResource(CollectionOccurrences, o.ID, 0, o, map[string]string{"automation_id": o.AutomationID, "status": o.Status}, s.now())
	if err != nil {
		return err
	}
	_, err = s.Store.PutResource(ctx, next, r.Revision)
	return err
}

func (s *Service) settle(ctx context.Context, r state.Resource, o occurrenceDoc, status, reason, summary string, failure *ErrorDetail) error {
	finished := s.now()
	o.Status, o.Reason, o.Summary, o.Error, o.FinishedAt = status, reason, summary, failure, &finished
	return s.put(ctx, r, o)
}

func (s *Service) finish(ctx context.Context, r state.Resource, o occurrenceDoc, a stored, run state.Run, status string) error {
	summary := strings.TrimSpace(run.Result)
	var failure *ErrorDetail
	if status != "completed" {
		failure = &ErrorDetail{Code: "task_" + status, Message: clip(run.Error, 1000)}
	}
	quiet := o.Monitor && status == "completed" && (strings.HasPrefix(summary, NoChange) || sameFinding(summary, a.Checkpoint))
	if !quiet {
		results := []string{}
		if s.Results != nil {
			results = s.Results(ctx, run.ID)
		}
		deliveries, err := s.deliver(ctx, a, o, run, summary, status, results)
		if err != nil {
			return err
		}
		o.DeliveryIDs = deliveries
		if o.Monitor && status == "completed" {
			s.checkpoint(ctx, a.ID, clip(summary, 2000))
		}
	}
	if err := s.settle(ctx, r, o, status, "", clip(summary, 2000), failure); err != nil {
		return err
	}
	s.recordOutcomeHealth(ctx, a.ID, status, run.Error)
	return nil
}

const maxConsecutiveAutomationFailures = 3

func isTransientAutomationFailure(errText string) bool {
	lower := strings.ToLower(errText)
	for _, marker := range []string{
		"rate or spend limit",
		"quota",
		"http 429",
		"http 503",
		"temporarily unavailable",
		"interrupted or unavailable",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func (s *Service) recordOutcomeHealth(ctx context.Context, id, status, errText string) {
	if status != "completed" && status != "failed" {
		return
	}
	if status == "failed" && isTransientAutomationFailure(errText) {
		return
	}
	r, err := s.Store.Resource(ctx, CollectionAutomations, id)
	if err != nil {
		return
	}
	a, err := decodeAutomation(r)
	if err != nil {
		return
	}
	if status == "completed" {
		if a.ConsecutiveFailures == 0 {
			return
		}
		a.ConsecutiveFailures = 0
	} else {
		a.ConsecutiveFailures++
		if a.ConsecutiveFailures >= maxConsecutiveAutomationFailures && a.Status == "enabled" {
			a.Status = "paused"
			a.NextAt = nil
			slog.WarnContext(ctx, "auto-pausing automation after consecutive failures",
				"automation_id", id, "consecutive_failures", a.ConsecutiveFailures)
		}
	}
	resource, err := state.NewResource(CollectionAutomations, id, 0, a, map[string]string{"status": a.Status, "goal_id": a.GoalID}, s.now())
	if err == nil {
		_, _ = s.Store.PutResource(ctx, resource, r.Revision)
	}
}

func sameFinding(a, b string) bool {
	norm := func(t string) string { return strings.ToLower(strings.Join(strings.Fields(t), " ")) }
	return b != "" && norm(clip(a, 2000)) == norm(b)
}

func (s *Service) checkpoint(ctx context.Context, id, summary string) {
	r, err := s.Store.Resource(ctx, CollectionAutomations, id)
	if err != nil {
		return
	}
	a, err := decodeAutomation(r)
	if err != nil {
		return
	}
	a.Checkpoint = summary
	resource, err := state.NewResource(CollectionAutomations, id, 0, a, r.Index, s.now())
	if err == nil {
		_, _ = s.Store.PutResource(ctx, resource, r.Revision)
	}
}

func clip(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit] + "…"
}
