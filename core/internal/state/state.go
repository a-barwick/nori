package state

import (
	"errors"
	"time"
)

const MaxMessageBytes = 8192

var ErrBusy = errors.New("task is not open")
var ErrDuplicate = errors.New("run ID already exists")
var ErrPaused = errors.New("task execution is paused")
var ErrNotFound = errors.New("not found")

type Control struct {
	Paused bool `json:"paused" firestore:"paused"`
}

func (c Control) Check() error {
	if c.Paused {
		return ErrPaused
	}
	return nil
}

const (
	MessageStatusCompleted  = "completed"
	MessageStatusFailed     = "failed"
	MessageStatusCancelled  = "cancelled"
	MessageStatusSuperseded = "superseded"
)

type MessageError struct {
	Code      string `json:"code" firestore:"code"`
	Message   string `json:"message" firestore:"message"`
	Retryable bool   `json:"retryable" firestore:"retryable"`
}

type Message struct {
	Status    string        `json:"status,omitempty" firestore:"status,omitempty"`
	ID        string        `json:"id" firestore:"id"`
	TurnID    string        `json:"turn_id,omitempty" firestore:"turn_id,omitempty"`
	RunID     string        `json:"run_id" firestore:"run_id"`
	Role      string        `json:"role" firestore:"role"`
	Text      string        `json:"text" firestore:"text"`
	Parts     []Part        `json:"parts,omitempty" firestore:"parts,omitempty"`
	ResultIDs []string      `json:"result_ids,omitempty" firestore:"result_ids,omitempty"`
	Error     *MessageError `json:"error,omitempty" firestore:"error,omitempty"`
	CreatedAt time.Time     `json:"created_at" firestore:"created_at"`
}

type Event struct {
	Kind      string    `json:"kind" firestore:"kind"`
	Text      string    `json:"text" firestore:"text"`
	CreatedAt time.Time `json:"created_at" firestore:"created_at"`
}

type Artifact struct {
	ID   string `json:"id" firestore:"id"`
	Kind string `json:"kind" firestore:"kind"`
}

const (
	UsageBillingHosted = "hosted"
	UsageBillingBYOK   = "byok"
	UsageBillingLocal  = "local"

	UsageCategoryLLM      = "llm"
	UsageCategorySearch   = "search"
	UsageCategoryComputer = "computer"
)

type Usage struct {
	ID                   string    `json:"id" firestore:"id"`
	InputID              string    `json:"input_id" firestore:"input_id"`
	Provider             string    `json:"provider" firestore:"provider"`
	Model                string    `json:"model" firestore:"model"`
	Billing              string    `json:"billing,omitempty" firestore:"billing,omitempty"`
	Category             string    `json:"category,omitempty" firestore:"category,omitempty"`
	InputTokens          *int64    `json:"input_tokens,omitempty" firestore:"input_tokens,omitempty"`
	OutputTokens         *int64    `json:"output_tokens,omitempty" firestore:"output_tokens,omitempty"`
	TotalTokens          *int64    `json:"total_tokens,omitempty" firestore:"total_tokens,omitempty"`
	CachedTokens         *int64    `json:"cached_tokens,omitempty" firestore:"cached_tokens,omitempty"`
	CacheWriteTokens     *int64    `json:"cache_write_tokens,omitempty" firestore:"cache_write_tokens,omitempty"`
	CostUSD              *float64  `json:"cost_usd,omitempty" firestore:"cost_usd,omitempty"`
	EstimatedExposureUSD float64   `json:"estimated_exposure_usd,omitempty" firestore:"estimated_exposure_usd,omitempty"`
	CostStatus           string    `json:"cost_status" firestore:"cost_status"`
	RequestStatus        string    `json:"request_status" firestore:"request_status"`
	CreatedAt            time.Time `json:"created_at" firestore:"created_at"`
}

type Session struct {
	Messages []Message `json:"messages"`
	Runs     []Run     `json:"runs"`
	Paused   bool      `json:"paused"`
	Usage    []Usage   `json:"usage,omitempty"`
}

const (
	SessionMessageLimit = 100
	SessionRunLimit     = 30
	SessionUsageLimit   = 100
)

func MergeOpenRuns(recent, open []Run) []Run {
	merged := append([]Run{}, recent...)
	seen := map[string]bool{}
	for _, run := range merged {
		seen[run.ID] = true
	}
	for _, run := range open {
		if !seen[run.ID] {
			merged = append(merged, run)
		}
	}
	sortRunsNewestFirst(merged)
	return merged
}

func Chronological[T any](newestFirst []T) []T {
	for i, j := 0, len(newestFirst)-1; i < j; i, j = i+1, j-1 {
		newestFirst[i], newestFirst[j] = newestFirst[j], newestFirst[i]
	}
	return newestFirst
}

type SpendUpdate struct {
	RunID       string
	Spent       float64
	Budget      float64
	CostUnknown bool
}

func MergeSpend(run Run, update SpendUpdate) (spent, budget float64, costUnknown bool) {
	return max(run.Spent, update.Spent), max(run.Budget, update.Budget), run.CostUnknown || update.CostUnknown
}
