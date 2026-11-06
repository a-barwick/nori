package conversation

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/state"
)

// Clock is the user's time zone and the current time, which the outer model needs to resolve "in 20 minutes" or
// "tomorrow at ten" into a schedule.
type Clock struct {
	Location *time.Location
}

func (c Clock) now() time.Time {
	location := c.location()
	return time.Now().In(location)
}

func (c Clock) location() *time.Location {
	if c.Location == nil {
		return time.UTC
	}
	return c.Location
}

// ChangeSchedule pauses, resumes or cancels a schedule from a client control. The next conversation pass arms a
// resumed schedule's wake.
func (r *Runner) ChangeSchedule(ctx context.Context, id, change string) (state.Schedule, error) {
	if change != state.ChangePause && change != state.ChangeResume && change != state.ChangeCancel {
		return state.Schedule{}, fmt.Errorf("%w: change must be pause, resume or cancel", state.ErrCommandInvalid)
	}
	return r.tasks.Store.ChangeScheduleNow(ctx, id, change, r.Clock.location(), r.Clock.now())
}

type scheduleCall struct {
	callID string
	change state.ScheduleChangeRequest
}

// splitSchedules separates schedule and change_schedule actions, which the runtime applies itself, from task commands.
func splitSchedules(calls []ToolCall) ([]ToolCall, []scheduleCall, error) {
	commands := make([]ToolCall, 0, len(calls))
	changes := []scheduleCall{}
	for _, call := range calls {
		switch call.Function.Name {
		case state.ScheduleCreate:
			var args struct {
				Kind       string   `json:"kind"`
				Text       string   `json:"text"`
				Repeat     string   `json:"repeat"`
				Start      string   `json:"start"`
				Time       string   `json:"time"`
				Days       []string `json:"days"`
				MonthDay   *int     `json:"month_day"`
				EveryHours *int     `json:"every_hours"`
				Ends       *string  `json:"ends"`
				Holidays   *string  `json:"holidays"`
			}
			if err := decodeCommandArguments(call.Function.Arguments, &args); err != nil {
				return nil, nil, err
			}
			rule := state.ScheduleRule{Repeat: args.Repeat, Start: args.Start, Time: args.Time, Days: args.Days}
			if args.MonthDay != nil {
				rule.MonthDay = *args.MonthDay
			}
			if args.EveryHours != nil {
				rule.EveryHours = *args.EveryHours
			}
			if args.Ends != nil {
				rule.Ends = *args.Ends
			}
			if args.Holidays != nil {
				rule.Holidays = *args.Holidays
			}
			changes = append(changes, scheduleCall{call.ID, state.ScheduleChangeRequest{
				Action: state.ScheduleCreate, Kind: args.Kind, Text: strings.TrimSpace(args.Text), Rule: rule,
			}})
		case state.ScheduleChange:
			var args struct {
				ScheduleID string  `json:"schedule_id"`
				Change     string  `json:"change"`
				Date       *string `json:"date"`
				Until      *string `json:"until"`
			}
			if err := decodeCommandArguments(call.Function.Arguments, &args); err != nil {
				return nil, nil, err
			}
			if !canonicalID(args.ScheduleID) {
				return nil, nil, fmt.Errorf("change_schedule names an invalid schedule ID")
			}
			change := state.ScheduleChangeRequest{Action: state.ScheduleChange, ID: args.ScheduleID, Change: args.Change}
			if args.Date != nil {
				change.Date = *args.Date
			}
			if args.Until != nil {
				change.Until = *args.Until
			}
			changes = append(changes, scheduleCall{call.ID, change})
		default:
			commands = append(commands, call)
		}
	}
	return commands, changes, nil
}

// planSchedules names new schedules and checks every change against the saved ones. A new schedule's ID comes from the
// attempt and call, so a repeated attempt names it the same way.
func planSchedules(attemptID string, calls []scheduleCall, saved []state.Schedule, clock Clock) ([]state.ScheduleChangeRequest, bool) {
	changes := make([]state.ScheduleChangeRequest, 0, len(calls))
	for _, call := range calls {
		change := call.change
		if change.Action == state.ScheduleCreate {
			change.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(attemptID+":schedule:"+call.callID)).String()
		}
		change.TimeZone = clock.location().String()
		changes = append(changes, change)
	}
	return changes, state.PlanScheduleChanges(saved, changes, clock.location(), clock.now())
}

// scheduleFailureText replaces the reply when a schedule change was refused, because the reply was written before
// the model knew.
func scheduleFailureText(changes []state.ScheduleChangeRequest) string {
	for _, c := range changes {
		if c.Error != "" {
			return fmt.Sprintf("I couldn't set that up: %s. No schedules changed.", c.Error)
		}
	}
	return ""
}

// scheduleText acknowledges applied schedule changes when the model wrote no reply text.
func scheduleText(changes []state.ScheduleChangeRequest, clock Clock) string {
	if len(changes) == 0 {
		return ""
	}
	for _, c := range changes {
		if c.Action == state.ScheduleCreate && c.NextAt != nil {
			return "Done. The first one is " + localTime(*c.NextAt, clock.location()) + "."
		}
	}
	return "Done. I've updated your schedules."
}

func localTime(at time.Time, location *time.Location) string {
	return at.In(location).Format("Monday, January 2 at 3:04 PM MST")
}

// scheduleContext is what the outer model sees of the time and the schedules. Closed ones from the last week stay
// visible so "did you cancel it?" has an answer.
func scheduleContext(saved []state.Schedule, clock Clock) map[string]any {
	now := clock.now()
	location := clock.location()
	type shown struct {
		ID       string             `json:"id"`
		Kind     string             `json:"kind"`
		Text     string             `json:"text"`
		Status   string             `json:"status"`
		Rule     state.ScheduleRule `json:"rule"`
		Next     string             `json:"next,omitempty"`
		Snoozed  string             `json:"snoozed_to,omitempty"`
		Skips    []string           `json:"skipped_dates,omitempty"`
		LastRun  string             `json:"last_ran,omitempty"`
		LastTask string             `json:"last_task_id,omitempty"`
		Missed   []string           `json:"missed,omitempty"`
		Reported []string           `json:"already_reported,omitempty"`
	}
	list := []shown{}
	for _, s := range saved {
		if !s.Open() && now.Sub(s.UpdatedAt) > 7*24*time.Hour {
			continue
		}
		item := shown{ID: s.ID, Kind: s.Kind, Text: s.Text, Status: s.Status, Rule: s.Rule, Skips: s.Skips, LastTask: s.LastRunID, Reported: s.Reported}
		if s.NextAt != nil {
			item.Next = localTime(*s.NextAt, location)
		}
		if s.SnoozeAt != nil {
			item.Snoozed = localTime(*s.SnoozeAt, location)
		}
		if s.LastRunAt != nil {
			item.LastRun = localTime(*s.LastRunAt, location)
		}
		for _, m := range s.Missed {
			if !m.Handled {
				item.Missed = append(item.Missed, localTime(m.At, location)+": "+m.Reason)
			}
		}
		list = append(list, item)
	}
	return map[string]any{
		"now":       now.Format("Monday, January 2, 2006 15:04 MST") + " (" + location.String() + ")",
		"schedules": list,
	}
}

// openScheduleIDs are the schedules change_schedule may name.
func openScheduleIDs(saved []state.Schedule) []string {
	ids := []string{}
	for _, s := range saved {
		if s.Open() {
			ids = append(ids, s.ID)
		}
	}
	return slices.Clip(ids)
}
