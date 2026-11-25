package conversation

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"nori/internal/prompts"
	"nori/internal/state"
)

// Reuse the runtime's command schemas in one strict action-list response.
// Validated actions enter the same durable admission and replay path.
//
// stable omits every per-turn ID enum and keeps every tool, so the schema (part of the cached prompt prefix) does not
// change when memories or tasks do. Callers using it must check reply IDs with validateReplyIDs. It does not apply to
// command-plan or work-request turns, which keep their enum-restricted schemas.
func responseFormat(taskIDs []string, requests []WorkRequest, memoryIDs []string, commandPlan, gateway, stable bool) any {
	var tools []struct {
		Function struct {
			Name, Description string
			Parameters        map[string]any
		} `json:"function"`
	}
	_ = json.Unmarshal([]byte(prompts.ConversationTools), &tools)
	variants := []any{}
	restricted := len(requests) > 0
	asking, questions := []string{}, []string{}
	for _, request := range requests {
		asking, questions = append(asking, request.TaskID), append(questions, request.QuestionID)
	}
	if stable && !commandPlan && !restricted {
		for _, tool := range tools {
			variants = append(variants, actionVariant(tool.Function.Name, tool.Function.Description, tool.Function.Parameters))
		}
		tools = nil
	}
	for _, tool := range tools {
		if commandPlan && tool.Function.Name != "dispatch" && tool.Function.Name != "steer" &&
			tool.Function.Name != "stop" && tool.Function.Name != "answer" && tool.Function.Name != "hold" &&
			tool.Function.Name != state.MemoryRemember && tool.Function.Name != state.MemoryForget {
			continue
		}
		if restricted && tool.Function.Name != "dispatch" && tool.Function.Name != "answer" {
			continue
		}
		if tool.Function.Name == "remember" || tool.Function.Name == "forget" {
			if len(memoryIDs) == 0 && tool.Function.Name == "forget" {
				continue
			}
			properties := tool.Function.Parameters["properties"].(map[string]any)
			if tool.Function.Name == "forget" {
				properties["memory_id"].(map[string]any)["enum"] = memoryIDs
			} else {
				setNullableStringEnum(properties["replaces"].(map[string]any), memoryIDs, gateway)
			}
			variants = append(variants, actionVariant(tool.Function.Name, tool.Function.Description, tool.Function.Parameters))
			continue
		}
		if tool.Function.Name == "dispatch" {
			field := tool.Function.Parameters["properties"].(map[string]any)["for"].(map[string]any)
			if restricted {
				field["type"], field["enum"] = "string", asking
				question := tool.Function.Parameters["properties"].(map[string]any)["question_id"].(map[string]any)
				question["type"], question["enum"] = "string", questions
			} else if commandPlan {
				setNullableStringEnum(field, nil, gateway)
				setNullableStringEnum(tool.Function.Parameters["properties"].(map[string]any)["question_id"].(map[string]any), nil, gateway)
			} else {
				setNullableStringEnum(field, taskIDs, gateway)
			}
		} else if restricted {
			properties := tool.Function.Parameters["properties"].(map[string]any)
			properties["run_id"].(map[string]any)["enum"] = asking
			properties["question_id"].(map[string]any)["enum"] = questions
		}
		if tool.Function.Name != "dispatch" && tool.Function.Name != "show_result" && !restricted {
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
		variants = append(variants, actionVariant(tool.Function.Name, tool.Function.Description, tool.Function.Parameters))
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

// validateReplyIDs enforces, after decoding, the ID restrictions a non-stable response schema expresses as enums:
// task commands name only the tasks shown to the model, and memory changes name only saved memories.
func validateReplyIDs(calls []ToolCall, taskIDs, memoryIDs []string) error {
	for _, call := range calls {
		var args map[string]any
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			continue // malformed arguments are rejected with their own error downstream
		}
		check := func(key string, allowed []string, nullable bool, what string) error {
			value, present := args[key]
			if !present || (nullable && value == nil) {
				return nil
			}
			id, ok := value.(string)
			if ok && nullable && id == "" {
				return nil
			}
			if !ok || !slices.Contains(allowed, id) {
				return fmt.Errorf("%s names a %s that isn't in this conversation", call.Function.Name, what)
			}
			return nil
		}
		var err error
		switch call.Function.Name {
		case "steer", "answer", "stop":
			err = check("run_id", taskIDs, false, "task")
		case "hold":
			err = check("task_id", taskIDs, false, "task")
		case "dispatch":
			err = check("for", taskIDs, true, "task")
		case state.MemoryForget:
			err = check("memory_id", memoryIDs, false, "memory")
		case state.MemoryRemember:
			err = check("replaces", memoryIDs, true, "memory")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Some model providers reject a null enum member on a field with a union type.
func setNullableStringEnum(field map[string]any, values []string, gateway bool) {
	if !gateway {
		items := make([]any, 1, len(values)+1)
		for _, value := range values {
			items = append(items, value)
		}
		field["enum"] = items
		return
	}
	if len(values) == 0 {
		field["type"] = "null"
		return
	}
	delete(field, "type")
	field["anyOf"] = []any{map[string]any{"type": "string", "enum": values}, map[string]any{"type": "null"}}
}

func actionVariant(name, description string, parameters map[string]any) map[string]any {
	return map[string]any{
		"type": "object", "description": description,
		"properties": map[string]any{"name": map[string]any{"type": "string", "enum": []string{name}}, "arguments": parameters},
		"required":   []string{"name", "arguments"}, "additionalProperties": false,
	}
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
	commands, _, err := splitShows(calls)
	if err == nil {
		commands, _, err = splitMemory(commands)
	}
	if err != nil {
		return "", nil, err
	}
	if _, err := commandRequests(commands); err != nil {
		return "", nil, err
	}
	text := strings.TrimSpace(*response.Text)
	if len(calls) == 0 && text == "" {
		return "", nil, fmt.Errorf("model returned neither a reply nor a task command")
	}
	return text, calls, nil
}
