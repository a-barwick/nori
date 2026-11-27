package usage

import "strings"

// EstimateCallUSD returns a conservative upper-bound USD estimate for a single
// outbound model call based on provider, model name, request payload size, and
// max output tokens.
func EstimateCallUSD(provider, model string, bodyBytes, maxOutputTokens int) float64 {
	if provider == "home" {
		return 0
	}
	inputRate, outputRate := 2.5, 10.0
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "gemini-4-carbon"):
		inputRate, outputRate = 5, 25
	case strings.Contains(m, "gemini-4-neon"):
		inputRate, outputRate = 3.75, 18.75
	case strings.Contains(m, "gemini-4-hydrogen"):
		inputRate, outputRate = 2.5, 10
	case strings.Contains(m, "opus-5.5"), strings.Contains(m, "opus-5-5"):
		inputRate, outputRate = 5, 20
	case strings.Contains(m, "opus"):
		inputRate, outputRate = 18.75, 75
	case strings.Contains(m, "claude"), provider == "anthropic":
		inputRate, outputRate = 3.75, 15
	case strings.Contains(m, "gpt-6.1-terra"):
		inputRate, outputRate = 2.5, 7.5
	case strings.Contains(m, "gpt-6-luna"), strings.Contains(m, "gpt-6.1-luna"), strings.Contains(m, "gpt-6.2-luna"):
		inputRate, outputRate = 0.2, 0.75
	case strings.Contains(m, "gpt-6"):
	case provider == "gateway", provider == "openrouter":
		inputRate, outputRate = 18.75, 75
	}
	output := maxOutputTokens
	if output <= 0 {
		output = 1024
	}
	return (float64(max(bodyBytes, 256))*inputRate + float64(output)*outputRate) / 1e6
}

// LegacyOpenAICost prices legacy GPT-4.1 series models used in local gateway
// tests and fixtures.
func LegacyOpenAICost(model string, input, output, cached int64) (float64, bool) {
	if input < 0 || output < 0 || cached < 0 || cached > input {
		return 0, false
	}
	low := strings.ToLower(model)
	switch {
	case strings.HasPrefix(low, "gpt-4.1-mini"):
		return (float64(max(input-cached, 0))*0.40 + float64(cached)*0.10 + float64(output)*1.60) / 1e6, true
	case strings.HasPrefix(low, "gpt-4.1-nano"):
		return (float64(max(input-cached, 0))*0.10 + float64(cached)*0.025 + float64(output)*0.40) / 1e6, true
	case strings.HasPrefix(low, "gpt-4.1"):
		return (float64(max(input-cached, 0))*2.00 + float64(cached)*0.50 + float64(output)*8.00) / 1e6, true
	default:
		return 0, false
	}
}
