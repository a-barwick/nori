package usage

import (
	"errors"
	"strings"
)

func matchGoogleModel(model, base string) bool {
	return model == base || (strings.HasPrefix(model, base+"-") && len(model) > len(base)+1)
}

// GoogleTariff returns per-million-token input/output USD rates and the cached-input multiplier
// for configured Gemini 4 models (accepting both direct "gemini-4-*" and routed "google/gemini-4-*" IDs).
func GoogleTariff(model string) (inputPerMillion, outputPerMillion, cacheReadMult float64, ok bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimPrefix(m, "google/")
	switch {
	case matchGoogleModel(m, "gemini-4-carbon"):
		return 4.0, 25.0, 0.10, true
	case matchGoogleModel(m, "gemini-4-neon"):
		return 3.0, 18.75, 0.10, true
	case matchGoogleModel(m, "gemini-4-hydrogen"):
		return 2.0, 10.0, 0.10, true
	default:
		return 0, 0, 0, false
	}
}

// GoogleCost calculates USD cost for a Gemini 4 model call.
// input is total prompt tokens (including cached), output is total completion tokens
// (including reasoning tokens when reported together), and cached is cached prompt tokens.
func GoogleCost(model string, input, output, cached int64) (float64, error) {
	if input < 0 || output < 0 || cached < 0 || cached > input {
		return 0, errors.New("Google usage contains invalid token counts")
	}
	inPerM, outPerM, cacheReadMult, ok := GoogleTariff(model)
	if !ok {
		return 0, errors.New("unknown Google model tariff")
	}
	uncached := input - cached
	cost := (float64(uncached)*inPerM +
		float64(cached)*(inPerM*cacheReadMult) +
		float64(output)*outPerM) / 1e6
	return cost, nil
}

// GoogleCostWithReasoning calculates USD cost when non-reasoning output and reasoning
// output tokens are tracked separately (both billed at the model's output rate).
func GoogleCostWithReasoning(model string, input, output, reasoning, cached int64) (float64, error) {
	if reasoning < 0 {
		return 0, errors.New("Google usage contains invalid token counts")
	}
	return GoogleCost(model, input, output+reasoning, cached)
}
