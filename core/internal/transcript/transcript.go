// Package transcript defines the messages exchanged between an inner task and its
// model: text, tool calls, tool results, and the opaque state a model provider needs
// back to continue its own turn. Task code, the workflow and storage use only these
// types. Request formats for a particular provider live in that provider's adapter
// (see internal/agent/responses).
package transcript

// Role says who produced a message.
type Role string

const (
	RoleUser  Role = "user"
	RoleModel Role = "model"
)

// Message is one turn of a transcript.
type Message struct {
	Parts []Part `json:"parts,omitempty"`
	Role  Role   `json:"role,omitempty"`
}

// Part is one element of a message, holding one kind of content.
type Part struct {
	// Text is visible text from the user or the model.
	Text string `json:"text,omitempty"`
	// ToolCall is a call the model asked Nori to run.
	ToolCall *ToolCall `json:"tool_call,omitempty"`
	// ToolResult answers an earlier ToolCall.
	ToolResult *ToolResult `json:"tool_result,omitempty"`
	// Provider is the model provider's own record of a model turn.
	Provider *ProviderState `json:"provider,omitempty"`
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
	Format string `json:"format"`
	Data   []byte `json:"data"`
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
