package scheduler

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"nori/internal/homeai"
	"nori/internal/llmguard"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/usage"
	"nori/internal/users"
)

func TestIsTransientAutomationFailure(t *testing.T) {
	transientCases := []string{
		"provider failed: HTTP 429 Too Many Requests",
		"upstream returned HTTP 500 internal error",
		"gateway error: HTTP 502 bad gateway",
		"service unavailable (HTTP 503)",
		"timeout: HTTP 504 gateway timeout",
		"anthropic overloaded: HTTP 529",
		usage.ErrUserQuotaExceeded.Error(),
		llmguard.ErrRateLimited.Error(),
		llmguard.ErrCircuitOpen.Error(),
		context.DeadlineExceeded.Error(),
		homeai.ErrOffline.Error(),
		"task model request interrupted or unavailable",
	}
	for _, msg := range transientCases {
		if !isTransientAutomationFailure(msg) {
			t.Errorf("expected %q to be classified as transient", msg)
		}
	}

	nonTransientCases := []string{
		"bad request: HTTP 400 invalid schema",
		"not found: HTTP 404 resource missing",
		"unauthorized: HTTP 401 invalid token",
		"tool execution failed: missing required parameter",
		"HTTP 5000 not a real status code",
	}
	for _, msg := range nonTransientCases {
		if isTransientAutomationFailure(msg) {
			t.Errorf("expected %q NOT to be classified as transient", msg)
		}
	}
}

func TestFinishDoesNotAdvanceCheckpointWhenSettleFails(t *testing.T) {
	ctx := context.Background()
	backend := memstore.New()
	defer backend.Close()
	db := records.NewShared(backend, users.User{ID: "60af6e73-7554-4484-8ca7-3fa8890b27b4"}, 1)
	defer db.Close()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	s := &Service{Store: db, Now: func() time.Time { return now }}

	a := stored{
		Automation: Automation{
			ID:     "monitor-1",
			Title:  "Monitor",
			Status: AutomationEnabled,
		},
		Checkpoint: "old finding",
	}
	autoRes, err := state.NewResource(CollectionAutomations, a.ID, 0, a, map[string]string{"status": string(a.Status)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutResource(ctx, autoRes, 0); err != nil {
		t.Fatal(err)
	}

	occ := occurrenceDoc{
		Occurrence: Occurrence{
			ID:           "occ-1",
			AutomationID: a.ID,
			Status:       OccurrenceRunning,
		},
		Monitor: true,
	}
	occRes, err := state.NewResource(CollectionOccurrences, occ.ID, 0, occ, map[string]string{"automation_id": a.ID, "status": string(occ.Status)}, now)
	if err != nil {
		t.Fatal(err)
	}
	saved1, err := db.PutResource(ctx, occRes, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Advance occurrence revision to simulate a concurrent settle/conflict.
	if _, err := db.PutResource(ctx, occRes, saved1.Revision); err != nil {
		t.Fatal(err)
	}

	staleOccRes := occRes
	staleOccRes.Revision = saved1.Revision // stale revision forces settle() to fail with ErrRevisionConflict

	err = s.finish(ctx, staleOccRes, occ, a, state.Run{ID: "run-1", Status: state.RunCompleted, Result: "new finding"}, OccurrenceCompleted)
	if !errors.Is(err, state.ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict from finish, got %v", err)
	}

	row, err := db.Resource(ctx, CollectionAutomations, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeAutomation(row)
	if err != nil {
		t.Fatal(err)
	}
	if got.Checkpoint != "old finding" {
		t.Fatalf("expected checkpoint to stay %q when settle fails, got %q", "old finding", got.Checkpoint)
	}
}

func TestRecordOutcomeHealthRetriesOnConflict(t *testing.T) {
	ctx := context.Background()
	backend := memstore.New()
	defer backend.Close()
	db := records.NewShared(backend, users.User{ID: "60af6e73-7554-4484-8ca7-3fa8890b27b4"}, 1)
	defer db.Close()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	s := &Service{Store: db, Now: func() time.Time { return now }}

	a := stored{Automation: Automation{ID: "auto-health", Status: AutomationEnabled}}
	autoRes, err := state.NewResource(CollectionAutomations, a.ID, 0, a, map[string]string{"status": string(a.Status)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutResource(ctx, autoRes, 0); err != nil {
		t.Fatal(err)
	}

	// Non-transient failure increments consecutive failures cleanly.
	if err := s.recordOutcomeHealth(ctx, a.ID, OccurrenceFailed, "HTTP 400 invalid tool input"); err != nil {
		t.Fatalf("recordOutcomeHealth failed: %v", err)
	}
	// Transient 529 failure must NOT increment consecutive failures.
	if err := s.recordOutcomeHealth(ctx, a.ID, OccurrenceFailed, fmt.Sprintf("anthropic: HTTP 529 overloaded")); err != nil {
		t.Fatalf("recordOutcomeHealth transient failed: %v", err)
	}
	row, err := db.Resource(ctx, CollectionAutomations, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeAutomation(row)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConsecutiveFailures != 1 {
		t.Fatalf("expected ConsecutiveFailures=1, got %d", got.ConsecutiveFailures)
	}
}
