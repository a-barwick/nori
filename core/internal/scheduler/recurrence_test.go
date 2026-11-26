package scheduler_test

import (
	"slices"
	"testing"
	"time"

	"nori/internal/scheduler"
)

func mustRFC3339(t *testing.T, s string) time.Time {
	t.Helper()
	got, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return got.UTC()
}

func formatUTC(times []time.Time) []string {
	out := make([]string, len(times))
	for i, ts := range times {
		out[i] = ts.UTC().Format(time.RFC3339)
	}
	return out
}

func TestExpandRecurrenceRules(t *testing.T) {
	afterOrigin := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		localStart string
		zone       string
		rrule      string
		after      time.Time
		limit      int
		wantUTC    []string
	}{
		{
			name:       "daily_with_interval_and_rrule_prefix",
			localStart: "2026-01-10T09:00",
			zone:       "UTC",
			rrule:      "RRULE:FREQ=DAILY;INTERVAL=2;COUNT=3",
			after:      afterOrigin,
			limit:      10,
			wantUTC: []string{
				"2026-01-10T09:00:00Z",
				"2026-01-12T09:00:00Z",
				"2026-01-14T09:00:00Z",
			},
		},
		{
			name:       "weekly_byday_multiple_days",
			localStart: "2026-01-05T08:30",
			zone:       "UTC",
			rrule:      "FREQ=WEEKLY;BYDAY=MO,WE,FR",
			after:      afterOrigin,
			limit:      4,
			wantUTC: []string{
				"2026-01-05T08:30:00Z",
				"2026-01-07T08:30:00Z",
				"2026-01-09T08:30:00Z",
				"2026-01-12T08:30:00Z",
			},
		},
		{
			name:       "weekly_interval_2_wkst_monday",
			localStart: "2026-01-05T09:00",
			zone:       "UTC",
			rrule:      "FREQ=WEEKLY;INTERVAL=2;WKST=MO;BYDAY=SU,MO",
			after:      afterOrigin,
			limit:      4,
			wantUTC: []string{
				"2026-01-05T09:00:00Z",
				"2026-01-11T09:00:00Z",
				"2026-01-19T09:00:00Z",
				"2026-01-25T09:00:00Z",
			},
		},
		{
			name:       "weekly_interval_2_wkst_sunday",
			localStart: "2026-01-05T09:00",
			zone:       "UTC",
			rrule:      "FREQ=WEEKLY;INTERVAL=2;WKST=SU;BYDAY=SU,MO",
			after:      afterOrigin,
			limit:      4,
			wantUTC: []string{
				"2026-01-05T09:00:00Z",
				"2026-01-18T09:00:00Z",
				"2026-01-19T09:00:00Z",
				"2026-02-01T09:00:00Z",
			},
		},
		{
			name:       "monthly_byday_combined_with_bymonthday_friday_13th",
			localStart: "2026-01-01T10:00",
			zone:       "UTC",
			rrule:      "FREQ=MONTHLY;BYDAY=FR;BYMONTHDAY=13",
			after:      afterOrigin,
			limit:      3,
			wantUTC: []string{
				"2026-02-13T10:00:00Z",
				"2026-03-13T10:00:00Z",
				"2026-11-13T10:00:00Z",
			},
		},
		{
			name:       "monthly_negative_bymonthday_and_ordinal_byday_intersection",
			localStart: "2026-01-01T12:00",
			zone:       "UTC",
			rrule:      "FREQ=MONTHLY;BYMONTHDAY=-1;BYDAY=-1SA",
			after:      afterOrigin,
			limit:      3,
			wantUTC: []string{
				"2026-01-31T12:00:00Z",
				"2026-02-28T12:00:00Z",
				"2026-10-31T12:00:00Z",
			},
		},
		{
			name:       "daily_byday_combined_with_bymonthday",
			localStart: "2026-01-01T09:00",
			zone:       "UTC",
			rrule:      "FREQ=DAILY;BYDAY=TH,SU;BYMONTHDAY=1,-1",
			after:      afterOrigin,
			limit:      3,
			wantUTC: []string{
				"2026-01-01T09:00:00Z",
				"2026-02-01T09:00:00Z",
				"2026-03-01T09:00:00Z",
			},
		},
		{
			name:       "monthly_negative_bymonthday_february_leap_year",
			localStart: "2028-01-31T09:00",
			zone:       "UTC",
			rrule:      "FREQ=MONTHLY;BYMONTHDAY=-1",
			after:      afterOrigin,
			limit:      3,
			wantUTC: []string{
				"2028-01-31T09:00:00Z",
				"2028-02-29T09:00:00Z",
				"2028-03-31T09:00:00Z",
			},
		},
		{
			name:       "floating_until_interpreted_in_local_timezone",
			localStart: "2026-01-10T09:00",
			zone:       "America/New_York",
			rrule:      "FREQ=DAILY;UNTIL=20260112T090000",
			after:      afterOrigin,
			limit:      10,
			wantUTC: []string{
				"2026-01-10T14:00:00Z",
				"2026-01-11T14:00:00Z",
				"2026-01-12T14:00:00Z",
			},
		},
		{
			name:       "utc_until_excludes_later_same_local_day",
			localStart: "2026-01-10T09:00",
			zone:       "America/New_York",
			rrule:      "FREQ=DAILY;UNTIL=20260112T090000Z",
			after:      afterOrigin,
			limit:      10,
			wantUTC: []string{
				"2026-01-10T14:00:00Z",
				"2026-01-11T14:00:00Z",
			},
		},
		{
			name:       "date_only_until_includes_full_end_date",
			localStart: "2026-01-10T23:30",
			zone:       "America/New_York",
			rrule:      "FREQ=DAILY;UNTIL=20260111",
			after:      afterOrigin,
			limit:      10,
			wantUTC: []string{
				"2026-01-11T04:30:00Z",
				"2026-01-12T04:30:00Z",
			},
		},
		{
			name:       "dst_spring_forward_skipped_wall_time",
			localStart: "2026-03-07T02:30",
			zone:       "America/New_York",
			rrule:      "FREQ=DAILY",
			after:      afterOrigin,
			limit:      3,
			wantUTC: []string{
				"2026-03-07T07:30:00Z",
				"2026-03-08T07:00:00Z",
				"2026-03-09T06:30:00Z",
			},
		},
		{
			name:       "dst_spring_forward_normal_wall_time",
			localStart: "2026-03-07T09:00",
			zone:       "America/New_York",
			rrule:      "FREQ=DAILY",
			after:      afterOrigin,
			limit:      2,
			wantUTC: []string{
				"2026-03-07T14:00:00Z",
				"2026-03-08T13:00:00Z",
			},
		},
		{
			name:       "dst_fall_back_ambiguous_wall_time_runs_once_earlier",
			localStart: "2026-10-31T01:30",
			zone:       "America/New_York",
			rrule:      "FREQ=DAILY",
			after:      afterOrigin,
			limit:      3,
			wantUTC: []string{
				"2026-10-31T05:30:00Z",
				"2026-11-01T05:30:00Z",
				"2026-11-02T06:30:00Z",
			},
		},
		{
			name:       "strictly_after_cursor_excludes_exact_match",
			localStart: "2026-01-10T09:00",
			zone:       "UTC",
			rrule:      "FREQ=DAILY",
			after:      mustRFC3339(t, "2026-01-11T09:00:00Z"),
			limit:      2,
			wantUTC: []string{
				"2026-01-12T09:00:00Z",
				"2026-01-13T09:00:00Z",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scheduler.Expand(tc.localStart, tc.zone, tc.rrule, tc.after, tc.limit)
			if err != nil {
				t.Fatalf("Expand(%q, %q, %q): %v", tc.localStart, tc.zone, tc.rrule, err)
			}
			gotFmt := formatUTC(got)
			if !slices.Equal(gotFmt, tc.wantUTC) {
				t.Fatalf("Expand = %v, want %v", gotFmt, tc.wantUTC)
			}
		})
	}
}

