package agent

import (
	"context"

	"nori/internal/transcript"
)

// Model is the inner task model. An adapter owns everything particular to one provider:
// endpoint, credentials, request and response formats, spend reservation and the text of
// its errors. The agent and task packages see only the types below.
type Model interface {
	// Provider and Name identify the model in traces and cost records, for example
	// "openrouter" and "openai/gpt-6.1-terra".
	Provider() string
	Name() string
	// Respond makes exactly one model request. The caller owns retries and budgets.
	// The Response carries billed usage and cost even when err is not nil, so an
	// unusable generation is still accounted for.
	Respond(ctx context.Context, req Request) (Response, error)
	// Inspect asks one question about one image in a request of its own that offers no tools, so
	// only the answer's text joins a task and text inside the image cannot call a tool. Like
	// Respond, it reports cost even when err is not nil.
	Inspect(ctx context.Context, req InspectRequest) (Inspection, error)
}

// Request is everything one model turn needs. Messages are the whole transcript so far.
type Request struct {
	Instructions    string
	Messages        []transcript.Message
	Tools           []transcript.Tool
	MaxOutputTokens int
	// Reasoning is the reasoning effort to ask for. Empty leaves the provider's default.
	Reasoning string
}

// Response is one model turn.
type Response struct {
	// Message is the turn as it is saved in the transcript: the provider's own state, then
	// each tool call, then any visible text. Nil when the model returned nothing to save.
	Message *transcript.Message
	Calls   []transcript.ToolCall
	// Answer is the visible text of a turn with no tool calls.
	Answer string
	// Usage is nil when the provider reported none. Cost is the provider-reported USD
	// cost of this request, valid only when CostKnown is true.
	Usage     *Usage
	Cost      float64
	CostKnown bool
}

// Usage counts tokens for one request. OutputTokens excludes ReasoningTokens.
type Usage struct {
	InputTokens     int32
	OutputTokens    int32
	ReasoningTokens int32
	CachedTokens    int32
	TotalTokens     int32
}

// InspectRequest asks a question about one PNG image.
type InspectRequest struct {
	Instructions    string
	Question        string
	Image           []byte
	MediaType       string
	MaxOutputTokens int
}

// Inspection is the answer to an InspectRequest.
type Inspection struct {
	Observation string
	// Sent is set once a request has left the process, so a missing cost means the request may
	// have been billed. Cost is the provider-reported USD cost, valid only when CostKnown is true.
	Sent      bool
	Cost      float64
	CostKnown bool
}
