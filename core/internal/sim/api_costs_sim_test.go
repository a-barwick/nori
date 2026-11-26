package sim_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"testing"
	"time"

	"nori/internal/connections"
	"nori/internal/state"
	"nori/internal/usage"
)

func TestAPICostsSimulation(t *testing.T) {
	world := newMemoryAPIWorld(t, 7301)
	ctx := context.Background()
	now := time.Date(2026, time.November, 1, 10, 5, 0, 0, time.UTC)
	world.clock.Set(now)
	world.alice.assistant.UsageNow = world.clock.Now
	world.bob.assistant.UsageNow = world.clock.Now

	prefs := connections.PreferencesEditable{Timezone: "America/Los_Angeles", Budgets: connections.Budgets{TaskUSD: 5}}
	if _, err := world.alice.assistant.Connections.SavePreferences(ctx, world.ids.Next(), 0, prefs); err != nil {
		t.Fatal(err)
	}
	read := func(userID, query string, status int) usage.CostReport {
		t.Helper()
		res := world.doJSON(t, userID, http.MethodGet, "/v1/settings/api-costs"+query, nil)
		if res.Code != status {
			t.Fatalf("%s returned %d, want %d: %s", query, res.Code, status, res.Body.String())
		}
		if status != http.StatusOK {
			return usage.CostReport{}
		}
		var report usage.CostReport
		if err := json.Unmarshal(res.Body.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		if res.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cost response must be private to the request")
		}
		return report
	}
	save := func(user *memoryAPIUserEnv, id, model, costStatus, requestStatus string, at time.Time, price *float64) {
		t.Helper()
		if err := user.store.RecordUsage(ctx, state.Usage{
			ID: id, Model: model, CostStatus: costStatus, RequestStatus: requestStatus,
			CostUSD: price, EstimatedExposureUSD: 5, CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	assertAmount := func(got usage.CostAmount, usd float64, priced, unpriced, local, pending int) {
		t.Helper()
		if math.Abs(got.CostUSD-usd) > 1e-12 || got.PricedCalls != priced ||
			got.UnpricedCalls != unpriced || got.LocalCalls != local || got.PendingCalls != pending {
			t.Fatalf("unexpected totals: %+v, want USD %g and counts %d/%d/%d/%d", got, usd, priced, unpriced, local, pending)
		}
	}
	midnight := time.Date(2026, time.November, 1, 7, 0, 0, 0, time.UTC)
	firstOne := midnight.Add(time.Hour)
	secondOne := firstOne.Add(time.Hour)
	save(world.alice, world.ids.Next(), "alpha", "provider", "received", midnight.Add(-time.Nanosecond), new(0.08))
	save(world.alice, world.ids.Next(), "alpha", "calculated", "received", firstOne.Add(-time.Nanosecond), new(0.004))
	save(world.alice, world.ids.Next(), "alpha", "provider", "received", firstOne.Add(30*time.Minute), new(0.30))
	save(world.alice, world.ids.Next(), "alpha", "calculated", "received", secondOne.Add(15*time.Minute), new(0.10))
	save(world.alice, world.ids.Next(), "beta", "unknown", "unknown", secondOne.Add(25*time.Minute), nil)
	save(world.alice, world.ids.Next(), "home", "local", "received", secondOne.Add(35*time.Minute), nil)
	pendingID := world.ids.Next()
	pendingAt := secondOne.Add(45 * time.Minute)
	save(world.alice, pendingID, "alpha", "unknown", "pending", pendingAt, nil)
	save(world.alice, world.ids.Next(), "alpha", "provider", "received", now, new(0.0))
	save(world.alice, world.ids.Next(), "alpha", "provider", "not_sent", now, new(88.0))
	save(world.alice, world.ids.Next(), "future", "provider", "received", now.Add(time.Nanosecond), new(99.0))
	save(world.bob, world.ids.Next(), "bob", "provider", "received", now, new(76.0))
	for i := range 105 {
		save(world.alice, world.ids.Next(), "alpha", "calculated", "received",
			midnight.AddDate(0, 0, -10).Add(time.Duration(i)*time.Minute), new(0.0004))
	}

	hourly := read(world.alice.user.ID, "?days=1", http.StatusOK)
	if hourly.Timezone != "America/Los_Angeles" || hourly.BucketSize != "hour" ||
		!hourly.StartAt.Equal(midnight) || !hourly.EndAt.Equal(midnight.Add(4*time.Hour)) || len(hourly.Buckets) != 4 {
		t.Fatalf("unexpected hourly window: %+v", hourly)
	}
	if !hourly.Buckets[1].StartAt.Equal(firstOne) || !hourly.Buckets[2].StartAt.Equal(secondOne) {
		t.Fatal("the two 1 am hours must have separate buckets")
	}
	assertAmount(hourly.Totals, 0.404, 4, 1, 1, 1)
	assertAmount(hourly.Buckets[0].Totals, 0.004, 1, 0, 0, 0)
	assertAmount(hourly.Buckets[1].Totals, 0.30, 1, 0, 0, 0)
	assertAmount(hourly.Buckets[2].Totals, 0.10, 1, 1, 1, 1)
	assertAmount(hourly.Buckets[3].Totals, 0, 1, 0, 0, 0)
	if len(hourly.Models) != 3 || hourly.Models[0].Model != "alpha" {
		t.Fatalf("unexpected model breakdown: %+v", hourly.Models)
	}
	assertAmount(hourly.Models[0].CostAmount, 0.404, 4, 0, 0, 1)
	save(world.alice, pendingID, "alpha", "provider", "received", pendingAt, new(0.06))

	week := read(world.alice.user.ID, "?days=7", http.StatusOK)
	if week.BucketSize != "day" || len(week.Buckets) != 7 || week.Buckets[6].EndAt.Sub(week.Buckets[6].StartAt) != 25*time.Hour {
		t.Fatalf("unexpected weekly window: %+v", week)
	}
	assertAmount(week.Buckets[5].Totals, 0.08, 1, 0, 0, 0)
	assertAmount(week.Buckets[6].Totals, 0.464, 5, 1, 1, 0)
	assertAmount(week.Totals, 0.544, 6, 1, 1, 0)
	month := read(world.alice.user.ID, "", http.StatusOK)
	if month.Days != 30 || len(month.Buckets) != 30 {
		t.Fatalf("unexpected default window: %+v", month)
	}
	assertAmount(month.Totals, 0.586, 111, 1, 1, 0)
	bob := read(world.bob.user.ID, "?days=7", http.StatusOK)
	assertAmount(bob.Totals, 76, 1, 0, 0, 0)
	if bob.Buckets[0].Models == nil || len(bob.Buckets[0].Models) != 0 {
		t.Fatalf("empty bucket models must be an empty array: %+v", bob.Buckets[0])
	}
	read(world.alice.user.ID, "?days=90", http.StatusBadRequest)
	read(world.alice.user.ID, "?days=1&days=7", http.StatusBadRequest)
}

func TestAPICostsHourBoundariesSimulation(t *testing.T) {
	for _, tc := range []struct {
		zone   string
		now    string
		starts []string
		end    string
	}{
		{
			zone: "Australia/Lord_Howe", now: "2026-04-04T15:45:00Z",
			starts: []string{"2026-04-04T13:00:00Z", "2026-04-04T14:00:00Z", "2026-04-04T15:00:00Z", "2026-04-04T15:30:00Z"},
			end:    "2026-04-04T16:30:00Z",
		},
		{
			zone: "America/Havana", now: "2026-03-08T06:05:00Z",
			starts: []string{"2026-03-08T05:00:00Z", "2026-03-08T06:00:00Z"},
			end:    "2026-03-08T07:00:00Z",
		},
	} {
		t.Run(tc.zone, func(t *testing.T) {
			world := newMemoryAPIWorld(t, 7302)
			now, err := time.Parse(time.RFC3339, tc.now)
			if err != nil {
				t.Fatal(err)
			}
			world.clock.Set(now)
			a := world.alice
			a.assistant.UsageNow = world.clock.Now
			prefs := connections.PreferencesEditable{Timezone: tc.zone, Budgets: connections.Budgets{TaskUSD: 5}}
			if _, err := a.assistant.Connections.SavePreferences(context.Background(), world.ids.Next(), 0, prefs); err != nil {
				t.Fatal(err)
			}
			res := world.doJSON(t, a.user.ID, http.MethodGet, "/v1/settings/api-costs?days=1", nil)
			if res.Code != http.StatusOK {
				t.Fatalf("cost report failed: %s", res.Body.String())
			}
			var report usage.CostReport
			if err := json.Unmarshal(res.Body.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if report.EndAt.Format(time.RFC3339) != tc.end || len(report.Buckets) != len(tc.starts) {
				t.Fatalf("unexpected window: %+v", report)
			}
			for i, start := range tc.starts {
				if got := report.Buckets[i].StartAt.Format(time.RFC3339); got != start {
					t.Errorf("bucket %d starts at %s, want %s", i, got, start)
				}
			}
		})
	}
}
