package usage

import (
	"math"
	"testing"
)

func TestAnthropicCostOpus55AndHaiku55Tiers(t *testing.T) {
	// Opus 5.5: $4/M input, $20/M output, 0.05x cache read ($0.20/M), 1.25x cache write ($5/M).
	cost, err := AnthropicCost("anthropic/claude-opus-5.5", 1_000_000, 500_000, 1_000_000, 1_000_000)
	if err != nil {
		t.Fatalf("AnthropicCost opus-5.5: %v", err)
	}
	want := 4.0 + 10.0 + 0.20 + 5.0 // 19.20
	if math.Abs(cost-want) > 1e-9 {
		t.Fatalf("opus-5.5 cost=%v want=%v", cost, want)
	}

	// Haiku 5.5 <= 100k prompt tokens: $0.10/M input, $0.50/M output.
	lowTier, err := AnthropicCost("anthropic/claude-haiku-5.5", 60_000, 10_000, 40_000, 0)
	if err != nil {
		t.Fatalf("AnthropicCost haiku-5.5 low tier: %v", err)
	}
	wantLow := (60_000*0.10 + 40_000*0.01 + 10_000*0.50) / 1e6
	if math.Abs(lowTier-wantLow) > 1e-9 {
		t.Fatalf("haiku-5.5 low tier=%v want=%v", lowTier, wantLow)
	}

	// Haiku 5.5 > 100k prompt tokens: $0.50/M input, $2.50/M output.
	highTier, err := AnthropicCost("anthropic/claude-haiku-5.5", 60_001, 10_000, 40_000, 0)
	if err != nil {
		t.Fatalf("AnthropicCost haiku-5.5 high tier: %v", err)
	}
	wantHigh := (60_001*0.50 + 40_000*0.05 + 10_000*2.50) / 1e6
	if math.Abs(highTier-wantHigh) > 1e-9 {
		t.Fatalf("haiku-5.5 high tier=%v want=%v", highTier, wantHigh)
	}
}
