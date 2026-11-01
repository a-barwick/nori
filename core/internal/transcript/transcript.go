// Package transcript defines the messages exchanged between an inner task and its
// model: text, tool calls, tool results, and the opaque state a model provider needs
// back to continue its own turn. Task code, the workflow and storage use only these
// types. Request formats for a particular provider live in that provider's adapter
// (see internal/agent/openrouter).
package transcript

// Role says who produced a message.
type Role string

const (
	RoleUser  Role = "user"
	RoleModel Role = "model"
)

// Message is one turn of a transcript. Parts are listed before Role because that is
// the order of the saved JSON (see wire.go).
type Message struct {
	Parts []Part `json:"parts,omitempty"`
	Role  Role   `json:"role,omitempty"`
}

// Part is one element of a message. A part holds one kind of content. When a decoded
// part carries several, wire.go decides which one wins; Nori never writes that.
type Part struct {
	// Text is visible text from Austin or the model.
	Text string
	// ToolCall is a call the model asked Nori to run.
	ToolCall *ToolCall
	// ToolResult answers an earlier ToolCall.
	ToolResult *ToolResult
	// Provider is the model provider's own record of a model turn.
	Provider *ProviderState
}

// ToolCall is a request from the model to run one named tool.
type ToolCall struct {
	ID   string         `json:"id,omitempty"`
	Args map[string]any `json:"args,omitempty"`
	Name string         `json:"name,omitempty"`
}

// ToolResult is the outcome of a ToolCall, matched by CallID.
type ToolResult struct {
	CallID string         `json:"id,omitempty"`
	Name   string         `json:"name,omitempty"`
	Output map[string]any `json:"response,omitempty"`
}

// ProviderState is what a model provider returned for one turn, kept exactly so the
// provider can replay it (for example encrypted reasoning and call IDs). Nori never
// looks inside Data. Format names the adapter that wrote it, such as
// "nori.responses", so another adapter can tell it is not its own.
type ProviderState struct {
	Format string
	Data   []byte
}

// UserText returns a user message holding one text part.
func UserText(text string) Message {
	return Message{Role: RoleUser, Parts: []Part{{Text: text}}}
}

// ToolResultMessage returns a user message answering one tool call.
func ToolResultMessage(callID, name string, output map[string]any) Message {
	return Message{Role: RoleUser, Parts: []Part{{ToolResult: &ToolResult{CallID: callID, Name: name, Output: output}}}}
}

// ToolCalls lists the message's tool calls in order.
func (m Message) ToolCalls() []ToolCall {
	var calls []ToolCall
	for _, part := range m.Parts {
		if part.ToolCall != nil {
			calls = append(calls, *part.ToolCall)
		}
	}
	return calls
}

// Provider returns the message's provider state, if any.
func (m Message) Provider() *ProviderState {
	for _, part := range m.Parts {
		if part.Provider != nil {
			return part.Provider
		}
	}
	return nil
}