func TestLocalTimeDSTTransitions(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}

	spring, adjusted := scheduler.LocalTime(2026, time.March, 8, 2, 30, 0, ny)
	if !adjusted {
		t.Fatalf("spring-forward gap expected adjusted=true, got false (instant=%s)", spring.UTC())
	}
	wantSpring := mustRFC3339(t, "2026-03-08T07:00:00Z")
	if !spring.Equal(wantSpring) {
		t.Fatalf("spring-forward instant = %s, want %s", spring.UTC(), wantSpring)
	}

	fall, adjusted := scheduler.LocalTime(2026, time.November, 1, 1, 30, 0, ny)
	if adjusted {
		t.Fatalf("fall-back repeat expected adjusted=false, got true")
	}
	wantFall := mustRFC3339(t, "2026-11-01T05:30:00Z")
	if !fall.Equal(wantFall) {
		t.Fatalf("fall-back instant = %s, want %s", fall.UTC(), wantFall)
	}
}

func TestParseRuleAndExpandRejectsInvalidInputs(t *testing.T) {
	invalidRules := []struct {
		name  string
		rrule string
	}{
		{"empty", ""},
		{"missing_freq", "INTERVAL=2;COUNT=5"},
		{"unsupported_freq_hourly", "FREQ=HOURLY"},
		{"unsupported_freq_minutely", "FREQ=MINUTELY"},
		{"malformed_part_no_equals", "FREQ=DAILY;INVALID"},
		{"unsupported_part_bysetpos", "FREQ=MONTHLY;BYDAY=MO;BYSETPOS=1"},
		{"unsupported_part_byyearday", "FREQ=YEARLY;BYYEARDAY=100"},
		{"interval_zero", "FREQ=DAILY;INTERVAL=0"},
		{"interval_negative", "FREQ=DAILY;INTERVAL=-1"},
		{"interval_too_large", "FREQ=DAILY;INTERVAL=1001"},
		{"interval_non_numeric", "FREQ=DAILY;INTERVAL=two"},
		{"byday_invalid_code", "FREQ=WEEKLY;BYDAY=XX"},
		{"byday_zero_ordinal", "FREQ=MONTHLY;BYDAY=0MO"},
		{"byday_ordinal_out_of_range_pos", "FREQ=MONTHLY;BYDAY=6MO"},
		{"byday_ordinal_out_of_range_neg", "FREQ=MONTHLY;BYDAY=-6FR"},
		{"bymonthday_zero", "FREQ=MONTHLY;BYMONTHDAY=0"},
		{"bymonthday_32", "FREQ=MONTHLY;BYMONTHDAY=32"},
		{"bymonthday_neg_32", "FREQ=MONTHLY;BYMONTHDAY=-32"},
		{"bymonth_zero", "FREQ=YEARLY;BYMONTH=0"},
		{"bymonth_13", "FREQ=YEARLY;BYMONTH=13"},
		{"byhour_24", "FREQ=DAILY;BYHOUR=24"},
		{"byhour_negative", "FREQ=DAILY;BYHOUR=-1"},
		{"byminute_60", "FREQ=DAILY;BYMINUTE=60"},
		{"count_zero", "FREQ=DAILY;COUNT=0"},
		{"count_negative", "FREQ=DAILY;COUNT=-5"},
		{"invalid_until", "FREQ=DAILY;UNTIL=not-a-date"},
		{"invalid_wkst", "FREQ=WEEKLY;WKST=MONDAY"},
	}

	for _, tc := range invalidRules {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := scheduler.ParseRule(tc.rrule); err == nil {
				t.Errorf("ParseRule(%q) = nil, want error", tc.rrule)
			}
		})
	}

	t.Run("expand_rejects_bad_timezone", func(t *testing.T) {
		if _, err := scheduler.Expand("2026-01-01T09:00", "Mars/Olympus_Mons", "FREQ=DAILY", time.Time{}, 1); err == nil {
			t.Fatalf("Expand accepted invalid timezone")
		}
	})

	t.Run("expand_rejects_local_start_with_offset", func(t *testing.T) {
		if _, err := scheduler.Expand("2026-01-01T09:00:00Z", "UTC", "FREQ=DAILY", time.Time{}, 1); err == nil {
			t.Fatalf("Expand accepted local_start with UTC offset")
		}
	})
}
