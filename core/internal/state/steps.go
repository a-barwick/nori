package state

import "time"

const TaskStepLimit = 100

// TaskStep records observed provider/tool activity. A missing cost or token
// count is unknown; no model reasoning is collected.
type TaskStep struct {
	ID             string      `json:"id" firestore:"id"`
	Kind           string      `json:"kind" firestore:"kind"`
	Status         string      `json:"status" firestore:"status"`
	Revision       int64       `json:"revision" firestore:"revision"`
	StartedAt      time.Time   `json:"started_at" firestore:"started_at"`
	FinishedAt     *time.Time  `json:"finished_at,omitempty" firestore:"finished_at,omitempty"`
	DurationMS     int64       `json:"duration_ms" firestore:"duration_ms"`
	Model          string      `json:"model,omitempty" firestore:"model,omitempty"`
	Name           string      `json:"name,omitempty" firestore:"name,omitempty"`
	InputTokens    *int32      `json:"input_tokens,omitempty" firestore:"input_tokens,omitempty"`
	OutputTokens   *int32      `json:"output_tokens,omitempty" firestore:"output_tokens,omitempty"`
	CachedTokens   *int32      `json:"cached_tokens,omitempty" firestore:"cached_tokens,omitempty"`
	ThinkingTokens *int32      `json:"thinking_tokens,omitempty" firestore:"thinking_tokens,omitempty"`
	CostUSD        *float64    `json:"cost_usd,omitempty" firestore:"cost_usd,omitempty"`
	CostStatus     string      `json:"cost_status" firestore:"cost_status"`
	Calls          []TraceCall `json:"calls,omitempty" firestore:"calls,omitempty"`
	Arguments      string      `json:"arguments,omitempty" firestore:"arguments,omitempty"`
	Result         string      `json:"result,omitempty" firestore:"result,omitempty"`
	Error          string      `json:"error,omitempty" firestore:"error,omitempty"`
}

type TraceCall struct {
	Name      string `json:"name" firestore:"name"`
	Arguments string `json:"arguments" firestore:"arguments"`
}
