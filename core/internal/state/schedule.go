package state

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// A schedule is something Austin asked Nori to do later: a reminder, a task to run at a time, or a monitor that checks
// something on a cadence and speaks only when it finds what he asked about. Austin's request is what authorizes it, so
// it runs with no new message. Times are kept as a local rule in Austin's time zone, so 8 a.m. stays 8 a.m. across a
// daylight saving change.

// Schedule kinds.
const (
	ScheduleReminder = "reminder"
	ScheduleTask     = "task"
	ScheduleMonitor  = "monitor"
)

// Schedule statuses. Finished means it has no occurrence left; cancelled means Austin stopped it.
const (
	ScheduleActive    = "active"
	SchedulePaused    = "paused"
	ScheduleFinished  = "finished"
	ScheduleCancelled = "cancelled"
)

// How a schedule repeats.
const (
	RepeatOnce                   = "once"
	RepeatDaily                  = "daily"
	RepeatWeekdays               = "weekdays"
	RepeatWeekly                 = "weekly"
	RepeatMonthlyDay             = "monthly_day"
	RepeatMonthlyLastBusinessDay = "monthly_last_business_day"
	RepeatEveryHours             = "every_hours"
)

// HolidaysUSFederal is the one business-day calendar Nori knows.
const HolidaysUSFederal = "us_federal"

// Schedule change actions the outer model can ask for.
const (
	ScheduleCreate = "schedule"
	ScheduleChange = "change_schedule"

	ChangeSkip   = "skip"
	ChangeSnooze = "snooze"
	ChangePause  = "pause"
	ChangeResume = "resume"
	ChangeCancel = "cancel"
	ChangeRunNow = "run_now"
)

const (
	// MaxSchedules bounds the open schedules, which the outer model sees on every turn.
	MaxSchedules = 30
	// MaxScheduleText bounds a reminder's text or a task's instructions.
	MaxScheduleText = 2000
	// ScheduleGrace is how late an occurrence may run and still count as on time. Later than that it was missed.
	ScheduleGrace = 30 * time.Minute
	// MaxScheduleSkips and MaxReported bound the lists a schedule keeps.
	MaxScheduleSkips = 60
	MaxReported      = 10
	// ScheduleWakeHorizon is the furthest ahead a timed wake is asked for. Cloud Tasks takes at most 30 days; a wake
	// that finds nothing due asks for the next one.
	ScheduleWakeHorizon = 29 * 24 * time.Hour
)

var ErrScheduleState = errors.New("schedule is not open")

// ScheduleRule is when a schedule runs, in local time. Start is the first local date it may run on and Ends the last.
type ScheduleRule struct {
	Repeat string `json:"repeat" firestore:"repeat"`
	Start  string `json:"start" firestore:"start"`
	// Time is the local time of day, HH:MM in 24 hours.
	Time string `json:"time" firestore:"time"`
	// Days are the weekdays of a weekly rule: mon, tue, wed, thu, fri, sat, sun.
	Days       []string `json:"days,omitempty" firestore:"days,omitempty"`
	MonthDay   int      `json:"month_day,omitempty" firestore:"month_day,omitempty"`
	EveryHours int      `json:"every_hours,omitempty" firestore:"every_hours,omitempty"`
	Ends       string   `json:"ends,omitempty" firestore:"ends,omitempty"`
	// Holidays names the business-day calendar. Weekday rules skip its holidays when it is set.
	Holidays string `json:"holidays,omitempty" firestore:"holidays,omitempty"`
}

// MissedRun is an occurrence that didn't run on time.
type MissedRun struct {
	At     time.Time `json:"at" firestore:"at"`
	Reason string    `json:"reason" firestore:"reason"`
	// Handled is set once Austin ran it with run_now, or a later occurrence ran.
	Handled bool `json:"handled,omitempty" firestore:"handled,omitempty"`
}

