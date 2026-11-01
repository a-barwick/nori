package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Saved transcripts were first written as Google genai Content JSON, and the task history
// chain keeps those blocks as immutable bytes. This file is where that compatibility lives.
// A part is written exactly as genai wrote it, so blocks stay readable by older workers
// and by this code, and no migration is needed:
//
//	text            {"text":"..."}
//	tool call       {"functionCall":{"id":"...","args":{...},"name":"..."}}
//	tool result     {"functionResponse":{"id":"...","name":"...","response":{...}}}
//	provider state  {"thought":true,"thoughtSignature":"<base64 of format + ":" + data>"}
//
// The provider form is the Responses marker that already existed: format "nori.responses"
// is the prefix "nori.responses:" that saved blocks carry before the provider items.
//
// Reading accepts more than Nori writes. Blocks from the Gemini era hold thought text,
// binary thought signatures and other genai fields, which carry nothing the task model
// replays, so they decode to an empty part. Inline media was never stored and fails the
// read, matching the old refusal to replay it to the task model.
type wirePart struct {
	FileData         json.RawMessage `json:"fileData,omitempty"`
	FunctionCall     *ToolCall       `json:"functionCall,omitempty"`
	FunctionResponse *ToolResult     `json:"functionResponse,omitempty"`
	InlineData       json.RawMessage `json:"inlineData,omitempty"`
	Text             string          `json:"text,omitempty"`
	Thought          bool            `json:"thought,omitempty"`
	ThoughtSignature []byte          `json:"thoughtSignature,omitempty"`
}

const (
	providerPrefix = "nori."
	maxFormatBytes = 64
)

func (p Part) MarshalJSON() ([]byte, error) {
	w := wirePart{FunctionCall: p.ToolCall, FunctionResponse: p.ToolResult, Text: p.Text}
	if p.Provider != nil {
		signature := append([]byte(p.Provider.Format+":"), p.Provider.Data...)
		w.Thought, w.ThoughtSignature = true, signature
	}
	return json.Marshal(w)
}

func (p *Part) UnmarshalJSON(data []byte) error {
	var w wirePart
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*p = Part{}
	if len(w.InlineData) > 0 || len(w.FileData) > 0 {
		return errors.New("transcript holds inline media, which the task model does not accept")
	}
	// Provider state wins over every other field, as the saved marker always did.
	if format, state, ok := splitProvider(w.ThoughtSignature); ok {
		p.Provider = &ProviderState{Format: format, Data: state}
		return nil
	}
	switch {
	case w.FunctionCall != nil:
		p.ToolCall = w.FunctionCall
	case w.FunctionResponse != nil:
		p.ToolResult = w.FunctionResponse
	case w.Text != "" && !w.Thought:
		p.Text = w.Text
	}
	return nil
}

func splitProvider(signature []byte) (format string, data []byte, ok bool) {
	if !bytes.HasPrefix(signature, []byte(providerPrefix)) {
		return "", nil, false
	}
	end := bytes.IndexByte(signature, ':')
	if end < 0 || end > maxFormatBytes {
		return "", nil, false
	}
	return string(signature[:end]), signature[end+1:], true
}
