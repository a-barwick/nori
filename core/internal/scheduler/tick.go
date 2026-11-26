package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"nori/internal/homeai"
	"nori/internal/httpretry"
	"nori/internal/llmguard"
	"nori/internal/modelkeys"
	"nori/internal/state"
	"nori/internal/usage"
)

const (
	defaultSkipGrace                 = time.Minute
	maxAutomationUpdateRetries       = 4
	maxConsecutiveAutomationFailures = 3
)

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
	enabled, err := s.automations(ctx, string(AutomationEnabled))
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
			if err := s.saveNext(ctx, a, next); err != nil && !errors.Is(err, state.ErrNotFound) {
				slog.WarnContext(ctx, "scheduler saveNext failed", "automation_id", a.ID, "err", err)
			}
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
		status OccurrenceStatus
		reason string
	}
	var plans []plan
	for i, slot := range slots {
		last := i == len(slots)-1
		late := skipWindow > 0 && now.Sub(slot) > skipWindow && !snoozed
		switch {
		case a.MissedRun == "latest" && !last:
			plans = append(plans, plan{slot, OccurrenceSkipped, "missed_window"})
		case late:
			plans = append(plans, plan{slot, OccurrenceSkipped, "missed_window"})
		case a.MissedRun == "skip" && !last:
			plans = append(plans, plan{slot, OccurrenceSkipped, "missed_window"})
		case active >= a.MaxActiveRuns:
			plans = append(plans, plan{slot, OccurrenceSkipped, "max_active_runs"})
		default:
			plans = append(plans, plan{slot, OccurrencePending, ""})
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
			if latest.Generation != a.Generation || latest.Status != AutomationEnabled || !sameTime(latest.Cursor, a.Cursor) {
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
				resource, err := state.NewResource(CollectionOccurrences, occurrence.ID, 1, occurrence, map[string]string{"automation_id": latest.ID, "status": string(occurrence.Status)}, s.now())
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
			resource, err := state.NewResource(CollectionAutomations, latest.ID, current.Revision+1, latest, map[string]string{"status": string(latest.Status), "goal_id": latest.GoalID}, s.now())
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

func (s *Service) updateAutomationWithRetry(ctx context.Context, id string, update func(*stored, state.Resource) (bool, map[string]string, error)) error {
	for attempt := 0; attempt < maxAutomationUpdateRetries; attempt++ {
		r, err := s.Store.Resource(ctx, CollectionAutomations, id)
		if err != nil {
			return err
		}
		latest, err := decodeAutomation(r)
		if err != nil {
			return err
		}
		changed, index, err := update(&latest, r)
		if err != nil || !changed {
			return err
		}
		if index == nil {
			index = r.Index
		}
		resource, err := state.NewResource(CollectionAutomations, id, 0, latest, index, s.now())
		if err != nil {
			return err
		}
		_, err = s.Store.PutResource(ctx, resource, r.Revision)
		if err == nil {
			return nil
		}
		if !errors.Is(err, state.ErrRevisionConflict) {
			return err
		}
	}
	return state.ErrRevisionConflict
}

func (s *Service) saveNext(ctx context.Context, a stored, next *time.Time) error {
	return s.updateAutomationWithRetry(ctx, a.ID, func(latest *stored, r state.Resource) (bool, map[string]string, error) {
		if latest.Generation != a.Generation {
			return false, nil, nil
		}
		latest.NextAt = next
		latest.EventStart, latest.EventRevision, latest.EventStatus, latest.EventCheckedAt = a.EventStart, a.EventRevision, a.EventStatus, a.EventCheckedAt
		return true, r.Index, nil
	})
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
	for _, status := range []OccurrenceStatus{OccurrencePending, OccurrenceAccepted, OccurrenceRunning} {
		resources, err := s.allResources(ctx, CollectionOccurrences, map[string]string{"automation_id": automationID, "status": string(status)}, 50)
		if err != nil {
			return 0, err
		}
		count += len(resources)
	}
	return count, nil
}

func (s *Service) drain(ctx context.Context, now time.Time) error {
	for _, status := range []OccurrenceStatus{OccurrencePending, OccurrenceAccepted, OccurrenceRunning} {
		after := ""
		for {
			page, next, err := s.Store.Resources(ctx, CollectionOccurrences, map[string]string{"status": string(status)}, after, 100)
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
	case OccurrencePending:
		if a.Generation != o.Generation || a.Status == AutomationCancelled {
			return s.settle(ctx, r, o, OccurrenceSkipped, "superseded", "", nil)
		}
		if a.Status == AutomationPaused {
			return s.settle(ctx, r, o, OccurrenceSkipped, "paused", "", nil)
		}
		if window := time.Duration(a.ExecutionWindowSeconds) * time.Second; window > 0 && now.Sub(o.ScheduledAt) > window && o.Reason == "" {
			return s.settle(ctx, r, o, OccurrenceExpired, "missed_window", "", nil)
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
		o.Status, o.StartedAt, o.TaskIDs = OccurrenceAccepted, &started, []string{id}
		if run.Status == state.RunRunning {
			o.Status = OccurrenceRunning
		}
		return s.put(ctx, r, o)
	case OccurrenceAccepted, OccurrenceRunning:
		if len(o.TaskIDs) == 0 {
			return nil
		}
		run, err := s.Tasks.GetRun(ctx, o.TaskIDs[0])
		if err != nil {
			return err
		}
		switch run.Status {
		case state.RunRunning:
			if o.Status != OccurrenceRunning {
				o.Status = OccurrenceRunning
				return s.put(ctx, r, o)
			}
			return nil
		case state.RunCompleted:
			return s.finish(ctx, r, o, a, run, OccurrenceCompleted)
		case state.RunCancelled:
			return s.finish(ctx, r, o, a, run, OccurrenceCancelled)
		default:
			return s.finish(ctx, r, o, a, run, OccurrenceFailed)
		}
	}
	return nil
}

func (s *Service) put(ctx context.Context, r state.Resource, o occurrenceDoc) error {
	next, err := state.NewResource(CollectionOccurrences, o.ID, 0, o, map[string]string{"automation_id": o.AutomationID, "status": string(o.Status)}, s.now())
	if err != nil {
		return err
	}
	_, err = s.Store.PutResource(ctx, next, r.Revision)
	return err
}

func (s *Service) settle(ctx context.Context, r state.Resource, o occurrenceDoc, status OccurrenceStatus, reason, summary string, failure *ErrorDetail) error {
	finished := s.now()
	o.Status, o.Reason, o.Summary, o.Error, o.FinishedAt = status, reason, summary, failure, &finished
	return s.put(ctx, r, o)
}

func (s *Service) finish(ctx context.Context, r state.Resource, o occurrenceDoc, a stored, run state.Run, status OccurrenceStatus) error {
	summary := strings.TrimSpace(run.Result)
	var failure *ErrorDetail
	if status != OccurrenceCompleted {
		code := "task_" + string(status)
		if status == OccurrenceFailed && modelkeys.IsAccessFailure(run.Error) {
			code = modelkeys.AccessFailureCode
		}
		failure = &ErrorDetail{Code: code, Message: clip(run.Error, 1000)}
	}
	quiet := o.Monitor && status == OccurrenceCompleted && (strings.HasPrefix(summary, NoChange) || sameFinding(summary, a.Checkpoint))
	if !quiet {
		results := []string{}
		if s.Results != nil {
			results = s.Results(ctx, run.ID)
		}
		// Delivery uses deterministic IDs and tolerates duplicate writes, so running it
		// before settle ensures DeliveryIDs are persisted atomically on the settled
		// occurrence without advancing the monitor checkpoint before settlement succeeds.
		deliveries, err := s.deliver(ctx, a, o, run, summary, status, results)
		if err != nil {
			return err
		}
		o.DeliveryIDs = deliveries
	}
	if err := s.settle(ctx, r, o, status, "", clip(summary, 2000), failure); err != nil {
		return err
	}
	if !quiet && o.Monitor && status == OccurrenceCompleted {
		if err := s.checkpoint(ctx, a.ID, clip(summary, 2000)); err != nil && !errors.Is(err, state.ErrNotFound) {
			slog.WarnContext(ctx, "scheduler monitor checkpoint update failed", "automation_id", a.ID, "occurrence_id", o.ID, "err", err)
		}
	}
	if err := s.recordOutcomeHealth(ctx, a.ID, status, run.Error); err != nil && !errors.Is(err, state.ErrNotFound) {
		slog.WarnContext(ctx, "scheduler outcome health update failed", "automation_id", a.ID, "occurrence_id", o.ID, "err", err)
	}
	return nil
}

// extractHTTPStatus parses a 3-digit HTTP status code following "HTTP " (case-insensitive).
func extractHTTPStatus(errText string) (int, bool) {
	upper := strings.ToUpper(errText)
	search := upper
	for {
		idx := strings.Index(search, "HTTP ")
		if idx < 0 {
			return 0, false
		}
		rest := search[idx+5:]
		if len(rest) >= 3 {
			if code, err := strconv.Atoi(rest[:3]); err == nil && code >= 100 && code <= 599 {
				if len(rest) == 3 || rest[3] < '0' || rest[3] > '9' {
					return code, true
				}
			}
		}
		search = rest
	}
}

var transientSentinelMessages = []string{
	usage.ErrUserQuotaExceeded.Error(),
	llmguard.ErrRateLimited.Error(),
	llmguard.ErrConcurrencyLimited.Error(),
	llmguard.ErrCircuitOpen.Error(),
	llmguard.ErrBudgetExceeded.Error(),
	llmguard.ErrCallLimitExceeded.Error(),
	context.DeadlineExceeded.Error(),
	homeai.ErrOffline.Error(),
	homeai.ErrBusy.Error(),
	homeai.ErrInterrupted.Error(),
	homeai.ErrOllamaUnavailable.Error(),
	"task model request interrupted or unavailable",
	"Google model request interrupted or unavailable",
	"inspection request interrupted or unavailable",
	"model request interrupted or unavailable",
}

func isTransientAutomationFailure(errText string) bool {
	if code, ok := extractHTTPStatus(errText); ok && httpretry.IsRetryableStatus(code) {
		return true
	}
	lower := strings.ToLower(errText)
	for _, sentinel := range transientSentinelMessages {
		if strings.Contains(lower, strings.ToLower(sentinel)) {
			return true
		}
	}
	return false
}

func (s *Service) recordOutcomeHealth(ctx context.Context, id string, status OccurrenceStatus, errText string) error {
	if status != OccurrenceCompleted && status != OccurrenceFailed {
		return nil
	}
	if status == OccurrenceFailed && (modelkeys.IsAccessFailure(errText) || isTransientAutomationFailure(errText)) {
		return nil
	}
	return s.updateAutomationWithRetry(ctx, id, func(a *stored, _ state.Resource) (bool, map[string]string, error) {
		if status == OccurrenceCompleted {
			if a.ConsecutiveFailures == 0 {
				return false, nil, nil
			}
			a.ConsecutiveFailures = 0
		} else {
			a.ConsecutiveFailures++
			if a.ConsecutiveFailures >= maxConsecutiveAutomationFailures && a.Status == AutomationEnabled {
				a.Status = AutomationPaused
				a.NextAt = nil
				slog.WarnContext(ctx, "auto-pausing automation after consecutive failures",
					"automation_id", id, "consecutive_failures", a.ConsecutiveFailures)
			}
		}
		return true, map[string]string{"status": string(a.Status), "goal_id": a.GoalID}, nil
	})
}

func sameFinding(a, b string) bool {
	norm := func(t string) string { return strings.ToLower(strings.Join(strings.Fields(t), " ")) }
	return b != "" && norm(clip(a, 2000)) == norm(b)
}

func (s *Service) checkpoint(ctx context.Context, id, summary string) error {
	return s.updateAutomationWithRetry(ctx, id, func(a *stored, r state.Resource) (bool, map[string]string, error) {
		a.Checkpoint = summary
		return true, r.Index, nil
	})
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