type Schedule struct {
	ID       string       `json:"id" firestore:"id"`
	Kind     string       `json:"kind" firestore:"kind"`
	Text     string       `json:"text" firestore:"text"`
	Rule     ScheduleRule `json:"rule" firestore:"rule"`
	TimeZone string       `json:"time_zone" firestore:"time_zone"`
	Status   string       `json:"status" firestore:"status"`
	// NextAt is the next regular occurrence. SnoozeAt is a one-time extra one that Austin asked for.
	NextAt   *time.Time `json:"next_at,omitempty" firestore:"next_at,omitempty"`
	SnoozeAt *time.Time `json:"snooze_at,omitempty" firestore:"snooze_at,omitempty"`
	// RunNowAt asks for one run as soon as possible, outside the rule. It is when Austin asked.
	RunNowAt *time.Time `json:"run_now_at,omitempty" firestore:"run_now_at,omitempty"`
	// Skips are local dates on which the rule doesn't run.
	Skips []string `json:"skips,omitempty" firestore:"skips,omitempty"`
	// Runs counts occurrences that ran, and LastRunAt and LastRunID say what the latest one did.
	Runs      int         `json:"runs" firestore:"runs"`
	LastRunAt *time.Time  `json:"last_run_at,omitempty" firestore:"last_run_at,omitempty"`
	LastRunID string      `json:"last_run_id,omitempty" firestore:"last_run_id,omitempty"`
	Missed    []MissedRun `json:"missed,omitempty" firestore:"missed,omitempty"`
	// Reported is what a monitor already told Austin, newest last, so it doesn't tell him twice.
	Reported []string `json:"reported,omitempty" firestore:"reported,omitempty"`
	// ArmedFor is the time of the latest timed wake asked for.
	ArmedFor *time.Time `json:"-" firestore:"armed_for,omitempty"`
	// SourceInputID and SourceText are the message that asked for it, in Austin's words.
	SourceInputID string    `json:"source_input_id" firestore:"source_input_id"`
	SourceText    string    `json:"source_text" firestore:"source_text"`
	CreatedAt     time.Time `json:"created_at" firestore:"created_at"`
	UpdatedAt     time.Time `json:"updated_at" firestore:"updated_at"`
}

// Open reports whether a schedule can still run.
func (s Schedule) Open() bool { return s.Status == ScheduleActive || s.Status == SchedulePaused }

// Due returns the occurrence a schedule should run now, if any: a run_now first, then the earlier of a snooze and the
// next regular occurrence.
func (s Schedule) Due(now time.Time) (at time.Time, snooze bool, ok bool) {
	if s.Status != ScheduleActive {
		return time.Time{}, false, false
	}
	if s.RunNowAt != nil {
		return *s.RunNowAt, false, true
	}
	if s.SnoozeAt != nil && !s.SnoozeAt.After(now) && (s.NextAt == nil || !s.SnoozeAt.After(*s.NextAt)) {
		return *s.SnoozeAt, true, true
	}
	if s.NextAt != nil && !s.NextAt.After(now) {
		return *s.NextAt, false, true
	}
	return time.Time{}, false, false
}

// WakeAt is when a schedule next needs a look, or zero.
func (s Schedule) WakeAt() time.Time {
	if s.Status != ScheduleActive {
		return time.Time{}
	}
	if s.RunNowAt != nil {
		return *s.RunNowAt
	}
	var at time.Time
	if s.NextAt != nil {
		at = *s.NextAt
	}
	if s.SnoozeAt != nil && (at.IsZero() || s.SnoozeAt.Before(at)) {
		at = *s.SnoozeAt
	}
	return at
}

// ScheduleChangeRequest is one schedule or change_schedule the outer model asked for. It is applied when the turn's
// reply is published, in the same transaction, like a memory change.
type ScheduleChangeRequest struct {
	Action string `json:"action" firestore:"action"`
	// ID is the schedule created or changed. A new schedule's ID comes from the attempt and call.
	ID   string       `json:"id" firestore:"id"`
	Kind string       `json:"kind,omitempty" firestore:"kind,omitempty"`
	Text string       `json:"text,omitempty" firestore:"text,omitempty"`
	Rule ScheduleRule `json:"rule,omitempty" firestore:"rule,omitempty"`
	// Change is the change_schedule action. Date is a skip's local date, and Until a snooze's local date and time.
	Change string `json:"change,omitempty" firestore:"change,omitempty"`
	Date   string `json:"date,omitempty" firestore:"date,omitempty"`
	Until  string `json:"until,omitempty" firestore:"until,omitempty"`
	// TimeZone is Austin's time zone when the change was asked for. A new schedule keeps it.
	TimeZone string `json:"time_zone,omitempty" firestore:"time_zone,omitempty"`
	// Error says why the change was refused. A turn with any refused change applies none of them.
	Error string `json:"error,omitempty" firestore:"error,omitempty"`
	// NextAt is what the change left as the next occurrence, for the reply's record.
	NextAt *time.Time `json:"next_at,omitempty" firestore:"next_at,omitempty"`
}

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday,
	"fri": time.Friday, "sat": time.Saturday,
}

