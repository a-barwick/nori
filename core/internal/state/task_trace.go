package state

import (
	"context"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
)

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

func (s *Google) RecordTaskStep(ctx context.Context, runID string, step TaskStep) error {
	_, err := s.runs().Doc(runID).Collection("steps").Doc(step.ID).Set(ctx, step)
	return err
}

// Detailed traces are fetched only when opened, not with every session poll.
func (s *Google) TaskSteps(ctx context.Context, runID string) ([]TaskStep, error) {
	if _, err := s.GetRun(ctx, runID); err != nil {
		return nil, err
	}
	iter := s.runs().Doc(runID).Collection("steps").OrderBy("started_at", firestore.Desc).Limit(100).Documents(ctx)
	defer iter.Stop()
	steps := []TaskStep{}
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		var step TaskStep
		if err := doc.DataTo(&step); err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	for i, j := 0, len(steps)-1; i < j; i, j = i+1, j-1 {
		steps[i], steps[j] = steps[j], steps[i]
	}
	return steps, nil
}
