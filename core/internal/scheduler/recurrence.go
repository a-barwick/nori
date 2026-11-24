package scheduler

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Rule is the supported subset of RFC 5545 RRULE: FREQ (DAILY, WEEKLY, MONTHLY, YEARLY), INTERVAL, BYDAY (with
// ordinals for MONTHLY), BYMONTHDAY, BYMONTH, BYHOUR, BYMINUTE, COUNT and UNTIL. Unsupported parts are rejected so a
// schedule never runs on a silently simplified rule.
type Rule struct {
	Freq          string
	Interval      int
	WeekStart     time.Weekday
	ByDay         []weekdayRule
	ByMonthDay    []int
	ByMonth       []int
	ByHour        []int
	ByMinute      []int
	Count         int
	Until         *time.Time
	UntilDate     bool
	UntilFloating bool
}

type weekdayRule struct {
	Ordinal int
	Day     time.Weekday
}

var weekdays = map[string]time.Weekday{"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday, "TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday}

// ParseRule parses an RRULE value (without the "RRULE:" prefix, which is tolerated).
func ParseRule(value string) (Rule, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "RRULE:")
	rule := Rule{Interval: 1, WeekStart: time.Monday}
	ints := func(text string, min, max int) ([]int, error) {
		var out []int
		for _, part := range strings.Split(text, ",") {
			n, err := strconv.Atoi(part)
			if err != nil || n < min || n > max || n == 0 && min < 0 {
				return nil, fmt.Errorf("invalid value %q", part)
			}
			out = append(out, n)
		}
		return out, nil
	}
	for _, part := range strings.Split(value, ";") {
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			return rule, fmt.Errorf("invalid RRULE part %q", part)
		}
		var err error
		switch strings.ToUpper(key) {
		case "FREQ":
			rule.Freq = strings.ToUpper(val)
			switch rule.Freq {
			case "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
			default:
				return rule, fmt.Errorf("unsupported FREQ %s", val)
			}
		case "INTERVAL":
			rule.Interval, err = strconv.Atoi(val)
			if err != nil || rule.Interval < 1 || rule.Interval > 1000 {
				return rule, errors.New("INTERVAL must be 1-1000")
			}
		case "BYDAY":
			for _, day := range strings.Split(strings.ToUpper(val), ",") {
				ordinal := 0
				if len(day) > 2 {
					ordinal, err = strconv.Atoi(day[:len(day)-2])
					if err != nil || ordinal == 0 || ordinal < -5 || ordinal > 5 {
						return rule, fmt.Errorf("invalid BYDAY %s", day)
					}
					day = day[len(day)-2:]
				}
				weekday, ok := weekdays[day]
				if !ok {
					return rule, fmt.Errorf("invalid BYDAY %s", day)
				}
				rule.ByDay = append(rule.ByDay, weekdayRule{ordinal, weekday})
			}
		case "BYMONTHDAY":
			rule.ByMonthDay, err = ints(val, -31, 31)
		case "BYMONTH":
			rule.ByMonth, err = ints(val, 1, 12)
		case "BYHOUR":
			rule.ByHour, err = ints(val, 0, 23)
		case "BYMINUTE":
			rule.ByMinute, err = ints(val, 0, 59)
		case "COUNT":
			rule.Count, err = strconv.Atoi(val)
			if err == nil && rule.Count < 1 {
				err = errors.New("COUNT must be positive")
			}
		case "UNTIL":
			var until time.Time
			for _, layout := range []string{"20060102T150405Z", "20060102T150405", "20060102"} {
				if until, err = time.Parse(layout, val); err == nil {
					rule.UntilDate = layout == "20060102"
					rule.UntilFloating = layout == "20060102T150405"
					break
				}
			}
			if err != nil {
				return rule, fmt.Errorf("invalid UNTIL %s", val)
			}
			rule.Until = &until
		case "WKST":
			weekday, ok := weekdays[strings.ToUpper(val)]
			if !ok {
				return rule, fmt.Errorf("invalid WKST %s", val)
			}
			rule.WeekStart = weekday
		default:
			return rule, fmt.Errorf("unsupported RRULE part %s", key)
		}
		if err != nil {
			return rule, err
		}
	}
	if rule.Freq == "" {
		return rule, errors.New("RRULE needs FREQ")
	}
	return rule, nil
}