func parseDate(value string, location *time.Location) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", value, location)
}

// ValidateRule checks a rule and returns why it can't be used.
func ValidateRule(r ScheduleRule, location *time.Location) error {
	if _, err := parseDate(r.Start, location); err != nil {
		return fmt.Errorf("start must be a date like 2026-11-06")
	}
	if r.Ends != "" {
		ends, err := parseDate(r.Ends, location)
		if err != nil {
			return fmt.Errorf("ends must be a date like 2026-11-20")
		}
		start, _ := parseDate(r.Start, location)
		if ends.Before(start) {
			return fmt.Errorf("ends is before start")
		}
	}
	if _, _, err := clockTime(r.Time); err != nil {
		return err
	}
	if r.Holidays != "" && r.Holidays != HolidaysUSFederal {
		return fmt.Errorf("the only business-day calendar is %s", HolidaysUSFederal)
	}
	switch r.Repeat {
	case RepeatOnce, RepeatDaily, RepeatWeekdays, RepeatMonthlyLastBusinessDay:
	case RepeatWeekly:
		if len(r.Days) == 0 {
			return fmt.Errorf("a weekly schedule needs days")
		}
		for _, day := range r.Days {
			if _, ok := weekdays[day]; !ok {
				return fmt.Errorf("days must be mon, tue, wed, thu, fri, sat or sun")
			}
		}
	case RepeatMonthlyDay:
		if r.MonthDay < 1 || r.MonthDay > 31 {
			return fmt.Errorf("month_day must be 1–31")
		}
	case RepeatEveryHours:
		if r.EveryHours < 1 || r.EveryHours > 24*7 {
			return fmt.Errorf("every_hours must be 1–168")
		}
	default:
		return fmt.Errorf("repeat must be once, daily, weekdays, weekly, monthly_day, monthly_last_business_day or every_hours")
	}
	return nil
}

func clockTime(value string) (int, int, error) {
	at, err := time.Parse("15:04", value)
	if err != nil {
		return 0, 0, fmt.Errorf("time must be HH:MM in 24 hours")
	}
	return at.Hour(), at.Minute(), nil
}

// NextOccurrence returns the first occurrence of a rule after a time, skipping the given local dates. ok is false when
// the rule has none left.
func NextOccurrence(r ScheduleRule, skips []string, location *time.Location, after time.Time) (time.Time, bool) {
	start, err := parseDate(r.Start, location)
	if err != nil {
		return time.Time{}, false
	}
	hour, minute, err := clockTime(r.Time)
	if err != nil {
		return time.Time{}, false
	}
	var ends time.Time
	if r.Ends != "" {
		if ends, err = parseDate(r.Ends, location); err != nil {
			return time.Time{}, false
		}
	}
	first := time.Date(start.Year(), start.Month(), start.Day(), hour, minute, 0, 0, location)
	if r.Repeat == RepeatEveryHours {
		step := time.Duration(r.EveryHours) * time.Hour
		at := first
		if at.Before(after) || at.Equal(after) {
			at = first.Add((after.Sub(first)/step + 1) * step)
		}
		for i := 0; i < 1000; i++ {
			local := at.In(location)
			if !ends.IsZero() && local.After(ends.AddDate(0, 0, 1)) {
				return time.Time{}, false
			}
			if !slices.Contains(skips, local.Format("2006-01-02")) {
				return at, true
			}
			at = at.Add(step)
		}
		return time.Time{}, false
	}
	day := after.In(location)
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, location)
	if day.Before(start) {
		day = start
	}
	// Five years covers any rule that still has an occurrence.
	for i := 0; i < 366*5; i++ {
		date := day.AddDate(0, 0, i)
		if !ends.IsZero() && date.After(ends) {
			return time.Time{}, false
		}
		at := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, location)
		if r.Repeat == RepeatOnce && date.After(start) {
			return time.Time{}, false
		}
		if !at.After(after) || slices.Contains(skips, date.Format("2006-01-02")) || !ruleMatches(r, date) {
			continue
		}
		return at, true
	}
	return time.Time{}, false
}

