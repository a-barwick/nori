package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/genai"
	"nori/internal/spend"
)

// Keep the existing durable Content envelope so saved tasks do not need a
// migration. Opaque Responses items preserve encrypted reasoning and call IDs.
const responsesMarker = "nori.responses:"

func responseInput(contents []*genai.Content) ([]any, error) {
	input := []any{}
	pending := map[string][]string{}
	for i, content := range contents {
		if content == nil {
			continue
		}
		restored := false
		for _, part := range content.Parts {
			if part != nil && bytes.HasPrefix(part.ThoughtSignature, []byte(responsesMarker)) {
				var items []json.RawMessage
				if json.Unmarshal(part.ThoughtSignature[len(responsesMarker):], &items) != nil {
					return nil, errors.New("invalid saved model response")
				}
				for _, item := range items {
					input = append(input, item)
				}
				restored = true
				break
			}
		}
		if restored {
			continue
		}
		for j, part := range content.Parts {
			if part == nil {
				continue
			}
			switch {
			case part.InlineData != nil || part.FileData != nil:
				return nil, errors.New("task model accepts text and artifact references, not inline media")
			case part.FunctionCall != nil:
				call := part.FunctionCall
				id := call.ID
				if id == "" {
					id = fmt.Sprintf("legacy_call_%d_%d", i, j)
				}
				pending[call.Name] = append(pending[call.Name], id)
				args, err := json.Marshal(call.Args)
				if err != nil {
					return nil, err
				}
				input = append(input, map[string]any{"type": "function_call", "call_id": id, "name": call.Name, "arguments": string(args)})
			case part.FunctionResponse != nil:
				result := part.FunctionResponse
				id := result.ID
				if ids := pending[result.Name]; len(ids) > 0 {
					if id == "" {
						id = ids[0]
					}
					pending[result.Name] = ids[1:]
				}
				if id == "" {
					return nil, errors.New("saved tool response has no call identity")
				}
				output, err := json.Marshal(result.Response)
				if err != nil {
					return nil, err
				}
				input = append(input, map[string]any{"type": "function_call_output", "call_id": id, "output": string(output)})
			case part.Text != "" && !part.Thought:
				role := content.Role
				if role == "model" {
					role = "assistant"
				}
				input = append(input, map[string]any{"role": role, "content": part.Text})
			}
		}
	}
	return input, nil
}

