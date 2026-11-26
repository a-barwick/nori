package usage

import (
	"errors"
	"strings"
)

func AnthropicCost(model string, input, output, cacheRead, cacheWrite int64) (float64, error) {
	if input < 0 || output < 0 || cacheRead < 0 || cacheWrite < 0 {
		return 0, errors.New("Anthropic usage contains negative token counts")
	}
	totalPrompt := input + cacheRead + cacheWrite
	inPerM, outPerM, cacheReadMult := anthropicTariff(model, totalPrompt)
	if inPerM <= 0 || outPerM <= 0 {
		return 0, errors.New("unknown Anthropic model tariff")
	}
	cost := (float64(input)*inPerM +
		float64(cacheRead)*(inPerM*cacheReadMult) +
		float64(cacheWrite)*(inPerM*1.25) +
		float64(output)*outPerM) / 1e6
	return cost, nil
}

func anthropicTariff(model string, totalPromptTokens int64) (inputPerMillion, outputPerMillion, cacheReadMult float64) {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimPrefix(m, "anthropic/")
	switch {
	case strings.Contains(m, "opus-5.5") || strings.Contains(m, "opus-5-5"):
		return 4.0, 20.0, 0.05
	case strings.Contains(m, "haiku-5.5") || strings.Contains(m, "haiku-5-5"):
		if totalPromptTokens > 100_000 {
			return 0.50, 2.50, 0.10
		}
		return 0.10, 0.50, 0.10
	case strings.Contains(m, "opus"):
		return 15.0, 75.0, 0.10
	case strings.Contains(m, "haiku"):
		return 0.80, 4.0, 0.10
	case strings.Contains(m, "sonnet"), strings.HasPrefix(m, "claude-"):
		return 3.0, 15.0, 0.10
	default:
		return 0, 0, 0
	}
}