func ruleMatches(r ScheduleRule, date time.Time) bool {
	start, _ := parseDate(r.Start, date.Location())
	switch r.Repeat {
	case RepeatOnce:
		return date.Year() == start.Year() && date.YearDay() == start.YearDay()
	case RepeatDaily:
		return true
	case RepeatWeekdays:
		return businessDay(date, r.Holidays)
	case RepeatWeekly:
		for _, day := range r.Days {
			if weekdays[day] == date.Weekday() {
				return true
			}
		}
		return false
	case RepeatMonthlyDay:
		last := time.Date(date.Year(), date.Month()+1, 0, 0, 0, 0, 0, date.Location()).Day()
		return date.Day() == min(r.MonthDay, last)
	case RepeatMonthlyLastBusinessDay:
		if !businessDay(date, r.Holidays) {
			return false
		}
		for next := date.AddDate(0, 0, 1); next.Month() == date.Month(); next = next.AddDate(0, 0, 1) {
			if businessDay(next, r.Holidays) {
				return false
			}
		}
		return true
	}
	return false
}

func businessDay(date time.Time, holidays string) bool {
	if date.Weekday() == time.Saturday || date.Weekday() == time.Sunday {
		return false
	}
	return holidays != HolidaysUSFederal || !USFederalHoliday(date)
}

// USFederalHoliday reports whether a date is a US federal holiday as observed: one on a Saturday is observed the
// Friday before, and one on a Sunday the Monday after.
func USFederalHoliday(date time.Time) bool {
	y, m, d := date.Date()
	location := date.Location()
	nth := func(month time.Month, weekday time.Weekday, n int) time.Time {
		first := time.Date(y, month, 1, 0, 0, 0, 0, location)
		offset := (int(weekday) - int(first.Weekday()) + 7) % 7
		return first.AddDate(0, 0, offset+7*(n-1))
	}
	last := func(month time.Month, weekday time.Weekday) time.Time {
		end := time.Date(y, month+1, 0, 0, 0, 0, 0, location)
		return end.AddDate(0, 0, -((int(end.Weekday()) - int(weekday) + 7) % 7))
	}
	observed := func(year int, month time.Month, day int) time.Time {
		at := time.Date(year, month, day, 0, 0, 0, 0, location)
		switch at.Weekday() {
		case time.Saturday:
			return at.AddDate(0, 0, -1)
		case time.Sunday:
			return at.AddDate(0, 0, 1)
		}
		return at
	}
	holidays := []time.Time{
		// New Year's Day of the next year can be observed on December 31.
		observed(y, time.January, 1), observed(y+1, time.January, 1),
		nth(time.January, time.Monday, 3), nth(time.February, time.Monday, 3), last(time.May, time.Monday),
		observed(y, time.June, 19), observed(y, time.July, 4), nth(time.September, time.Monday, 1),
		nth(time.October, time.Monday, 2), observed(y, time.November, 11), nth(time.November, time.Thursday, 4),
		observed(y, time.December, 25),
	}
	for _, h := range holidays {
		hy, hm, hd := h.Date()
		if hy == y && hm == m && hd == d {
			return true
		}
	}
	return false
}

// PlanScheduleChanges checks a turn's schedule changes against the saved schedules. It fills in Error on each change
// that can't be applied and NextAt on the rest, and reports whether all of them can apply.
func PlanScheduleChanges(saved []Schedule, changes []ScheduleChangeRequest, location *time.Location, now time.Time) bool {
	byID := map[string]Schedule{}
	open := 0
	for _, s := range saved {
		byID[s.ID] = s
		if s.Open() {
			open++
		}
	}
	ok := true
	for i := range changes {
		c := &changes[i]
		var s Schedule
		var err error
		if c.Action == ScheduleCreate {
			if open >= MaxSchedules {
				err = fmt.Errorf("there are already %d open schedules", MaxSchedules)
			} else {
				s, err = NewSchedule(*c, location, now)
				open++
			}
		} else if prior, found := byID[c.ID]; !found {
			err = fmt.Errorf("no schedule has ID %s", c.ID)
		} else {
			s, err = ChangeSchedule(prior, *c, location, now)
		}
		if err != nil {
			c.Error, ok = err.Error(), false
			continue
		}
		byID[s.ID] = s
		c.NextAt = nil
		if at := s.WakeAt(); !at.IsZero() {
			c.NextAt = &at
		}
	}
	return ok
}

