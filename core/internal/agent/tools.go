package agent

import (
	"errors"
	"fmt"
	"strings"

	"nori/internal/extensions"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/transcript"
)

type ToolPolicy struct {
	Name                   string
	Effect                 string
	Runtime                bool
	InterruptedCostUSD     float64
	InterruptedCostUnknown bool
}

type toolAccess int

const (
	ordinaryTools toolAccess = iota
	inputTools
	delegationTools
)

func availableTools(wait, work bool) toolAccess {
	if !wait {
		return ordinaryTools
	}
	if work {
		return delegationTools
	}
	return inputTools
}

type taskToolDefinition struct {
	transcript.Tool
	policy      ToolPolicy
	capability  string
	access      toolAccess
	unavailable string
}

func defineTaskTool(name string, parameters *transcript.Schema, capability string, policy ToolPolicy) taskToolDefinition {
	policy.Name = name
	return taskToolDefinition{
		Tool:   transcript.Tool{Name: name, Description: prompts.TaskTool(name), Parameters: parameters},
		policy: policy, capability: capability,
	}
}

var taskToolDefinitions = func() []taskToolDefinition {
	text := &transcript.Schema{Type: transcript.TypeString}
	list := &transcript.Schema{Type: transcript.TypeArray, Items: text}
	completion := &transcript.Schema{
		Type:       transcript.TypeObject,
		Properties: map[string]*transcript.Schema{"answer": text, "evidence": list, "unmet": list},
		Required:   []string{"answer", "evidence", "unmet"},
	}
	shell := &transcript.Schema{
		Type:       transcript.TypeObject,
		Properties: map[string]*transcript.Schema{"command": text, "read_only": {Type: transcript.TypeBoolean}},
		Required:   []string{"command", "read_only"},
	}
	capabilities := &transcript.Schema{
		Type:       transcript.TypeObject,
		Properties: map[string]*transcript.Schema{"names": list},
		Required:   []string{"names"},
	}
	unknown := ToolPolicy{Effect: "unknown"}
	computerRead := ToolPolicy{Effect: "read"}
	computerControl := ToolPolicy{Effect: "control"}
	runtimeRead := ToolPolicy{Effect: "read", Runtime: true}
	runtimeControl := ToolPolicy{Effect: "control", Runtime: true}
	paidRead := ToolPolicy{Effect: "read", Runtime: true, InterruptedCostUnknown: true}
	search := ToolPolicy{Effect: "read", Runtime: true, InterruptedCostUSD: SearchCostUSD}
	requestInput := defineTaskTool("request_input", params("question"), "", unknown)
	requestInput.access, requestInput.unavailable = inputTools, "tool-request-input-is-not-available"
	requestWork := defineTaskTool("request_work", params("request", "why"), "", unknown)
	requestWork.access, requestWork.unavailable = delegationTools, "tool-request-work-is-not-available-in-a-subtask"

	// Keep schema order stable for provider prompt caches.
	return []taskToolDefinition{
		defineTaskTool("complete", completion, "", unknown),
		defineTaskTool("shell", shell, shellCapability, unknown),
		defineTaskTool("job_start", params("command"), shellCapability, unknown),
		defineTaskTool("job_status", params("job_id"), shellCapability, computerRead),
		defineTaskTool("job_wait", params("job_id"), shellCapability, computerRead),
		defineTaskTool("job_stop", params("job_id"), shellCapability, computerControl),
		defineTaskTool("web_search", params("query"), searchCapability, search),
		defineTaskTool("browser_navigate", params("url"), browserCapability, computerRead),
		defineTaskTool("browser_screenshot", nil, browserCapability, computerRead),
		defineTaskTool("result_save", params("path", "title"), resultCapability, computerControl),
		defineTaskTool("result_link", params("url", "title"), resultCapability, runtimeControl),
		defineTaskTool("results_find", params("query"), resultCapability, runtimeRead),
		defineTaskTool("result_read", params("result_id"), resultCapability, runtimeRead),
		defineTaskTool("result_open", params("result_id"), resultCapability, computerRead),
		defineTaskTool("upload_open", params("upload_id"), attachmentCapability, computerRead),
		defineTaskTool("upload_inspect", params("upload_id", "question"), attachmentCapability, paidRead),
		defineTaskTool("screenshot_inspect", params("artifact_id", "question"), browserCapability, paidRead),
		defineTaskTool("memory_read", nil, memoryCapability, runtimeRead),
		defineTaskTool("conversation_read", params("message_id"), memoryCapability, runtimeRead),
		defineTaskTool("conversation_find", params("query"), memoryCapability, runtimeRead),
		defineTaskTool("task_history_read", params("history_ref", "before", "offset"), memoryCapability, runtimeRead),
		defineTaskTool("operation_read", params("operation_id"), memoryCapability, runtimeRead),
		defineTaskTool("capability_load", capabilities, "", runtimeRead),
		requestInput,
		requestWork,
	}
}()

