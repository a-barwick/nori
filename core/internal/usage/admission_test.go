package usage

import (
	"context"
	"errors"
	"testing"
	"time"

	"nori/internal/state"
)

type fakeWindowReader struct {
	calls        int
	spendUSD     float64
	unknownCalls int
}

func (f *fakeWindowReader) UsageWindowSummary(_ context.Context, _, _ time.Time) (int, float64, int, error) {
	return f.calls, f.spendUSD, f.unknownCalls, nil
}

func TestWithAdmission_BlocksBeforeProviderIO(t *testing.T) {
	now := time.Date(2026, 11, 25, 18, 0, 0, 0, time.UTC)
	recorded := 0
	local := Func(func(_ context.Context, _ state.Usage) error {
		recorded++
		return nil
	})
	reader := &fakeWindowReader{calls: 10, spendUSD: 4.50, unknownCalls: 0}
	meter := WithAdmission(local, reader, 10, 10.0, func() time.Time { return now })

	err := meter.RecordUsage(context.Background(), state.Usage{ID: "u1", RequestStatus: "pending"})
	if !errors.Is(err, ErrUserQuotaExceeded) {
		t.Fatalf("expected ErrUserQuotaExceeded on hourly call cap, got %v", err)
	}
	if recorded != 0 {
		t.Fatalf("expected 0 local writes when blocked, got %d", recorded)
	}

	if err := meter.RecordUsage(context.Background(), state.Usage{ID: "u1", RequestStatus: "received"}); err != nil {
		t.Fatalf("expected terminal usage update to pass through, got %v", err)
	}
	if recorded != 1 {
		t.Fatalf("expected terminal usage write to succeed, got %d", recorded)
	}

	reader.calls = 2
	reader.spendUSD = 9.95
	reader.unknownCalls = 5
	err = meter.RecordUsage(context.Background(), state.Usage{ID: "u2", RequestStatus: "pending"})
	if !errors.Is(err, ErrUserQuotaExceeded) {
		t.Fatalf("expected ErrUserQuotaExceeded on daily spend cap with unknown exposure, got %v", err)
	}
}

func TestLocalInferenceUsesCallLimitWithoutCloudSpend(t *testing.T) {
	calls := 0
	reader := &fakeWindowReader{spendUSD: 20}
	meter := WithAdmission(Func(func(_ context.Context, u state.Usage) error {
		calls++
		if u.EstimatedExposureUSD != 0 {
			t.Fatalf("local request reserved cloud spend: %+v", u)
		}
		return nil
	}), reader, 1, 15, nil)
	record := state.Usage{ID: "home", CostStatus: "local", RequestStatus: "pending"}
	if err := meter.RecordUsage(context.Background(), record); err != nil || calls != 1 {
		t.Fatalf("local admission after cloud cap = %v, %d writes", err, calls)
	}
	reader.calls = 1
	if err := meter.RecordUsage(context.Background(), record); !errors.Is(err, ErrUserQuotaExceeded) || calls != 1 {
		t.Fatalf("local admission bypassed call limit: %v, %d writes", err, calls)
	}
}