// NewSchedule builds a schedule from a create request.
func NewSchedule(c ScheduleChangeRequest, location *time.Location, now time.Time) (Schedule, error) {
	text := strings.TrimSpace(c.Text)
	if text == "" || len(text) > MaxScheduleText || !utf8.ValidString(text) {
		return Schedule{}, fmt.Errorf("text must be 1–%d bytes", MaxScheduleText)
	}
	if c.Kind != ScheduleReminder && c.Kind != ScheduleTask && c.Kind != ScheduleMonitor {
		return Schedule{}, fmt.Errorf("kind must be reminder, task or monitor")
	}
	if err := ValidateRule(c.Rule, location); err != nil {
		return Schedule{}, err
	}
	s := Schedule{
		ID: c.ID, Kind: c.Kind, Text: text, Rule: c.Rule, TimeZone: location.String(), Status: ScheduleActive,
		Skips: []string{}, Missed: []MissedRun{}, Reported: []string{}, CreatedAt: now, UpdatedAt: now,
	}
	next, found := NextOccurrence(c.Rule, nil, location, now.Add(-time.Minute))
	if !found {
		return Schedule{}, fmt.Errorf("that time has already passed")
	}
	s.NextAt = &next
	return s, nil
}

// ChangeSchedule applies one change_schedule to a saved schedule.
func ChangeSchedule(s Schedule, c ScheduleChangeRequest, location *time.Location, now time.Time) (Schedule, error) {
	if !s.Open() {
		return s, fmt.Errorf("%w: it was %s", ErrScheduleState, s.Status)
	}
	if loaded, err := time.LoadLocation(s.TimeZone); err == nil {
		location = loaded
	}
	s.UpdatedAt = now
	switch c.Change {
	case ChangeSkip:
		date, err := parseDate(c.Date, location)
		if err != nil {
			return s, fmt.Errorf("skip needs a date like 2026-11-11")
		}
		if len(s.Skips) >= MaxScheduleSkips {
			return s, fmt.Errorf("a schedule keeps at most %d skipped dates", MaxScheduleSkips)
		}
		if !slices.Contains(s.Skips, c.Date) {
			s.Skips = append(s.Skips, c.Date)
		}
		if s.NextAt != nil && s.NextAt.In(location).Format("2006-01-02") == date.Format("2006-01-02") {
			return advance(s, location, *s.NextAt), nil
		}
	case ChangeSnooze:
		until, err := time.ParseInLocation("2006-01-02T15:04", c.Until, location)
		if err != nil {
			return s, fmt.Errorf("snooze needs a local time like 2026-11-06T08:00")
		}
		if !until.After(now) {
			return s, fmt.Errorf("snooze time has already passed")
		}
		s.SnoozeAt = &until
	case ChangePause:
		s.Status = SchedulePaused
	case ChangeResume:
		s.Status = ScheduleActive
		// Occurrences that passed while paused were Austin's choice, not missed runs.
		if s.NextAt != nil && !s.NextAt.After(now) {
			s = advance(s, location, now)
		}
	case ChangeCancel:
		s.Status, s.NextAt, s.SnoozeAt, s.RunNowAt = ScheduleCancelled, nil, nil, nil
	case ChangeRunNow:
		if s.Status != ScheduleActive {
			return s, fmt.Errorf("resume it before running it")
		}
		s.RunNowAt = &now
		for i := range s.Missed {
			s.Missed[i].Handled = true
		}
	default:
		return s, fmt.Errorf("change must be skip, snooze, pause, resume, cancel or run_now")
	}
	return s, nil
}