var taskToolsByName = func() map[string]taskToolDefinition {
	tools := make(map[string]taskToolDefinition, len(taskToolDefinitions))
	for _, definition := range taskToolDefinitions {
		tools[definition.Name] = definition
	}
	return tools
}()

// Temporal replay relies on the same policy for tool names already in history.
func TaskToolPolicy(name string) ToolPolicy {
	if definition, known := taskToolsByName[name]; known {
		return definition.policy
	}
	return ToolPolicy{Name: "unknown", Effect: "unknown"}
}

// ValidateCalls checks a whole tool batch before any call runs, so a batch
// is either executed in order or rejected without effects.
// Calls naming a definition outside the engine are checked against snapshot, the set the model was offered.
func ValidateCalls(calls []transcript.ToolCall, wait, work bool, snapshot *extensions.Snapshot) error {
	if len(calls) > 64 {
		return errors.New(prompts.Runtime("tool-a-tool-batch-may-contain-at-most-64-calls", nil))
	}
	access := availableTools(wait, work)
	seen := map[string]bool{}
	for _, call := range calls {
		if len(call.ID) > 256 {
			return errors.New(prompts.Runtime("tool-tool-call-identity-exceeds-256-bytes", nil))
		}
		if call.ID != "" && seen[call.ID] {
			return errors.New(prompts.Runtime("tool-tool-call-identities-must-be-unique", nil))
		}
		seen[call.ID] = true
		definition, known := taskToolsByName[call.Name]
		if !known {
			registered, ok := snapshot.Lookup(call.Name)
			if !ok || registered.Builtin() || registered.Exposure == extensions.ExposureDisabled || registered.Exposure == extensions.ExposureProgrammatic {
				return errors.New(prompts.Runtime("unknown-tool-name", map[string]any{"Tool": call.Name}))
			}
			schema, err := extensions.CompileSchema(registered.InputSchema)
			if err == nil {
				err = schema.Validate(call.Args)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", call.Name, err)
			}
			continue
		}
		if definition.access > access {
			return errors.New(prompts.Runtime(definition.unavailable, nil))
		}
		switch call.Name {
		case "shell":
			command, _ := call.Args["command"].(string)
			if command == "" || len(command) > state.MaxMessageBytes {
				return errors.New(prompts.Runtime("tool-shell-needs-a-command-of-1-8192-bytes", nil))
			}
		case "browser_navigate":
			address, _ := call.Args["url"].(string)
			if address == "" || len(address) > state.MaxMessageBytes {
				return errors.New(prompts.Runtime("tool-browser-navigate-needs-a-url-of-1-8192-bytes", nil))
			}
		case "job_start":
			command, _ := call.Args["command"].(string)
			if command == "" || len(command) > state.MaxMessageBytes {
				return errors.New(prompts.Runtime("tool-job-start-needs-a-command-of-1-8192-bytes", nil))
			}
		case "job_status", "job_wait", "job_stop":
			id, _ := call.Args["job_id"].(string)
			if id == "" || len(id) > 80 {
				return errors.New(prompts.Runtime("job-id-required", map[string]any{"Tool": call.Name}))
			}
		case "web_search":
			query, _ := call.Args["query"].(string)
			if strings.TrimSpace(query) == "" || len(query) > maxSearchQuery {
				return errors.New(prompts.Runtime("search-query-required", map[string]any{"Limit": maxSearchQuery}))
			}
		case "screenshot_inspect":
			id, _ := call.Args["artifact_id"].(string)
			question, _ := call.Args["question"].(string)
			if id == "" || len(id) > 80 {
				return errors.New(prompts.Runtime("tool-screenshot-inspect-needs-the-artifact-id-browser-screenshot-retur", nil))
			}
			if strings.TrimSpace(question) == "" || len(question) > state.MaxMessageBytes {
				return errors.New(prompts.Runtime("tool-screenshot-inspect-needs-a-question-of-1-8192-bytes", nil))
			}
		case "result_save":
			filePath, _ := call.Args["path"].(string)
			title, _ := call.Args["title"].(string)
			if strings.TrimSpace(filePath) == "" || len(filePath) > 1024 || strings.TrimSpace(title) == "" || len(title) > state.MaxResultTitle {
				return errors.New(prompts.Runtime("tool-result-save-needs-a-path-inside-the-task-s-folder-and-a-title-of", nil))
			}
		case "result_link":
			address, _ := call.Args["url"].(string)
			title, _ := call.Args["title"].(string)
			if err := state.ValidLink(address); err != nil {
				return err
			}
			if strings.TrimSpace(title) == "" || len(title) > state.MaxResultTitle {
				return errors.New(prompts.Runtime("tool-result-link-needs-a-title-of-1-200-bytes", nil))
			}
		case "results_find":
			query, _ := call.Args["query"].(string)
			if strings.TrimSpace(query) == "" || len(query) > maxSearchQuery {
				return errors.New(prompts.Runtime("results-query-required", map[string]any{"Limit": maxSearchQuery}))
			}
		case "result_read", "result_open":
			id, _ := call.Args["result_id"].(string)
			if !state.ValidUploadID(id) {
				return errors.New(prompts.Runtime("result-id-required", map[string]any{"Tool": call.Name}))
			}
		case "upload_open":
			id, _ := call.Args["upload_id"].(string)
			if !state.ValidUploadID(id) {
				return errors.New(prompts.Runtime("tool-upload-open-needs-the-upload-id-of-a-file-this-task-was-given", nil))
			}
		case "upload_inspect":
			id, _ := call.Args["upload_id"].(string)
			question, _ := call.Args["question"].(string)
			if !state.ValidUploadID(id) {
				return errors.New(prompts.Runtime("tool-upload-inspect-needs-the-upload-id-of-an-image-this-task-was-give", nil))
			}
			if strings.TrimSpace(question) == "" || len(question) > state.MaxMessageBytes {
				return errors.New(prompts.Runtime("tool-upload-inspect-needs-a-question-of-1-8192-bytes", nil))
			}
		case "conversation_read":
			id, _ := call.Args["message_id"].(string)
			if !state.ValidEvidenceID(id) {
				return errors.New("conversation_read requires a source message_id or input_id")
			}
		case "conversation_find":
			query, _ := call.Args["query"].(string)
			if strings.TrimSpace(query) == "" || len(query) > maxSearchQuery {
				return errors.New(prompts.Runtime("history-query-required", map[string]any{"Limit": maxSearchQuery}))
			}
		case "task_history_read":
			ref, _ := call.Args["history_ref"].(string)
			before, _ := call.Args["before"].(string)
			if !state.ValidHistoryRef(ref) || len(before) > 10 || before == "" {
				return errors.New("task_history_read needs history_ref and before")
			}
		case "operation_read":
			id, _ := call.Args["operation_id"].(string)
			if id == "" || len(id) > 256 {
				return errors.New(prompts.Runtime("tool-operation-read-needs-the-operation-id-an-omitted-output-names", nil))
			}
		case "capability_load":
			names, ok := call.Args["names"].([]any)
			if !ok || len(names) == 0 || len(names) > 16 {
				return errors.New(prompts.Runtime("tool-capability-load-needs-1-16-capability-names", nil))
			}
			for _, name := range names {
				if text, ok := name.(string); !ok || strings.TrimSpace(text) == "" || len(text) > 80 {
					return errors.New(prompts.Runtime("tool-capability-load-names-must-be-catalog-capability-names", nil))
				}
			}
		case "complete":
			if len(calls) != 1 {
				return errors.New(prompts.Runtime("tool-complete-must-be-the-only-tool-call", nil))
			}
			answer, _ := call.Args["answer"].(string)
			if strings.TrimSpace(answer) == "" || len(answer) > 65536 {
				return errors.New(prompts.Runtime("tool-complete-needs-an-answer-of-1-65536-bytes", nil))
			}
			for _, key := range []string{"evidence", "unmet"} {
				values, ok := call.Args[key].([]any)
				if !ok || len(values) > 256 {
					return errors.New(prompts.Runtime("completion-array", map[string]any{"Field": key}))
				}
				for _, value := range values {
					if text, ok := value.(string); !ok || len(text) > state.MaxMessageBytes {
						return errors.New(prompts.Runtime("completion-strings", map[string]any{"Field": key}))
					}
				}
			}
		case "request_input":
			if len(calls) != 1 {
				return errors.New(prompts.Runtime("tool-request-input-must-be-the-only-tool-call", nil))
			}
			question, _ := call.Args["question"].(string)
			if question == "" || len(question) > state.MaxMessageBytes {
				return errors.New(prompts.Runtime("tool-request-input-needs-a-question-of-1-8192-bytes", nil))
			}
		case "request_work":
			if len(calls) != 1 {
				return errors.New(prompts.Runtime("tool-request-work-must-be-the-only-tool-call", nil))
			}
			request, _ := call.Args["request"].(string)
			why, _ := call.Args["why"].(string)
			if strings.TrimSpace(request) == "" || len(request)+len(why) > state.MaxMessageBytes-16 {
				return errors.New(prompts.Runtime("tool-request-work-needs-a-request-and-its-text-must-fit-in-8-kb", nil))
			}
		}
	}
	return nil
}
