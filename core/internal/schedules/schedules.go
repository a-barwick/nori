// Package schedules runs what the user asked Nori to do later. Each conversation pass fires the occurrences that are due:
// a reminder becomes its own finished turn, and a scheduled task or monitor check starts a task. Timed wakes bring the
// API up at the next due time, and any later wake catches up. Messages the user hasn't seen reach them by email.
package schedules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"nori/internal/conversation"
	"nori/internal/state"
	"nori/internal/wake"
)

// missedReason is all Nori can say about an occurrence that ran late: nothing ran it on time.
const missedReason = "Nori wasn't running at that time"

// Mailer sends one notification to the user.
type Mailer interface {
	Send(ctx context.Context, subject, body string) error
}

type Service struct {
	Store state.ConversationStore
	Tasks *conversation.Tasks
	Waker wake.Waker
	Clock conversation.Clock
	// Mailer is nil when notifications aren't configured; they are then skipped with that reason.
	Mailer Mailer
	Quiet  state.QuietHours
	// AppURL, when set, is linked from each notification.
	AppURL string
}

func (s *Service) location() *time.Location {
	if s.Clock.Location == nil {
		return time.UTC
	}
	return s.Clock.Location
}

func (s *Service) now() time.Time {
	if s.Clock.Now != nil {
		return s.Clock.Now().In(s.location())
	}
	return time.Now().In(s.location())
}

// Run fires due schedules, asks for the next wakes and sends due notifications.
func (s *Service) Run(ctx context.Context) error {
	fireErr := s.fire(ctx)
	notifyErr := s.notify(ctx)
	return errors.Join(fireErr, notifyErr)
}

func (s *Service) fire(ctx context.Context) error {
	saved, err := s.Store.Schedules(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	var errs error
	for _, schedule := range saved {
		_, fire, due := state.FireSchedule(schedule, now.UTC(), missedReason)
		if due {
			updated, err := s.fireOne(ctx, schedule, fire, now)
			if errors.Is(err, state.ErrConversationBusy) {
				// The reply in progress goes out first; this pass's caller looks again after it.
				return errors.Join(errs, err)
			}
			if err != nil {
				if !errors.Is(err, state.ErrScheduleState) {
					log.Printf("schedule %s %s: %v", schedule.ID, fire.Key, err)
					errs = errors.Join(errs, err)
				}
				continue
			}
			schedule = updated
		}
		s.arm(ctx, schedule, now)
	}
	return errs
}

// fireOne runs one due occurrence and returns the schedule as it moved on.
func (s *Service) fireOne(ctx context.Context, schedule state.Schedule, fire state.ScheduleFire, now time.Time) (state.Schedule, error) {
	location := s.location()
	if schedule.Kind == state.ScheduleReminder || !fire.Run {
		delivery := s.message(schedule, fire, location)
		updated, err := s.Store.ClaimSchedule(ctx, schedule.ID, fire.Key, "", missedReason, &delivery, now.UTC())
		if err == nil {
			log.Printf("schedule %s %s delivered late=%t", schedule.ID, fire.Key, fire.Late)
		}
		return updated, err
	}
	runID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(schedule.ID+"|"+fire.Key)).String()
	_, err := s.Tasks.AcceptBatch(ctx, []state.Command{{
		ID: runID + "-start", RunID: runID, Kind: state.CommandStart, Text: state.ScheduledRequest(schedule, fire, location),
		Schedule: schedule.ID, Quiet: schedule.Kind == state.ScheduleMonitor,
	}}, nil)
	// The task's ID comes from the occurrence, so a pass that started it and stopped before claiming finds it again.
	if err != nil && !errors.Is(err, state.ErrDuplicate) {
		return schedule, fmt.Errorf("start the scheduled task: %w", err)
	}
	updated, err := s.Store.ClaimSchedule(ctx, schedule.ID, fire.Key, runID, missedReason, nil, now.UTC())
	if err == nil {
		log.Printf("schedule %s %s started task %s", schedule.ID, fire.Key, runID)
	}
	return updated, err
}