// LocalTime resolves a floating local date-time in a zone deterministically: a repeated local time runs once at the
// earlier instant; a local time skipped by a daylight-saving gap moves to the first valid instant after the gap.
func LocalTime(year int, month time.Month, day, hour, minute, second int, loc *time.Location) (time.Time, bool) {
	guess := time.Date(year, month, day, hour, minute, second, 0, loc)
	want := time.Date(year, month, day, hour, minute, second, 0, time.UTC)
	var valid []time.Time
	for _, probe := range []time.Time{guess.Add(-3 * time.Hour), guess, guess.Add(3 * time.Hour)} {
		_, offset := probe.Zone()
		candidate := want.Add(-time.Duration(offset) * time.Second)
		local := candidate.In(loc)
		if local.Year() == year && local.Month() == month && local.Day() == day && local.Hour() == hour && local.Minute() == minute && local.Second() == second {
			valid = append(valid, candidate)
		}
	}
	if len(valid) > 0 {
		sort.Slice(valid, func(i, j int) bool { return valid[i].Before(valid[j]) })
		return valid[0], false
	}
	// In a gap: find the transition, which is the first valid instant after the skipped wall time.
	_, before := guess.Add(-3 * time.Hour).Zone()
	start := want.Add(-time.Duration(before) * time.Second).Add(-2 * time.Hour)
	for t := start; t.Before(start.Add(6 * time.Hour)); t = t.Add(time.Minute) {
		if _, offset := t.In(loc).Zone(); offset != before {
			return t.Truncate(time.Minute), true
		}
	}
	return guess, true
}

// Expand returns up to limit occurrence instants of a recurring trigger strictly after `after`, starting at the
// floating local start in its zone.
func Expand(localStart, zone, rrule string, after time.Time, limit int) ([]time.Time, error) {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %s", zone)
	}
	start, err := parseLocal(localStart)
	if err != nil {
		return nil, err
	}
	rule, err := ParseRule(rrule)
	if err != nil {
		return nil, err
	}
	var untilInstant *time.Time
	if rule.Until != nil && !rule.UntilDate {
		u := *rule.Until
		if rule.UntilFloating {
			resolved, _ := LocalTime(u.Year(), u.Month(), u.Day(), u.Hour(), u.Minute(), u.Second(), loc)
			u = resolved
		}
		untilInstant = &u
	}
	var out []time.Time
	count := 0
	for period := 0; period < 100000 && len(out) < limit; period++ {
		candidates := rule.period(start, period)
		for _, local := range candidates {
			if local.Before(start) {
				continue
			}
			instant, _ := LocalTime(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), local.Second(), loc)
			if rule.Until != nil {
				if rule.UntilDate {
					if local.Format("20060102") > rule.Until.Format("20060102") {
						return out, nil
					}
				} else if untilInstant != nil && instant.After(*untilInstant) {
					return out, nil
				}
			}
			count++
			if rule.Count > 0 && count > rule.Count {
				return out, nil
			}
			if instant.After(after) {
				out = append(out, instant)
				if len(out) >= limit {
					return out, nil
				}
			}
		}
		if rule.Until != nil && len(candidates) > 0 && candidates[0].After(rule.Until.Add(366*24*time.Hour)) {
			return out, nil
		}
	}
	return out, nil
}

func parseLocal(value string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("local_start %q must be YYYY-MM-DDTHH:MM[:SS] without an offset", value)
}

