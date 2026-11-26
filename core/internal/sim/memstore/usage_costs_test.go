package memstore_test

import (
	"context"
	"testing"
	"time"

	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/usage"
	"nori/internal/users"
)

func runUsageCostConformance(t *testing.T, backend docstore.Backend) {
	t.Run("UsageCostHistory", func(t *testing.T) {
		ctx := context.Background()
		alice := users.User{ID: "d5ba05fe-03ec-4982-8011-c4e56fcc2085", Email: "costs-one@example.com", TimeZone: "UTC"}
		bob := users.User{ID: "aca56bea-4b42-47eb-aa71-bb55995607d1", Email: "costs-two@example.com", TimeZone: "UTC"}
		directory := records.NewSharedUsers(backend)
		for _, user := range []users.User{alice, bob} {
			if err := directory.Create(ctx, user); err != nil {
				t.Fatal(err)
			}
		}
		aliceDB := records.NewShared(backend, alice, 1)
		bobDB := records.NewShared(backend, bob, 1)
		boundary := time.Date(2026, time.November, 25, 1, 0, 0, 0, time.UTC)
		aUsage := state.Usage{ID: "same-call-id", Model: "a", CreatedAt: boundary.Add(-time.Nanosecond),
			CostUSD: new(0.004), CostStatus: "provider", RequestStatus: "received"}
		bUsage := state.Usage{ID: aUsage.ID, Model: "b", CreatedAt: boundary,
			CostUSD: new(1.0), CostStatus: "calculated", RequestStatus: "received"}
		if err := aliceDB.RecordUsage(ctx, aUsage); err != nil {
			t.Fatal(err)
		}
		if err := bobDB.RecordUsage(ctx, bUsage); err != nil {
			t.Fatal(err)
		}
		report := func(store *records.Database) usage.CostReport {
			t.Helper()
			result, err := usage.ReadCostReport(ctx, store.ReadUsageCosts, 1, time.UTC, boundary)
			if err != nil {
				t.Fatal(err)
			}
			return result
		}
		got := report(aliceDB)
		if got.Totals.CostUSD != 0.004 || got.Buckets[0].Totals.PricedCalls != 1 || got.Buckets[1].Totals.PricedCalls != 0 {
			t.Fatalf("nanosecond bucketing or owner scope failed: %+v", got)
		}
		if got := report(bobDB); got.Totals.CostUSD != 1 || got.Buckets[1].Totals.PricedCalls != 1 {
			t.Fatalf("owner scope or current-instant inclusion failed: %+v", got)
		}

		if reset, ok := backend.(docstore.UserDataResetter); ok {
			if err := reset.BeginUserDataReset(ctx, alice.ID); err != nil {
				t.Fatal(err)
			}
			if err := reset.FinishUserDataReset(ctx, alice.ID, alice.DataOwner()); err != nil {
				t.Fatal(err)
			}
			fresh, err := directory.Get(ctx, alice.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := report(records.NewShared(backend, fresh, 1)); len(got.Models) != 0 {
				t.Fatalf("reset account retained model costs: %+v", got)
			}
			if got := report(bobDB); got.Totals.CostUSD != 1 {
				t.Fatalf("reset affected another account: %+v", got)
			}
		}
	})
}
