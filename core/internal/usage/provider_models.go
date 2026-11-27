package usage

import "strings"

func OpenAICostWithWrites(model, tier string, input, output, total *int64, cached, written int64) (float64, error) {
	switch {
	case strings.HasPrefix(model, "gpt-6.1-terra"):
		return TerraCostWithWrites(model, tier, input, output, total, cached, written)
	case matchOpenAIModel(model, "gpt-6-luna"), matchOpenAIModel(model, "gpt-6.1-luna"), matchOpenAIModel(model, "gpt-6.2-luna"):
		return LunaCostWithWrites(model, tier, input, output, total, cached, written)
	default:
		return SolCostWithWrites(model, tier, input, output, total, cached, written)
	}
}
