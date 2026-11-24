package responses

import (
	"nori/internal/state"
	"nori/internal/usage"
)

type responseUsage struct {
	Input        *int64   `json:"input_tokens"`
	Output       *int64   `json:"output_tokens"`
	Total        *int64   `json:"total_tokens"`
	Cost         *float64 `json:"cost"`
	InputDetails struct {
		Cached int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputDetails struct {
		Reasoning int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (c *Client) recordCost(record *state.Usage, model, tier string, u *responseUsage) (float64, bool) {
	if u == nil {
		return 0, false
	}
	record.InputTokens, record.OutputTokens, record.TotalTokens = u.Input, u.Output, u.Total
	if c.direct() {
		cost, err := usage.TerraCost(model, tier, u.Input, u.Output, u.Total, u.InputDetails.Cached)
		if err != nil {
			return 0, false
		}
		record.CostUSD, record.CostStatus = &cost, "calculated"
		return cost, true
	}
	if !usage.RecordReportedCost(record, u.Cost) {
		return 0, false
	}
	return *u.Cost, true
}