// advance moves the next regular occurrence past a time, and finishes a schedule that has none left.
func advance(s Schedule, location *time.Location, after time.Time) Schedule {
	next, ok := NextOccurrence(s.Rule, s.Skips, location, after)
	if ok {
		s.NextAt = &next
		return s
	}
	s.NextAt = nil
	if s.SnoozeAt == nil && s.RunNowAt == nil && s.Status == ScheduleActive {
		s.Status = ScheduleFinished
	}
	return s
}

// ScheduleFire is what one due occurrence does, decided when it is claimed.
type ScheduleFire struct {
	At time.Time
	// Late is set when the occurrence ran later than ScheduleGrace. Missed counts the regular occurrences it covers.
	Late   bool
	Missed int
	// Run is false for a task occurrence that was missed: Austin decides whether it runs.
	Run bool
	// Key names this occurrence, for the IDs of the message or task it makes.
	Key string
}

// FireSchedule claims a schedule's due occurrence and moves it on. Every regular occurrence already past is covered
// by this one, so a scheduler that was down runs it once, not once per missed time.
func FireSchedule(s Schedule, now time.Time, reason string) (Schedule, ScheduleFire, bool) {
	at, snooze, ok := s.Due(now)
	if !ok {
		return s, ScheduleFire{}, false
	}
	location, err := time.LoadLocation(s.TimeZone)
	if err != nil {
		location = time.UTC
	}
	fire := ScheduleFire{At: at, Run: true}
	switch {
	case s.RunNowAt != nil:
		fire.Key = fmt.Sprintf("now-%d", s.RunNowAt.Unix())
		s.RunNowAt = nil
		s.UpdatedAt = now
		s.Runs++
		s.LastRunAt = &now
		if s.NextAt == nil && s.SnoozeAt == nil {
			s.Status = ScheduleFinished
		}
		return s, fire, true
	case snooze:
		s.SnoozeAt = nil
		fire.Key = fmt.Sprintf("snooze-%d", at.Unix())
	default:
		fire.Key = fmt.Sprintf("at-%d", at.Unix())
		for next := at; !next.After(now); {
			fire.Missed++
			following, ok := NextOccurrence(s.Rule, s.Skips, location, next)
			if !ok {
				break
			}
			next = following
		}
		fire.Missed--
		s = advance(s, location, now)
	}
	fire.Late = now.Sub(at) > ScheduleGrace
	if fire.Late && s.Kind == ScheduleTask {
		// A late task doesn't run on its own: the world it was meant to look at has moved on. Austin is told and
		// decides with run_now.
		fire.Run = false
		s.Missed = append(s.Missed, MissedRun{At: at, Reason: reason})
		if len(s.Missed) > MaxScheduleSkips {
			s.Missed = s.Missed[len(s.Missed)-MaxScheduleSkips:]
		}
	} else {
		s.Runs++
		s.LastRunAt = &now
		for i := range s.Missed {
			s.Missed[i].Handled = true
		}
	}
	s.UpdatedAt = now
	if s.NextAt == nil && s.SnoozeAt == nil && s.RunNowAt == nil && s.Status == ScheduleActive {
		s.Status = ScheduleFinished
	}
	return s, fire, true
}

// Monitor results start with one of these lines, which decide whether Austin hears about it.
const (
	MonitorNothing   = "NOTHING_NEW"
	MonitorAlert     = "ALERT"
	MonitorAlertStop = "ALERT_AND_STOP"
	MonitorStop      = "STOP"
)

// MonitorOutcome reads a monitor result's first line. A result with no marker is treated as an alert, so a check
// that couldn't say what it found isn't hidden.
func MonitorOutcome(result string) (alert, stop bool) {
	line, _, _ := strings.Cut(strings.TrimSpace(result), "\n")
	switch strings.Trim(strings.TrimSpace(line), "*_`:. ") {
	case MonitorNothing:
		return false, false
	case MonitorStop:
		return false, true
	case MonitorAlertStop:
		return true, true
	}
	return true, false
}

