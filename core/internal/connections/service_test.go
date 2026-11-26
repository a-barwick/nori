package connections

import (
	"context"
	"testing"
	"time"

	"nori/internal/secret"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

func TestSettleFailedAttemptPersistsReasonAfterAttemptLapses(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	store := records.NewShared(memstore.New(), users.User{
		ID:       "11111111-1111-4111-8111-111111111111",
		Email:    "owner@example.com",
		TimeZone: "UTC",
	}, 1)
	svc := New(store, secret.Unavailable{}, "https://nori.test/callback")
	svc.Now = func() time.Time { return now }

	rec := Record{
		ID:            "conn_expired",
		IntegrationID: "google",
		Title:         "Google",
		Status:        StatusConnecting,
		GrantedScopes: []string{},
	}
	res, err := state.NewResource(CollectionConnections, rec.ID, 1, rec, recordIndex(rec), now)
	if err != nil {
		t.Fatalf("NewResource: %v", err)
	}
	if _, err := store.PutResource(ctx, res, 0); err != nil {
		t.Fatalf("PutResource: %v", err)
	}

	// Advance beyond attemptLifetime so lapsed() would mutate StatusConnecting in memory.
	now = now.Add(attemptLifetime + time.Minute)

	const reason = "The sign-in link expired. Start again from Apps."
	svc.settleFailedAttempt(ctx, rec.ID, reason)

	raw, _, err := svc.rawRecord(ctx, rec.ID)
	if err != nil {
		t.Fatalf("rawRecord: %v", err)
	}
	if raw.Status != StatusNotConnected {
		t.Fatalf("persisted status = %q, want %q", raw.Status, StatusNotConnected)
	}
	if raw.Health.Message != reason {
		t.Fatalf("persisted health message = %q, want %q", raw.Health.Message, reason)
	}
}
