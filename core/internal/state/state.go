// Package state defines Nori's durable records, the contracts a storage backend implements, and the rules that decide
// what those records may become.
//
// The package is backend-neutral and imports no database or cloud SDK. A backend (for example internal/state/postgres)
// provides atomic reads and writes plus queries, and calls the pure rules in this package inside its transactions.
// Compose joins a document backend with an ArtifactStore into the ConversationStore that the rest of Nori uses.
//
// The firestore struct tags on the record types are part of the saved document layout. They name stored fields and
// import nothing, so they stay here with the types they describe.
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

// Check fences execution without tying durable tasks to a temporary session.
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

// MessageError carries structured error metadata for a failed assistant reply.
// It never contains raw provider error strings or HTTP status codes.
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

// Artifact is the record of a private artifact on a run. Its bytes live in an ArtifactStore.
type Artifact struct {
	ID   string `json:"id" firestore:"id"`
	Kind string `json:"kind" firestore:"kind"`
}

// Usage records one provider request independently from the inner task budget.
// A nil cost is unknown, never a free request. IDs are stable across writes.
type Usage struct {
	ID           string `json:"id" firestore:"id"`
	InputID      string `json:"input_id" firestore:"input_id"`
	Provider     string `json:"provider" firestore:"provider"`
	Model        string `json:"model" firestore:"model"`
	InputTokens  *int64 `json:"input_tokens,omitempty" firestore:"input_tokens,omitempty"`
	OutputTokens *int64 `json:"output_tokens,omitempty" firestore:"output_tokens,omitempty"`
	TotalTokens  *int64 `json:"total_tokens,omitempty" firestore:"total_tokens,omitempty"`
	// CachedTokens and CacheWriteTokens are the provider's prompt-cache reads and writes, when it reports them.
	CachedTokens     *int64    `json:"cached_tokens,omitempty" firestore:"cached_tokens,omitempty"`
	CacheWriteTokens *int64    `json:"cache_write_tokens,omitempty" firestore:"cache_write_tokens,omitempty"`
	CostUSD          *float64  `json:"cost_usd,omitempty" firestore:"cost_usd,omitempty"`
	CostStatus       string    `json:"cost_status" firestore:"cost_status"`
	RequestStatus    string    `json:"request_status" firestore:"request_status"`
	CreatedAt        time.Time `json:"created_at" firestore:"created_at"`
}

// Session is the bounded projection that clients and the outer loop read.
type Session struct {
	Messages []Message `json:"messages"`
	Runs     []Run     `json:"runs"`
	Paused   bool      `json:"paused"`
	Usage    []Usage   `json:"usage,omitempty"`
}

// Page sizes for the bounded session projection. Every backend returns the same window.
const (
	SessionMessageLimit = 100
	SessionRunLimit     = 30
	SessionUsageLimit   = 100
)

// MergeOpenRuns adds every open run to the recent runs, so a waiting task stays visible after thirty newer tasks
// have started, and orders the result newest first.
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

// SpendUpdate is a cumulative cost projection for one run. Retries and late arrivals are harmless because
// MergeSpend only moves the stored values up.
type SpendUpdate struct {
	RunID       string
	Spent       float64
	Budget      float64
	CostUnknown bool
}

// MergeSpend returns the run's cost fields after applying update. Spend is a fact about money already used, so it is
// recorded even after a correction or a terminal state.
func MergeSpend(run Run, update SpendUpdate) (spent, budget float64, costUnknown bool) {
	return max(run.Spent, update.Spent), max(run.Budget, update.Budget), run.CostUnknown || update.CostUnknown
}