// ScheduledRequest is the task request a scheduled task or monitor occurrence starts with.
func ScheduledRequest(s Schedule, fire ScheduleFire, location *time.Location) string {
	lines := []string{
		fmt.Sprintf("Scheduled work Austin asked for earlier, running now (%s). Do it with current information, as of now, "+
			"not anything from when he asked.", fire.At.In(location).Format("Monday, January 2, 2006 at 3:04 PM MST")),
		"His request: " + s.Text,
	}
	if s.Kind == ScheduleMonitor {
		lines = append(lines,
			"This is one check of a monitor. Don't buy, book, send, change or contact anyone: only look.",
			"Start your result with exactly one line saying what happened, then a short report:",
			MonitorNothing+" when nothing he asked to hear about happened. Austin won't see it.",
			MonitorAlert+" when you found something he asked to hear about.",
			MonitorAlertStop+" when you found it and his stopping point has been reached.",
			MonitorStop+" when his stopping point has been reached and there's nothing new to tell him.")
		if len(s.Reported) > 0 {
			lines = append(lines, "Already reported to Austin, so it's not new unless something changed: "+strings.Join(s.Reported, " | "))
		}
		if s.Rule.Ends != "" {
			lines = append(lines, "The monitor ends after "+s.Rule.Ends+".")
		}
	}
	return strings.Join(lines, "\n")
}

// ReportedLine is how a monitor alert is remembered: its first lines after the marker, bounded.
func ReportedLine(result string) string {
	_, rest, _ := strings.Cut(strings.TrimSpace(result), "\n")
	rest = strings.Join(strings.Fields(rest), " ")
	if len(rest) > 240 {
		cut := 240
		for cut > 0 && !utf8.RuneStart(rest[cut]) {
			cut--
		}
		rest = rest[:cut] + "…"
	}
	return rest
}

// ApplyMonitorResult records what a finished monitor run found.
func ApplyMonitorResult(s Schedule, result string, now time.Time) Schedule {
	alert, stop := MonitorOutcome(result)
	if alert {
		s.Reported = append(s.Reported, ReportedLine(result))
		if len(s.Reported) > MaxReported {
			s.Reported = s.Reported[len(s.Reported)-MaxReported:]
		}
	}
	if stop && s.Open() {
		s.Status, s.NextAt, s.SnoozeAt, s.RunNowAt = ScheduleFinished, nil, nil, nil
	}
	s.UpdatedAt = now
	return s
}

// ScheduleMessage is the message a reminder, or a missed task's notice, publishes. Note is what the outer model later
// sees about it, Text what Austin sees, and Protocol the turn's saved model history. Urgent notifications go out at the
// time Austin set, even in quiet hours.
type ScheduleMessage struct {
	Note, Text string
	Protocol   []string
	Urgent     bool
}

func ScheduleInputID(id, key string) string   { return "input-schedule-" + id + "-" + key }
func ScheduleTurnID(id, key string) string    { return "turn-schedule-" + id + "-" + key }
func ScheduleMessageID(id, key string) string { return ScheduleTurnID(id, key) + "-assistant" }

// ScheduleTurn is the finished turn and message a reminder, or a missed task's notice, becomes.
func ScheduleTurn(c ConversationControl, s Schedule, key, note, text string, now time.Time) (ConversationControl, ConversationInput, ConversationTurn, Message) {
	c.Received++
	turnID := ScheduleTurnID(s.ID, key)
	input := ConversationInput{ID: ScheduleInputID(s.ID, key), Text: note, Position: c.Received, CreatedAt: now, Schedule: s.ID}
	t := ConversationTurn{
		ID: turnID, Inputs: []ConversationInput{input}, Through: c.Received, UserThrough: min(c.UserReceived, c.Consumed),
		StartedAt: now, FinishedAt: &now, Status: "completed", Protocol: []string{}, Context: []string{},
		Attempt: ConversationAttempt{
			ID: turnID + "-attempt-1", TurnID: turnID, Number: 1, InputIDs: []string{input.ID}, Model: "none",
			Status: "completed", StartedAt: now, FinishedAt: &now, Text: text, Results: []string{}, Actions: []ConversationAction{}, Usage: []Usage{},
		},
	}
	c.Consumed = c.Received
	c.LatestAt = now
	message := Message{ID: ScheduleMessageID(s.ID, key), Role: "assistant", Text: text, CreatedAt: now, TurnID: t.ID, Parts: TextParts(text)}
	return c, input, t, message
}
