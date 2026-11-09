package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/state"
)

// Tick turns due slots into occurrences, submits pending occurrences as task commands, settles finished ones and
// delivers their results. It is safe to run concurrently and repeatedly: every step is keyed by a stable identity
// and guarded by the automation's generation and cursor, so duplicate wakes reuse state and lost wakes are repaired
// on the next pass. It returns the earliest future due time, for the hosting wake adapter.
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
			log.Printf("scheduler: automation %s: %v", a.ID, err)
		}
		if next != nil && (earliest == nil || next.Before(*earliest)) {
			earliest = next
		}
	}
	if err := s.drain(ctx, now); err != nil {
		return earliest, err
	}
	if due, err := s.sendDue(ctx, now); err != nil {
		log.Printf("scheduler: deliveries: %v", err)
	} else if due != nil && (earliest == nil || due.Before(*earliest)) {
		earliest = due
	}
	if earliest != nil && s.Arm != nil {
		s.Arm(ctx, *earliest)
	}
	return earliest, nil
}

// schedule creates the occurrences due for one automation according to its missed-run policy and window.
func (s *Service) schedule(ctx context.Context, a stored, now time.Time) (*time.Time, error) {
	if a.Trigger.Kind == "event_relative" {
		a = s.refreshEvent(ctx, a, now)
	}
	next := s.computeNext(a, now)
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
		runs, err := s.nextRuns(a.Trigger, after, 500, a.EventStart)
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
	type plan struct {
		slot   time.Time
		status string
		reason string
	}
	var plans []plan
	for i, slot := range slots {
		last := i == len(slots)-1
		late := window > 0 && now.Sub(slot) > window && !snoozed
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
			// Another scheduler or an edit moved first: this pass's plan is obsolete.
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
			if latest.Trigger.Kind == "once" || latest.Trigger.Kind == "event_relative" && latest.Trigger.Kind != "" && latest.MaxOccurrences == 1 {
				latest.NextAt = nil
			}
			latest.NextAt = s.computeNext(latest, s.now())
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

// refreshEvent re-reads an event-relative trigger's source when it is stale or due soon. A moved event moves the
// reminder; a cancelled event stops it. A failed read keeps the last known time and is not treated as certain.
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
		log.Printf("scheduler: automation %s event read failed; keeping the last known time: %v", a.ID, err)
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
		page, _, err := s.Store.Resources(ctx, CollectionOccurrences, map[string]string{"automation_id": automationID, "status": status}, "", 50)
		if err != nil {
			return 0, err
		}
		count += len(page)
	}
	return count, nil
}

// drain submits pending occurrences and settles accepted or running ones.
func (s *Service) drain(ctx context.Context, now time.Time) error {
	for _, status := range []string{"pending", "accepted", "running"} {
		page, _, err := s.Store.Resources(ctx, CollectionOccurrences, map[string]string{"status": status}, "", 100)
		if err != nil {
			return err
		}
		for _, r := range page {
			o, err := decodeOccurrence(r)
			if err != nil {
				return err
			}
			if err := s.advance(ctx, r, o, now); err != nil {
				log.Printf("scheduler: occurrence %s: %v", o.ID, err)
			}
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
		// An edit or cancel fences unaccepted occurrences of the old generation.
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

// finish records the occurrence's outcome and delivers it. A monitor that found nothing meaningful delivers nothing
// and keeps its previous checkpoint.
func (s *Service) finish(ctx context.Context, r state.Resource, o occurrenceDoc, a stored, run state.Run, status string) error {
	summary := strings.TrimSpace(run.Result)
	var failure *ErrorDetail
	if status != "completed" {
		failure = &ErrorDetail{Code: "task_" + status, Message: clip(run.Error, 1000)}
	}
	// A monitor stays quiet when it found nothing, or when it found exactly what it already reported last time.
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
	return s.settle(ctx, r, o, status, "", clip(summary, 2000), failure)
}

// sameFinding compares two monitor findings ignoring case and spacing.
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
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