func (s *Service) respond(ctx context.Context, contents []*genai.Content, wait bool) (decision Decision, err error) {
	if s.ModelName != "openai/gpt-6.1-terra" {
		return decision, errors.New("task model must be the selected openai/gpt-6.1-terra")
	}
	input, err := responseInput(contents)
	if err != nil {
		return decision, err
	}
	config := modelConfig(wait)
	if enabled, _ := ctx.Value(completionProtocolKey{}).(bool); enabled {
		config = completionModelConfig(wait)
	}
	tools := []any{}
	for _, fn := range config.Tools[0].FunctionDeclarations {
		parameters := responseSchema(fn.Parameters)
		if parameters == nil {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false}
		}
		tools = append(tools, map[string]any{"type": "function", "name": fn.Name, "description": fn.Description, "strict": true, "parameters": parameters})
	}
	control, limit, err := spend.Prepare(ctx, "openrouter", modelOutputTokenLimit)
	if err != nil {
		return decision, err
	}
	defer control.Close()
	body, err := json.Marshal(map[string]any{
		"model": s.ModelName, "input": input, "instructions": config.SystemInstruction.Parts[0].Text,
		"tools": tools, "max_output_tokens": limit, "store": false,
		"include":  []string{"reasoning.encrypted_content"},
		"provider": map[string]any{"max_price": map[string]float64{"prompt": 1, "completion": 5}, "require_parameters": true},
	})
	if err != nil {
		return decision, errors.New("task request could not be encoded")
	}
	reservation, err := control.Reserve(ctx, "openrouter", s.ModelName, len(body), limit, uuid.NewString())
	if err != nil {
		return decision, err
	}
	if reservation != nil {
		defer func() {
			var cost *float64
			if decision.CostKnown {
				cost = &decision.Cost
			}
			err = errors.Join(err, reservation.Settle(ctx, cost, "", nil))
		}()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://openrouter.ai/api/v1/responses", bytes.NewReader(body))
	if err != nil {
		return decision, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Key)
	req.Header.Set("Content-Type", "application/json")
	res, err := s.Client.Do(req)
	if err != nil {
		return decision, errors.New("task model request interrupted or unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return decision, fmt.Errorf("task model returned HTTP %d", res.StatusCode)
	}
	var response struct {
		Status string            `json:"status"`
		Output []json.RawMessage `json:"output"`
		Usage  *struct {
			Input        int32    `json:"input_tokens"`
			Output       int32    `json:"output_tokens"`
			Total        int32    `json:"total_tokens"`
			Cost         *float64 `json:"cost"`
			InputDetails struct {
				Cached int32 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			OutputDetails struct {
				Reasoning int32 `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&response) != nil {
		return decision, errors.New("task model returned an unreadable response")
	}
	if u := response.Usage; u != nil {
		decision.Usage = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: u.Input, CandidatesTokenCount: u.Output - u.OutputDetails.Reasoning, ThoughtsTokenCount: u.OutputDetails.Reasoning, CachedContentTokenCount: u.InputDetails.Cached, TotalTokenCount: u.Total}
		if u.Cost != nil && *u.Cost >= 0 && !math.IsNaN(*u.Cost) && !math.IsInf(*u.Cost, 0) {
			decision.Cost = *u.Cost
			decision.CostKnown = true
		}
	}
	// Keep billed usage even when the generation is incomplete or unusable.
	if !decision.CostKnown {
		return decision, errors.New("task model reported no cost; budget cannot be enforced")
	}
	if response.Status != "completed" {
		return decision, errors.New("task model response incomplete; no tools executed")
	}
	saved, err := json.Marshal(response.Output)
	if err != nil {
		return decision, err
	}
	decision.Content = &genai.Content{Role: "model", Parts: []*genai.Part{{Thought: true, ThoughtSignature: append([]byte(responsesMarker), saved...)}}}
	calls := []*genai.FunctionCall{}
	text := []string{}
	for _, raw := range response.Output {
		var item struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return decision, errors.New("invalid task response item")
		}
		switch item.Type {
		case "function_call":
			call := &genai.FunctionCall{ID: item.CallID, Name: item.Name}
			if call.ID == "" || json.Unmarshal([]byte(item.Arguments), &call.Args) != nil || call.Args == nil {
				return decision, errors.New("invalid task tool call")
			}
			calls = append(calls, call)
			decision.Content.Parts = append(decision.Content.Parts, &genai.Part{FunctionCall: call})
		case "message":
			for _, content := range item.Content {
				if content.Type == "output_text" {
					text = append(text, content.Text)
				}
			}
		}
	}
	if len(calls) == 0 {
		decision.Answer = strings.TrimSpace(strings.Join(text, "\n"))
	}
	if len(calls) == 0 && decision.Answer == "" {
		return decision, errors.New("task model returned no answer")
	}
	decision.Calls = calls
	if decision.Answer != "" {
		decision.Content.Parts = append(decision.Content.Parts, &genai.Part{Text: decision.Answer})
	}
	return decision, nil
}

// responseSchema preserves nested object and array types for strict Responses tools.
func responseSchema(schema *genai.Schema) map[string]any {
	if schema == nil {
		return nil
	}
	result := map[string]any{"type": strings.ToLower(string(schema.Type))}
	if schema.Description != "" {
		result["description"] = schema.Description
	}
	if len(schema.Enum) > 0 {
		result["enum"] = schema.Enum
	}
	if schema.Type == genai.TypeObject {
		properties := map[string]any{}
		for name, child := range schema.Properties {
			properties[name] = responseSchema(child)
		}
		required := schema.Required
		if required == nil {
			required = []string{}
		}
		result["properties"], result["required"], result["additionalProperties"] = properties, required, false
	}
	if schema.Type == genai.TypeArray {
		result["items"] = responseSchema(schema.Items)
	}
	return result
}
