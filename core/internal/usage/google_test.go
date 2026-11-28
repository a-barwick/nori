package usage

import (
	"context"
	"math"
	"testing"
	"time"

	"nori/internal/state/docstore"
)

func TestGoogleCostTariffs(t *testing.T) {
	cases := []struct {
		model   string
		input   int64
		output  int64
		cached  int64
		wantUSD float64
	}{
		{"gemini-4-carbon", 1_000_000, 100_000, 0, 6.50},
		{"google/gemini-4-carbon-2026", 1_000_000, 100_000, 500_000, 4.70},
		{"gemini-4-neon", 1_000_000, 100_000, 0, 4.875},
		{"google/gemini-4-neon", 1_000_000, 100_000, 500_000, 3.525},
		{"gemini-4-hydrogen", 1_000_000, 100_000, 0, 3.00},
		{"google/gemini-4-hydrogen", 1_000_000, 100_000, 500_000, 2.10},
	}
	for _, tc := range cases {
		got, err := GoogleCost(tc.model, tc.input, tc.output, tc.cached)
		if err != nil {
			t.Fatalf("GoogleCost(%q): %v", tc.model, err)
		}
		if math.Abs(got-tc.wantUSD) > 1e-9 {
			t.Fatalf("GoogleCost(%q) = %v, want %v", tc.model, got, tc.wantUSD)
		}
	}

	if got, err := GoogleCostWithReasoning("gemini-4-neon", 1_000_000, 50_000, 50_000, 0); err != nil || math.Abs(got-4.875) > 1e-9 {
		t.Fatalf("GoogleCostWithReasoning = %v, %v; want 4.875", got, err)
	}
	if _, err := GoogleCost("unknown-gemini", 10, 10, 0); err == nil {
		t.Fatal("expected error for unknown model")
	}
	if _, err := GoogleCost("gemini-4-neon", 10, 10, 20); err == nil {
		t.Fatal("expected error when cached > input")
	}
}

func TestSolCostWithWritesOutputCap8192(t *testing.T) {
	in := int64(1000)
	outOK := int64(8192)
	totOK := in + outOK
	if _, err := SolCostWithWrites("gpt-6.2-sol", "default", &in, &outOK, &totOK, 0, 0); err != nil {
		t.Fatalf("expected 8192 output tokens to be valid: %v", err)
	}
	outTooHigh := int64(8193)
	totTooHigh := in + outTooHigh
	if _, err := SolCostWithWrites("gpt-6.2-sol", "default", &in, &outTooHigh, &totTooHigh, 0, 0); err == nil {
		t.Fatal("expected 8193 output tokens to be rejected")
	}
}

func TestReadCostReportBreakdowns(t *testing.T) {
	now := time.Date(2026, 11, 27, 15, 0, 0, 0, time.UTC)
	c1, c2, c3 := 0.25, 0.013, 0.50
	rows := []docstore.UsageCostRecord{
		{CreatedAt: now.Add(-10 * time.Minute), Model: "google/gemini-4-neon", Billing: "hosted", Category: "llm", CostUSD: &c1, CostStatus: "provider", RequestStatus: "received"},
		{CreatedAt: now.Add(-5 * time.Minute), Model: "exa-search", Billing: "hosted", Category: "search", CostUSD: &c2, CostStatus: "calculated", RequestStatus: "received"},
		{CreatedAt: now.Add(-2 * time.Minute), Model: "claude-opus-5-5", Billing: "byok", Category: "llm", CostUSD: &c3, CostStatus: "calculated", RequestStatus: "received"},
		{CreatedAt: now.Add(-1 * time.Minute), Model: "inkling-mini-2", Billing: "local", Category: "llm", CostStatus: "local", RequestStatus: "received"},
		{CreatedAt: now.Add(-1 * time.Minute), Model: "stale", Billing: "hosted", Category: "llm", RequestStatus: "stale_aborted"},
	}
	report, err := ReadCostReport(context.Background(), func(_ context.Context, _, _ time.Time, visit func(docstore.UsageCostRecord)) error {
		for _, r := range rows {
			visit(r)
		}
		return nil
	}, 1, time.UTC, now)
	if err != nil {
		t.Fatalf("ReadCostReport: %v", err)
	}
	if math.Abs(report.ByBilling.Hosted.CostUSD-(c1+c2)) > 1e-9 || report.ByBilling.Hosted.PricedCalls != 2 {
		t.Fatalf("unexpected hosted breakdown: %+v", report.ByBilling.Hosted)
	}
	if math.Abs(report.ByBilling.BYOK.CostUSD-c3) > 1e-9 || report.ByBilling.BYOK.PricedCalls != 1 {
		t.Fatalf("unexpected byok breakdown: %+v", report.ByBilling.BYOK)
	}
	if report.ByBilling.Local.LocalCalls != 1 {
		t.Fatalf("unexpected local breakdown: %+v", report.ByBilling.Local)
	}
	if math.Abs(report.ByCategory.Search.CostUSD-c2) > 1e-9 || report.ByCategory.Search.PricedCalls != 1 {
		t.Fatalf("unexpected search category breakdown: %+v", report.ByCategory.Search)
	}
	if math.Abs(report.ByCategory.LLM.CostUSD-(c1+c3)) > 1e-9 || report.ByCategory.LLM.LocalCalls != 1 {
		t.Fatalf("unexpected llm category breakdown: %+v", report.ByCategory.LLM)
	}
}
