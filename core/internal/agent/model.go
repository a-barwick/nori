package agent

import (
	"context"

	"nori/internal/extensions"
	"nori/internal/transcript"
)

type Model interface {
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

// NativeWebSearchModel is optionally implemented by a Model whose provider
// executes web search natively (such as OpenAI's Responses API web_search tool).
type NativeWebSearchModel interface {
	SupportsNativeWebSearch() bool
}

type Request struct {
	Instructions    string
	Messages        []transcript.Message
	Tools           []transcript.Tool
	MaxOutputTokens int
	// Reasoning is the reasoning effort to ask for. Empty leaves the provider's default.
	Reasoning string
	// WebSearch enables the provider's native web search tool when supported.
	WebSearch bool
	// SingleTurn disables multi-turn encrypted reasoning state export (e.g. compaction/extract/dream).
	SingleTurn bool
}

// WebSearchCall records one provider-executed web search action (such as an OpenAI
// Responses API web_search_call output item) observed during a model turn.
type WebSearchCall struct {
	ID      string   `json:"id,omitempty"`
	Status  string   `json:"status,omitempty"`
	Action  string   `json:"action,omitempty"`
	Query   string   `json:"query,omitempty"`
	Queries []string `json:"queries,omitempty"`
	URL     string   `json:"url,omitempty"`
	Pattern string   `json:"pattern,omitempty"`
	Sources []string `json:"sources,omitempty"`
}

// URLCitation records a URL citation annotation emitted alongside model output text.
type URLCitation struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

type Response struct {
	// Message is the turn as it is saved in the transcript: the provider's own state, then
	// each tool call, then any visible text. Nil when the model returned nothing to save.
	Message *transcript.Message
	Calls   []transcript.ToolCall
	Answer  string
	// WebSearches lists provider-executed native web search actions from this turn.
	WebSearches []WebSearchCall `json:"web_searches,omitempty"`
	// Citations lists deduplicated URL citations from provider output annotations.
	Citations []URLCitation `json:"citations,omitempty"`
	// Usage is nil when the provider reported none. Cost is the reported or calculated USD
	// cost of this request, valid only when CostKnown is true.
	Usage     *Usage
	Cost      float64
	CostKnown bool
	// NotSent confirms the adapter stopped before dispatch, so missing cost is not
	// uncertain billing. False stays conservative for older saved activity results.
	NotSent bool
	// Tools is the definition snapshot this request offered. Calls are validated and pinned against it. It is not
	// serialized: workflow history keeps each call's pinned execution metadata instead.
	Tools *extensions.Snapshot `json:"-"`
}

// Usage counts tokens for one request. OutputTokens excludes ReasoningTokens.
type Usage struct {
	InputTokens     int32
	OutputTokens    int32
	ReasoningTokens int32
	CachedTokens    int32
	TotalTokens     int32
}

type InspectRequest struct {
	Instructions    string
	Question        string
	Image           []byte
	MediaType       string
	MaxOutputTokens int
}

type Inspection struct {
	Observation string
	// Sent is set once a request has left the process, so a missing cost means the request may
	// have been billed. Cost is the reported or calculated USD cost, valid only when CostKnown is true.
	Sent      bool
	Cost      float64
	CostKnown bool
}
