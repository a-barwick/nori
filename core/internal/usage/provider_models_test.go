package usage

import (
	"math"
	"testing"
)

func int64Ptr(v int64) *int64 { return &v }

func TestOpenAICostWithWrites_LunaModels(t *testing.T) {
	models := []string{
		"gpt-6-luna",
		"gpt-6.1-luna",
		"gpt-6.2-luna",
		"gpt-6.1-luna-2026-10-01",
	}
	in, out, tot := int64(10_000), int64(2_000), int64(12_000)
	cached, written := int64(2_000), int64(1_000)
	// (10000 - 2000 - 1000)*100 + 2000*10 + 1000*125 + 2000*500
	// = 700_000 + 20_000 + 125_000 + 1_000_000 = 1_845_000 / 1e9 = 0.001845
	want := 0.001845

	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			got, err := OpenAICostWithWrites(model, "default", int64Ptr(in), int64Ptr(out), int64Ptr(tot), cached, written)
			if err != nil {
				t.Fatalf("OpenAICostWithWrites(%q) unexpected error: %v", model, err)
			}
			if math.Abs(got-want) > 1e-12 {
				t.Fatalf("OpenAICostWithWrites(%q) = %.9f, want %.9f", model, got, want)
			}
		})
	}
}

func TestOpenAICostWithWrites_LunaLongContextAndDispatch(t *testing.T) {
	// Long-context Luna (>272k input) doubles input/cache/write and 1.5x output.
	in, out, tot := int64(300_000), int64(10_000), int64(310_000)
	got, err := OpenAICostWithWrites("gpt-6.1-luna", "default", int64Ptr(in), int64Ptr(out), int64Ptr(tot), 50_000, 10_000)
	if err != nil {
		t.Fatalf("long-context luna error: %v", err)
	}
	// (300000 - 50000 - 10000)*200 + 50000*20 + 10000*250 + 10000*750
	// = 48_000_000 + 1_000_000 + 2_500_000 + 7_500_000 = 59_000_000 / 1e9 = 0.059
	if math.Abs(got-0.059) > 1e-12 {
		t.Fatalf("long-context luna = %.9f, want 0.059", got)
	}

	// Terra dispatch
	in2, out2, tot2 := int64(1_000), int64(500), int64(1_500)
	terra, err := OpenAICostWithWrites("gpt-6.1-terra", "default", int64Ptr(in2), int64Ptr(out2), int64Ptr(tot2), 0, 0)
	if err != nil {
		t.Fatalf("terra error: %v", err)
	}
	if math.Abs(terra-0.0035) > 1e-12 {
		t.Fatalf("terra = %.9f, want 0.0035", terra)
	}

	// Sol dispatch
	sol, err := OpenAICostWithWrites("gpt-6.2-sol", "default", int64Ptr(in2), int64Ptr(out2), int64Ptr(tot2), 0, 0)
	if err != nil {
		t.Fatalf("sol error: %v", err)
	}
	if math.Abs(sol-0.007) > 1e-12 {
		t.Fatalf("sol = %.9f, want 0.007", sol)
	}

	// EstimateCallUSD prices Luna well below Terra/Sol.
	lunaEst := EstimateCallUSD("openai", "gpt-6.1-luna", 10_000, 1024)
	terraEst := EstimateCallUSD("openai", "gpt-6.1-terra", 10_000, 1024)
	if !(lunaEst > 0 && lunaEst < terraEst) {
		t.Fatalf("expected 0 < lunaEst (%f) < terraEst (%f)", lunaEst, terraEst)
	}
}
