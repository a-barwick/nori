package conversation

import (
	"context"

	"nori/internal/prompts"
	"nori/internal/state"
)

// IntentClassifier gives the reply model an advisory read of what the current inputs ask for. The hint never routes
// work or grants permission, and an unavailable classifier only means the reply model gets no hint.
type IntentClassifier interface {
	Classify(ctx context.Context, request ClassifyRequest) (Classification, error)
}

type ClassifyRequest struct {
	// Inputs is the batch the turn owns, in acceptance order.
	Inputs []state.ConversationInput
	// History is the visible conversation before this batch, used only to resolve references.
	History []Message
	// RecordUsage saves a usage entry before the provider is called and again once the result is known.
	RecordUsage func(context.Context, state.Usage) error
}

type Intent string

const (
	Converse     Intent = "converse"
	Ask          Intent = "ask"
	RequestWork  Intent = "request_work"
	Steer        Intent = "steer"
	ProvideInput Intent = "provide_input"
	Decide       Intent = "decide"
	Inspect      Intent = "inspect"
	Control      Intent = "control"
	UpdateMemory Intent = "update_memory"
	Mixed        Intent = "mixed"
)

var intentDescriptions = map[Intent]string{
	Converse:     prompts.Runtime("intent-converse", nil),
	Ask:          prompts.Runtime("intent-ask", nil),
	RequestWork:  prompts.Runtime("intent-request-work", nil),
	Steer:        prompts.Runtime("intent-steer", nil),
	ProvideInput: prompts.Runtime("intent-provide-input", nil),
	Decide:       prompts.Runtime("intent-decide", nil),
	Inspect:      prompts.Runtime("intent-inspect", nil),
	Control:      prompts.Runtime("intent-control", nil),
	UpdateMemory: prompts.Runtime("intent-update-memory", nil),
	Mixed:        prompts.Runtime("intent-mixed", nil),
}

type Classification struct {
	Usage  state.Usage `json:"-"`
	Intent Intent      `json:"intent,omitempty"`
	// Confidence is the lowest Jev concentration statistic among selected flags,
	// not the probability that an intent is present. It never gates action.
	Confidence float64  `json:"confidence,omitempty"`
	Intents    []Intent `json:"intents,omitempty"`
	Available  bool     `json:"available"`
}
