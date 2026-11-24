package usage

import (
	"errors"
	"strings"
)

func matchOpenAIModel(model, name string) bool {
	return model == name || (strings.HasPrefix(model, name+"-") && len(model) > len(name)+1)
}

func SolCost(model, tier string, input, output, total *int64, cached int64) (float64, error) {
	if !matchOpenAIModel(model, "gpt-6.1-sol") || tier != "default" || input == nil || output == nil || total == nil ||
		*input < 0 || *input > 272_000 || *output < 0 || *output > 4096 || *total != *input+*output || cached < 0 || cached > *input {
		return 0, errors.New("OpenAI usage does not match the configured tariff")
	}
	return float64((*input-cached)*2000+cached*100+*output*10000) / 1e9, nil
}

func TerraCost(model, tier string, input, output, total *int64, cached int64) (float64, error) {
	if !matchOpenAIModel(model, "gpt-6.1-terra") || tier != "default" || input == nil || output == nil || total == nil ||
		*input < 0 || *input > 1_050_000 || *output < 0 || *output > 1_050_000 || *total != *input+*output || cached < 0 || cached > *input {
		return 0, errors.New("OpenAI usage does not match the configured tariff")
	}
	inputRate, cacheRate, outputRate := int64(1000), int64(50), int64(5000)
	if *input > 272_000 {
		inputRate, cacheRate, outputRate = 2000, 100, 7500
	}
	return float64((*input-cached)*inputRate+cached*cacheRate+*output*outputRate) / 1e9, nil
}
