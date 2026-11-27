package agent

import (
	"errors"
	"fmt"
	"strings"

	"nori/internal/extensions"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/state/docstore"
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
		Type: transcript.TypeObject,
		Properties: map[string]*transcript.Schema{
			"title":    {Type: transcript.TypeString, Description: "Optional concise document title when returning a write-up, report, or artifact."},
			"answer":   {Type: transcript.TypeString, Description: "The complete response for the user, including any helpful caveats or notes."},
			"evidence": {Type: transcript.TypeArray, Items: text, Description: "Optional opaque operation_id or artifact_id references backing the answer."},
			"unmet":    {Type: transcript.TypeArray, Items: text, Description: "Optional. Omit or pass [] whenever the user's request was fulfilled. Only list explicitly requested deliverables or actions that failed or could not be completed. Never include caveats, disclaimers, unverified background details, or placeholders like 'none'."},
		},
		Required: []string{"answer"},
	}
	capabilities := &transcript.Schema{
		Type:       transcript.TypeObject,
		Properties: map[string]*transcript.Schema{"names": list},
		Required:   []string{"names"},
	}
	memorySearch := &transcript.Schema{
		Type: transcript.TypeObject,
		Properties: map[string]*transcript.Schema{
			"query": {Type: transcript.TypeString, Description: "Keywords, phrase, or identifier to search across Core Memory and Daily Logs."},
			"layer": {Type: transcript.TypeString, Enum: []string{"", "core", "log"}, Description: "Optional layer filter: \"core\", \"log\", or \"\" for all."},
			"limit": {Type: transcript.TypeString, Description: "Optional max results (1-20; pass \"\" for default 8)."},
		},
		Required: []string{"query", "layer", "limit"},
	}
	memoryGet := &transcript.Schema{
		Type: transcript.TypeObject,
		Properties: map[string]*transcript.Schema{
			"target":   {Type: transcript.TypeString, Enum: []string{"core", "log"}, Description: "Which memory layer to read: \"core\" or \"log\"."},
			"date":     {Type: transcript.TypeString, Description: "YYYY-MM-DD date when target is \"log\"; \"\" when target is \"core\"."},
			"entry_id": {Type: transcript.TypeString, Description: "Optional specific log entry ID when target is \"log\"; \"\" for the full day's log."},
		},
		Required: []string{"target", "date", "entry_id"},
	}
	unknown := ToolPolicy{Effect: "unknown"}
	runtimeRead := ToolPolicy{Effect: "read", Runtime: true}
	runtimeControl := ToolPolicy{Effect: "control", Runtime: true}
	paidRead := ToolPolicy{Effect: "read", Runtime: true, InterruptedCostUnknown: true}
	search := ToolPolicy{Effect: "read", Runtime: true, InterruptedCostUSD: ExaSearchCostUSD}
	requestInputOption := &transcript.Schema{
		Type: transcript.TypeObject,
		Properties: map[string]*transcript.Schema{
			"id":    {Type: transcript.TypeString, Description: "Short stable option identifier (e.g. \"opt_1\")."},
			"label": {Type: transcript.TypeString, Description: "Concise user-facing label for this option."},
		},
		Required: []string{"id", "label"},
	}
	requestInputParams := &transcript.Schema{
		Type: transcript.TypeObject,
		Properties: map[string]*transcript.Schema{
			"question": {Type: transcript.TypeString, Description: "Self-contained question for the user."},
			"input":    {Type: transcript.TypeString, Enum: []string{"text", "choice", "multi_choice", "confirm", "date"}, Description: "Input style: \"confirm\" for yes/no, \"choice\" or \"multi_choice\" for small option sets, \"date\" for a date, or \"text\" for free-form answers."},
			"options":  {Type: transcript.TypeArray, Items: requestInputOption, Description: "Choice options when input is \"choice\" or \"multi_choice\"; pass [] otherwise."},
		},
		Required: []string{"question", "input", "options"},
	}
	requestInput := defineTaskTool("request_input", requestInputParams, "", unknown)
	requestInput.access, requestInput.unavailable = inputTools, "tool-request-input-is-not-available"
	requestWork := defineTaskTool("request_work", params("request", "why"), "", unknown)
	requestWork.access, requestWork.unavailable = delegationTools, "tool-request-work-is-not-available-in-a-subtask"

	operationReadParams := &transcript.Schema{
		Type: transcript.TypeObject,
		Properties: map[string]*transcript.Schema{
			"operation_id": {Type: transcript.TypeString, Description: "The operation_id named in an omitted tool output."},
			"offset":       {Type: transcript.TypeString, Description: "Optional byte offset for paging large outputs (pass \"\" or \"0\" for start)."},
			"limit":        {Type: transcript.TypeString, Description: "Optional byte limit per page (up to 32768; pass \"\" for default 16384)."},
		},
		Required: []string{"operation_id", "offset", "limit"},
	}
	return []taskToolDefinition{
		defineTaskTool("complete", completion, "", unknown),
		defineTaskTool("web_search", params("query"), searchCapability, search),
		defineTaskTool("result_link", params("url", "title"), resultCapability, runtimeControl),
		defineTaskTool("results_find", params("query"), resultCapability, runtimeRead),
		defineTaskTool("result_read", params("result_id"), resultCapability, runtimeRead),
		defineTaskTool("upload_inspect", params("upload_id", "question"), attachmentCapability, paidRead),
		defineTaskTool("memory_read", nil, memoryCapability, runtimeRead),
		defineTaskTool("memory_search", memorySearch, memoryCapability, runtimeRead),
		defineTaskTool("memory_get", memoryGet, memoryCapability, runtimeRead),
		defineTaskTool("conversation_read", params("message_id"), memoryCapability, runtimeRead),
		defineTaskTool("conversation_find", params("query"), memoryCapability, runtimeRead),
		defineTaskTool("task_history_read", params("history_ref", "before", "offset"), memoryCapability, runtimeRead),
		defineTaskTool("operation_read", operationReadParams, memoryCapability, runtimeRead),
		defineTaskTool("capability_load", capabilities, "", runtimeRead),
		defineTaskTool("compose", params("script"), "", runtimeControl),
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

func TaskToolPolicy(name string) ToolPolicy {
	if definition, known := taskToolsByName[name]; known {
		return definition.policy
	}
	return ToolPolicy{Name: "unknown", Effect: "unknown"}
}

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
		case "web_search":
			query, _ := call.Args["query"].(string)
			if strings.TrimSpace(query) == "" || len(query) > maxSearchQuery {
				return errors.New(prompts.Runtime("search-query-required", map[string]any{"Limit": maxSearchQuery}))
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
		case "result_read":
			id, _ := call.Args["result_id"].(string)
			if !state.ValidUploadID(id) {
				return errors.New(prompts.Runtime("result-id-required", map[string]any{"Tool": call.Name}))
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
		case "memory_search":
			query, _ := call.Args["query"].(string)
			if strings.TrimSpace(query) == "" || len(query) > maxSearchQuery {
				return errors.New("memory_search requires a query of 1-400 bytes")
			}
			layer, _ := call.Args["layer"].(string)
			layer = strings.TrimSpace(layer)
			if layer != "" && layer != docstore.MemoryLayerCore && layer != docstore.MemoryLayerLog {
				return errors.New("memory_search layer must be \"\", \"core\", or \"log\"")
			}
			if _, ok := parseMemorySearchLimit(call.Args["limit"]); !ok {
				return errors.New("memory_search limit must be an integer between 1 and 20")
			}
		case "memory_get":
			target, _ := call.Args["target"].(string)
			target = strings.TrimSpace(target)
			date, _ := call.Args["date"].(string)
			date = strings.TrimSpace(date)
			entryID, _ := call.Args["entry_id"].(string)
			entryID = strings.TrimSpace(entryID)
			switch target {
			case docstore.MemoryLayerCore:
			case docstore.MemoryLayerLog:
				if !state.ValidMemoryDate(date) {
					return errors.New("memory_get with target=\"log\" requires date in YYYY-MM-DD format")
				}
				if len(entryID) > 256 {
					return errors.New("memory_get entry_id must be at most 256 bytes")
				}
			default:
				return errors.New("memory_get target must be \"core\" or \"log\"")
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
		case "compose":
			script, _ := call.Args["script"].(string)
			if strings.TrimSpace(script) == "" || len(script) > 64<<10 {
				return errors.New("compose needs a script of 1-65536 bytes")
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
				raw, present := call.Args[key]
				if !present || raw == nil {
					continue
				}
				values, ok := raw.([]any)
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
			inputKind := "text"
			if raw, ok := call.Args["input"]; ok && raw != nil {
				s, ok := raw.(string)
				if !ok {
					return errors.New("request_input input must be text, choice, multi_choice, confirm, or date")
				}
				if trimmed := strings.TrimSpace(s); trimmed != "" {
					inputKind = trimmed
				}
			}
			switch inputKind {
			case "text", "choice", "multi_choice", "confirm", "date":
			default:
				return errors.New("request_input input must be text, choice, multi_choice, confirm, or date")
			}
			var optionCount int
			if rawOpts, ok := call.Args["options"]; ok && rawOpts != nil {
				opts, ok := rawOpts.([]any)
				if !ok {
					return errors.New("request_input options must be an array of {id, label}")
				}
				if len(opts) > 12 {
					return errors.New("request_input supports at most 12 options")
				}
				seenOpt := map[string]bool{}
				for _, item := range opts {
					m, ok := item.(map[string]any)
					if !ok {
						return errors.New("request_input options must be objects with id and label")
					}
					id, _ := m["id"].(string)
					label, _ := m["label"].(string)
					id = strings.TrimSpace(id)
					label = strings.TrimSpace(label)
					if id == "" || len(id) > 80 || label == "" || len(label) > 200 {
						return errors.New("request_input option requires non-empty id (<=80 bytes) and label (<=200 bytes)")
					}
					if seenOpt[id] {
						return errors.New("request_input option ids must be unique")
					}
					seenOpt[id] = true
					optionCount++
				}
			}
			if (inputKind == "choice" || inputKind == "multi_choice") && optionCount == 0 {
				return errors.New("request_input with choice or multi_choice requires at least one option")
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