// period lists the floating local candidates of one period (a day, week, month or year) in order.
func (r Rule) period(start time.Time, n int) []time.Time {
	var days []time.Time
	switch r.Freq {
	case "DAILY":
		day := start.AddDate(0, 0, n*r.Interval)
		if r.monthOK(day) && r.dayOK(day) {
			days = append(days, day)
		}
	case "WEEKLY":
		offset := (int(start.Weekday()) - int(r.WeekStart) + 7) % 7
		weekStart := start.AddDate(0, 0, -offset+n*7*r.Interval)
		for i := 0; i < 7; i++ {
			day := weekStart.AddDate(0, 0, i)
			if r.monthOK(day) && (len(r.ByDay) == 0 && day.Weekday() == start.Weekday() || len(r.ByDay) > 0 && r.weekdayMatch(day, 0)) {
				days = append(days, day)
			}
		}
	case "MONTHLY":
		first := time.Date(start.Year(), start.Month()+time.Month(n*r.Interval), 1, start.Hour(), start.Minute(), start.Second(), 0, time.UTC)
		days = r.monthDays(first, start)
	case "YEARLY":
		year := start.Year() + n*r.Interval
		months := r.ByMonth
		if len(months) == 0 {
			months = []int{int(start.Month())}
		}
		for _, month := range months {
			first := time.Date(year, time.Month(month), 1, start.Hour(), start.Minute(), start.Second(), 0, time.UTC)
			days = append(days, r.monthDays(first, start)...)
		}
	}
	var out []time.Time
	hours, minutes := r.ByHour, r.ByMinute
	if len(hours) == 0 {
		hours = []int{start.Hour()}
	}
	if len(minutes) == 0 {
		minutes = []int{start.Minute()}
	}
	for _, day := range days {
		for _, h := range hours {
			for _, m := range minutes {
				out = append(out, time.Date(day.Year(), day.Month(), day.Day(), h, m, start.Second(), 0, time.UTC))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

func (r Rule) monthOK(day time.Time) bool {
	if len(r.ByMonth) == 0 {
		return true
	}
	for _, m := range r.ByMonth {
		if int(day.Month()) == m {
			return true
		}
	}
	return false
}

func (r Rule) dayOK(day time.Time) bool {
	if len(r.ByDay) > 0 && !r.weekdayMatch(day, 0) {
		return false
	}
	if len(r.ByMonthDay) > 0 {
		last := time.Date(day.Year(), day.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
		for _, d := range r.ByMonthDay {
			if d == day.Day() || d < 0 && last+d+1 == day.Day() {
				return true
			}
		}
		return false
	}
	return true
}

func (r Rule) weekdayMatch(day time.Time, _ int) bool {
	for _, w := range r.ByDay {
		if w.Day == day.Weekday() && w.Ordinal == 0 {
			return true
		}
	}
	return false
}

// monthDays lists a month's matching days: BYMONTHDAY, ordinal or plain BYDAY, or the start's day of month.
func (r Rule) monthDays(first, start time.Time) []time.Time {
	last := time.Date(first.Year(), first.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	var out []time.Time
	for d := 1; d <= last; d++ {
		day := time.Date(first.Year(), first.Month(), d, 0, 0, 0, 0, time.UTC)
		if len(r.ByMonthDay) == 0 && len(r.ByDay) == 0 {
			if d == start.Day() {
				out = append(out, day)
			}
			continue
		}
		if len(r.ByMonthDay) > 0 {
			matched := false
			for _, md := range r.ByMonthDay {
				if md == d || md < 0 && last+md+1 == d {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		if len(r.ByDay) > 0 {
			matched := false
			for _, w := range r.ByDay {
				if w.Day != day.Weekday() {
					continue
				}
				nth := (d-1)/7 + 1
				fromEnd := -((last-d)/7 + 1)
				if w.Ordinal == 0 || w.Ordinal == nth || w.Ordinal == fromEnd {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		out = append(out, day)
	}
	return out
}
