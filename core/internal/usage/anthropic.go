package usage

import (
	"errors"
	"strings"
)

// AnthropicCost calculates USD cost for an Anthropic Messages API response,
// including prompt cache reads (0.1x input) and ephemeral cache writes (1.25x input).
// In Anthropic's usage object, input_tokens is ONLY non-cached input tokens;
// cache_read_input_tokens and cache_creation_input_tokens are reported separately.
func AnthropicCost(model string, input, output, cacheRead, cacheWrite int64) (float64, error) {
	if input < 0 || output < 0 || cacheRead < 0 || cacheWrite < 0 {
		return 0, errors.New("Anthropic usage contains negative token counts")
	}
	inPerM, outPerM := anthropicTariff(model)
	if inPerM <= 0 || outPerM <= 0 {
		return 0, errors.New("unknown Anthropic model tariff")
	}
	cost := (float64(input)*inPerM +
		float64(cacheRead)*(inPerM*0.10) +
		float64(cacheWrite)*(inPerM*1.25) +
		float64(output)*outPerM) / 1e6
	return cost, nil
}

func anthropicTariff(model string) (inputPerMillion, outputPerMillion float64) {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(m, "opus"):
		return 15.0, 75.0
	case strings.Contains(m, "haiku"):
		return 0.80, 4.0
	case strings.Contains(m, "sonnet"), strings.HasPrefix(m, "claude-"):
		return 3.0, 15.0
	default:
		return 0, 0
	}
}
