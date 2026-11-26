package transcript

type Role string

const (
	RoleUser  Role = "user"
	RoleModel Role = "model"
)

type Message struct {
	Parts []Part `json:"parts,omitempty"`
	Role  Role   `json:"role,omitempty"`
}

type Part struct {
	Text       string         `json:"text,omitempty"`
	ToolCall   *ToolCall      `json:"tool_call,omitempty"`
	ToolResult *ToolResult    `json:"tool_result,omitempty"`
	Provider   *ProviderState `json:"provider,omitempty"`
}

type ToolCall struct {
	ID   string         `json:"id,omitempty"`
	Args map[string]any `json:"args,omitempty"`
	Name string         `json:"name,omitempty"`
}

type ToolResult struct {
	CallID string         `json:"id,omitempty"`
	Name   string         `json:"name,omitempty"`
	Output map[string]any `json:"response,omitempty"`
}

type ProviderState struct {
	Format string `json:"format"`
	Data   []byte `json:"data"`
}

func UserText(text string) Message {
	return Message{Role: RoleUser, Parts: []Part{{Text: text}}}
}

func ToolResultMessage(callID, name string, output map[string]any) Message {
	return Message{Role: RoleUser, Parts: []Part{{ToolResult: &ToolResult{CallID: callID, Name: name, Output: output}}}}
}

func (m Message) ToolCalls() []ToolCall {
	var calls []ToolCall
	for _, part := range m.Parts {
		if part.ToolCall != nil {
			calls = append(calls, *part.ToolCall)
		}
	}
	return calls
}

func (m Message) Provider() *ProviderState {
	for _, part := range m.Parts {
		if part.Provider != nil {
			return part.Provider
		}
	}
	return nil
}
