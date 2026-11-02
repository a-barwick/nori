package conversation

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Reuse the runtime's command schemas in one strict action-list response.
// Validated actions enter the same durable admission and replay path.
func responseFormat(taskIDs []string, requests []WorkRequest) any {
	var tools []struct {
		Function struct {
			Name, Description string
			Parameters        map[string]any
		} `json:"function"`
	}
	_ = json.Unmarshal([]byte(taskTools), &tools)
	variants := []any{}
	restricted := len(requests) > 0
	asking, questions := []string{}, []string{}
	for _, request := range requests {
		asking, questions = append(asking, request.TaskID), append(questions, request.QuestionID)
	}
	for _, tool := range tools {
		if restricted && tool.Function.Name != "dispatch" && tool.Function.Name != "answer" {
			continue
		}
		if tool.Function.Name == "dispatch" {
			parents := []any{nil}
			for _, id := range taskIDs {
				parents = append(parents, id)
			}
			field := tool.Function.Parameters["properties"].(map[string]any)["for"].(map[string]any)
			field["enum"] = parents
			if restricted {
				field["type"], field["enum"] = "string", asking
				question := tool.Function.Parameters["properties"].(map[string]any)["question_id"].(map[string]any)
				question["type"], question["enum"] = "string", questions
			}
		} else if restricted {
			properties := tool.Function.Parameters["properties"].(map[string]any)
			properties["run_id"].(map[string]any)["enum"] = asking
			properties["question_id"].(map[string]any)["enum"] = questions
		}
		if tool.Function.Name != "dispatch" && !restricted {
			if len(taskIDs) == 0 {
				continue
			}
			properties := tool.Function.Parameters["properties"].(map[string]any)
			key := "run_id"
			if tool.Function.Name == "hold" {
				key = "task_id"
			}
			properties[key].(map[string]any)["enum"] = taskIDs
		}
		variants = append(variants, map[string]any{
			"type": "object", "description": tool.Function.Description,
			"properties": map[string]any{"name": map[string]any{"type": "string", "enum": []string{tool.Function.Name}}, "arguments": tool.Function.Parameters},
			"required":   []string{"name", "arguments"}, "additionalProperties": false,
		})
	}
	return map[string]any{"type": "json_schema", "json_schema": map[string]any{
		"name": "nori_response", "strict": true, "schema": map[string]any{
			"type": "object", "properties": map[string]any{
				"actions": map[string]any{"type": "array", "maxItems": 16, "items": map[string]any{"anyOf": variants}},
				"text":    map[string]any{"type": "string", "maxLength": 16384},
			}, "required": []string{"actions", "text"}, "additionalProperties": false,
		},
	}}
}

func decodeResponse(content string) (string, []ToolCall, error) {
	var response struct {
		Actions *[]struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"actions"`
		Text *string `json:"text"`
	}
	if decodeCommandArguments(content, &response) != nil || response.Actions == nil || response.Text == nil ||
		len(*response.Actions) > 16 || len(*response.Text) > 16384 {
		return "", nil, fmt.Errorf("model returned an invalid structured routing response")
	}
	calls := make([]ToolCall, 0, len(*response.Actions))
	for i, action := range *response.Actions {
		call := ToolCall{ID: fmt.Sprintf("action_%d", i), Type: "function"}
		call.Function.Name, call.Function.Arguments = action.Name, string(action.Arguments)
		calls = append(calls, call)
	}
	if _, err := commandRequests(calls); err != nil {
		return "", nil, err
	}
	text := strings.TrimSpace(*response.Text)
	if len(calls) == 0 && text == "" {
		return "", nil, fmt.Errorf("model returned neither a reply nor a task command")
	}
	return text, calls, nil
}
