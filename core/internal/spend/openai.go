package spend

// SolCost uses the selected standard-tier GPT-6.1 Sol tariff: $2/M input,
// $0.10/M cached input and $10/M total output (including reasoning). Requests
// disable implicit cache writes and stay below the 272K long-context threshold.
// OpenAI reports tokens, not a dollar cost. Missing or incompatible usage stays
// unknown, so the caller retains the entire reservation.
func SolCost(model, tier string, input, output, total *int64, cached int64) (float64, error) {
	if model != "gpt-6.1-sol" || tier != "default" || input == nil || output == nil || total == nil ||
		*input < 0 || *input > 272_000 || *output < 0 || *output > 4096 || *total != *input+*output || cached < 0 || cached > *input {
		return 0, Blocked("OpenAI usage or pricing is unverified; reservation retained")
	}
	return float64((*input-cached)*2000+cached*100+*output*10000) / 1e9, nil
}
