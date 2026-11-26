package agent

import (
	"context"

	"nori/internal/extensions"
	"nori/internal/transcript"
)

type Model interface {
	Provider() string
	Name() string
	Respond(ctx context.Context, req Request) (Response, error)
	Inspect(ctx context.Context, req InspectRequest) (Inspection, error)
}

type NativeWebSearchModel interface {
	SupportsNativeWebSearch() bool
}

type Request struct {
	Instructions    string
	Messages        []transcript.Message
	Tools           []transcript.Tool
	MaxOutputTokens int
	Reasoning       string
	WebSearch       bool
	SingleTurn      bool
}

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

type URLCitation struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

type Response struct {
	Message     *transcript.Message
	Calls       []transcript.ToolCall
	Answer      string
	WebSearches []WebSearchCall `json:"web_searches,omitempty"`
	Citations   []URLCitation   `json:"citations,omitempty"`
	Usage       *Usage
	Cost        float64
	CostKnown   bool
	CostLocal   bool
	NotSent     bool
	Tools       *extensions.Snapshot `json:"-"`
}

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
	Sent        bool
	Cost        float64
	CostKnown   bool
}
