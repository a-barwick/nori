package conversation

import (
	"context"

	"nori/internal/state"
)

// IntentClassifier gives the reply model an advisory read of what the current inputs ask for. The hint never routes
// work or grants permission, and an unavailable classifier only means the reply model gets no hint.
type IntentClassifier interface {
	Classify(ctx context.Context, request ClassifyRequest) (Classification, error)
}

// ClassifyRequest carries one turn's inputs. The runner builds a fresh request for each call.
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
	Converse:     "Conversation, discussion, or unclassifiable input. Discussion alone does not request work.",
	Ask:          "Ask a question; answering may or may not require independent work.",
	RequestWork:  "Ask Nori to do independent work.",
	Steer:        "Change instructions for existing work.",
	ProvideInput: "Supply information or answer a question from existing work.",
	Decide:       "Give an approval or another requested decision.",
	Inspect:      "Ask about existing work or its outcome.",
	Control:      "Ask to stop or otherwise control existing work.",
	UpdateMemory: "Explicitly request a persistent memory change.",
	Mixed:        "The message combines multiple distinct intents; one label would omit part of it.",
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
