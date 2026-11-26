package scheduler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"nori/internal/modelkeys"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

func TestRecurringWorkStaysEnabledAfterModelAccessFailures(t *testing.T) {
	ctx := context.Background()
	backend := memstore.New()
	defer backend.Close()
	db := records.NewShared(backend, users.User{ID: "60af6e73-7554-4484-8ca7-3fa8890b27b4"}, 1)
	defer db.Close()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	s := &Service{Store: db, Now: func() time.Time { return now }}
	next := now.AddDate(0, 0, 1)
	a := stored{Automation: Automation{ID: "daily-check", Status: AutomationEnabled, NextAt: &next}}
	resource, err := state.NewResource(CollectionAutomations, a.ID, 0, a, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutResource(ctx, resource, 0); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []error{modelkeys.ErrMissingAPIKey, fmt.Errorf("%w Check billing.", modelkeys.ErrModelAccess), modelkeys.ErrMissingAPIKey} {
		s.recordOutcomeHealth(ctx, a.ID, OccurrenceFailed, failure.Error())
	}
	row, err := db.Resource(ctx, CollectionAutomations, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeAutomation(row)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != AutomationEnabled || got.NextAt == nil || !got.NextAt.Equal(next) || got.ConsecutiveFailures != 0 {
		t.Fatalf("provider key failures changed the routine: %+v", got)
	}
}