// message is what a reminder, or a missed task's notice, says.
func (s *Service) message(schedule state.Schedule, fire state.ScheduleFire, location *time.Location) state.ScheduleMessage {
	due := fire.At.In(location).Format("3:04 PM on Monday, January 2")
	if schedule.Kind != state.ScheduleReminder {
		text := fmt.Sprintf("I didn't run your scheduled task at %s, because %s. It was: %s\n\nI can run it once now if you'd like.",
			due, missedReason, clip(schedule.Text, 300))
		note := "Missed schedule notice: the scheduled task " + schedule.ID + " the user set up earlier didn't run at " + due +
			" because " + missedReason + ". Nothing ran. If they ask for it now, use change_schedule run_now once; don't " +
			"run it more than once for missed times."
		return state.ScheduleMessage{Note: note, Text: text, Protocol: protocol(note, text)}
	}
	text := schedule.Text
	if fire.Late {
		text += fmt.Sprintf("\n\nThis was due at %s. It's late because %s.", due, missedReason)
		if fire.Missed > 0 {
			text += fmt.Sprintf(" It also covers %d later time(s) that passed meanwhile.", fire.Missed)
		}
	}
	note := "Scheduled reminder: the user set up reminder " + schedule.ID + " earlier, and it was delivered now. It is their own " +
		"earlier request, not a new message, and asks for nothing more unless they reply."
	return state.ScheduleMessage{Note: note, Text: text, Protocol: protocol(note, text), Urgent: true}
}

func protocol(note, text string) []string {
	return []string{message("system", note), message("assistant", text)}
}

func message(role, content string) string {
	data, _ := json.Marshal(map[string]string{"role": role, "content": content})
	return string(data)
}

func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit] + "…"
}

// arm asks for a timed wake at a schedule's next time, once per time. Cloud Tasks takes at most 30 days ahead; a
// wake that finds nothing due asks for the next one.
func (s *Service) arm(ctx context.Context, schedule state.Schedule, now time.Time) {
	at := schedule.WakeAt()
	if at.IsZero() || !at.After(now) || s.Waker == nil {
		return
	}
	if horizon := now.Add(state.ScheduleWakeHorizon); at.After(horizon) {
		at = horizon
	}
	at = at.UTC()
	// A wake still to come, at or before this time, already covers it.
	if armed := schedule.ArmedFor; armed != nil && armed.After(now) && !armed.After(at) {
		return
	}
	if err := s.Waker.WakeConversationAt(ctx, at, fmt.Sprintf("schedule-%s-%d", schedule.ID, at.Unix())); err != nil {
		log.Printf("schedule %s has no timed wake: %v", schedule.ID, err)
		return
	}
	if err := s.Store.ArmSchedule(ctx, schedule.ID, at); err != nil {
		log.Printf("schedule %s wake not recorded: %v", schedule.ID, err)
	}
}

// notify sends the notifications that are due, and asks for a wake at the next one.
func (s *Service) notify(ctx context.Context) error {
	pending, err := s.Store.PendingNotifications(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	var errs error
	for _, n := range pending {
		if n.SendAfter.After(now) {
			if s.Waker != nil {
				key := fmt.Sprintf("%s-%d", n.ID, n.SendAfter.Unix())
				if err := s.Waker.WakeConversationAt(ctx, n.SendAfter, key); err != nil {
					log.Printf("notification %s has no timed wake: %v", n.ID, err)
				}
			}
			continue
		}
		claimed, decision, err := s.Store.ClaimNotification(ctx, n.ID, s.Quiet, now)
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}
		if decision != state.NotifySend {
			log.Printf("notification %s %s %s", n.ID, decision, claimed.Reason)
			continue
		}
		status, reason := state.NotifySent, ""
		if s.Mailer == nil {
			status, reason = state.NotifySkipped, "no notification address is configured"
		} else if err := s.Mailer.Send(ctx, subject(claimed.Text), s.body(claimed.Text)); err != nil {
			status, reason = state.NotifyFailed, err.Error()
		}
		finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		err = s.Store.FinishNotification(finish, n.ID, status, reason, s.now())
		cancel()
		log.Printf("notification %s %s %s", n.ID, status, reason)
		errs = errors.Join(errs, err)
	}
	return errs
}

func subject(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return "Nori: " + clip(strings.Join(strings.Fields(line), " "), 70)
}

func (s *Service) body(text string) string {
	if s.AppURL == "" {
		return text
	}
	return text + "\n\nOpen Nori: " + s.AppURL
}
