package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"nori/internal/observability"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/transcript"
)

// The output cap includes reasoning, so it leaves room for both reasoning and a
// detailed answer without changing the model's default thinking settings.
const modelOutputTokenLimit = 8192

// Web search, screenshot reading and calendar reads run on the worker. Everything else runs on the computer.
func taskTools(wait, work bool) []transcript.Tool {
	text := &transcript.Schema{Type: transcript.TypeString}
	list := &transcript.Schema{Type: transcript.TypeArray, Items: text}
	tools := []transcript.Tool{
		{
			Name:        "complete",
			Description: prompts.TaskTool("complete"),
			Parameters: &transcript.Schema{
				Type:       transcript.TypeObject,
				Properties: map[string]*transcript.Schema{"answer": text, "evidence": list, "unmet": list},
				Required:   []string{"answer", "evidence", "unmet"},
			},
		},
		{
			Name:        "shell",
			Description: prompts.TaskTool("shell"),
			Parameters: &transcript.Schema{
				Type:       transcript.TypeObject,
				Properties: map[string]*transcript.Schema{"command": {Type: transcript.TypeString}, "read_only": {Type: transcript.TypeBoolean}},
				Required:   []string{"command", "read_only"},
			},
		},
		{
			Name:        "job_start",
			Description: prompts.TaskTool("job_start"),
			Parameters:  params("command"),
		},
		{
			Name:        "job_status",
			Description: prompts.TaskTool("job_status"),
			Parameters:  params("job_id"),
		},
		{
			Name:        "job_wait",
			Description: prompts.TaskTool("job_wait"),
			Parameters:  params("job_id"),
		},
		{
			Name:        "job_stop",
			Description: prompts.TaskTool("job_stop"),
			Parameters:  params("job_id"),
		},
		{
			Name:        "web_search",
			Description: prompts.TaskTool("web_search"),
			Parameters:  params("query"),
		},
		{
			Name:        "browser_navigate",
			Description: prompts.TaskTool("browser_navigate"),
			Parameters:  params("url"),
		},
		{
			Name:        "browser_screenshot",
			Description: prompts.TaskTool("browser_screenshot"),
		},
		{
			Name:        "result_save",
			Description: prompts.TaskTool("result_save"),
			Parameters:  params("path", "title"),
		},
		{
			Name:        "result_link",
			Description: prompts.TaskTool("result_link"),
			Parameters:  params("url", "title"),
		},
		{
			Name:        "results_find",
			Description: prompts.TaskTool("results_find"),
			Parameters:  params("query"),
		},
		{
			Name:        "result_read",
			Description: prompts.TaskTool("result_read"),
			Parameters:  params("result_id"),
		},
		{
			Name:        "result_open",
			Description: prompts.TaskTool("result_open"),
			Parameters:  params("result_id"),
		},
		{
			Name:        "upload_open",
			Description: prompts.TaskTool("upload_open"),
			Parameters:  params("upload_id"),
		},
		{
			Name:        "upload_inspect",
			Description: prompts.TaskTool("upload_inspect"),
			Parameters:  params("upload_id", "question"),
		},
		{
			Name:        "screenshot_inspect",
			Description: prompts.TaskTool("screenshot_inspect"),
			Parameters:  params("artifact_id", "question"),
		},
		{
			Name:        "calendar_create",
			Description: prompts.TaskTool("calendar_create"),
			Parameters:  params("title", "start", "end", "description", "private"),
		},
		{
			Name:        "calendar_move",
			Description: prompts.TaskTool("calendar_move"),
			Parameters:  params("event_id", "start", "end"),
		},
		{
			Name:        "email_send",
			Description: prompts.TaskTool("email_send"),
			Parameters:  params("to", "subject", "body"),
		},
		{
			Name:        "memory_read",
			Description: prompts.TaskTool("memory_read"),
		},
		{
			Name:        "conversation_find",
			Description: prompts.TaskTool("conversation_find"),
			Parameters:  params("query"),
		},
		{
			Name:        "operation_read",
			Description: prompts.TaskTool("operation_read"),
			Parameters:  params("operation_id"),
		},
		{
			Name:        "capability_load",
			Description: prompts.TaskTool("capability_load"),
			Parameters: &transcript.Schema{
				Type:       transcript.TypeObject,
				Properties: map[string]*transcript.Schema{"names": list},
				Required:   []string{"names"},
			},
		},
		{
			Name:        "calendar_read",
			Description: prompts.TaskTool("calendar_read"),
			Parameters:  params("start", "end"),
		},
	}
	if wait {
		tools = append(tools, transcript.Tool{
			Name:        "request_input",
			Description: prompts.TaskTool("request_input"),
			Parameters:  params("question"),
		})
	}
	if wait && work {
		tools = append(tools, transcript.Tool{
			Name:        "request_work",
			Description: prompts.TaskTool("request_work"),
			Parameters:  params("request", "why"),
		})
	}
	return tools
}

// ValidateCalls checks a whole tool batch before any call runs, so a batch
// is either executed in order or rejected without effects.
func ValidateCalls(calls []transcript.ToolCall, wait, work bool) error {
	if len(calls) > 64 {
		return errors.New(prompts.Runtime("tool-a-tool-batch-may-contain-at-most-64-calls", nil))
	}
	seen := map[string]bool{}
	for _, call := range calls {
		if len(call.ID) > 256 {
			return errors.New(prompts.Runtime("tool-tool-call-identity-exceeds-256-bytes", nil))
		}
		if call.ID != "" && seen[call.ID] {
			return errors.New(prompts.Runtime("tool-tool-call-identities-must-be-unique", nil))
		}
		seen[call.ID] = true
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
		case "browser_screenshot":
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
		case "memory_read":
		case "conversation_find":
			query, _ := call.Args["query"].(string)
			if strings.TrimSpace(query) == "" || len(query) > maxSearchQuery {
				return errors.New(prompts.Runtime("history-query-required", map[string]any{"Limit": maxSearchQuery}))
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
		case "calendar_read":
			start, _ := call.Args["start"].(string)
			end, _ := call.Args["end"].(string)
			if _, _, err := calendarRange(start, end); err != nil {
				return err
			}
		case "calendar_create", "calendar_move", "email_send":
			// Prepare validates the content. A write parks the task, so it runs alone.
			if len(calls) != 1 {
				return errors.New(prompts.Runtime("exclusive-tool", map[string]any{"Tool": call.Name}))
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
			if !wait {
				return errors.New(prompts.Runtime("tool-request-input-is-not-available", nil))
			}
			if len(calls) != 1 {
				return errors.New(prompts.Runtime("tool-request-input-must-be-the-only-tool-call", nil))
			}
			question, _ := call.Args["question"].(string)
			if question == "" || len(question) > state.MaxMessageBytes {
				return errors.New(prompts.Runtime("tool-request-input-needs-a-question-of-1-8192-bytes", nil))
			}
		case "request_work":
			if !wait || !work {
				return errors.New(prompts.Runtime("tool-request-work-is-not-available-in-a-subtask", nil))
			}
			if len(calls) != 1 {
				return errors.New(prompts.Runtime("tool-request-work-must-be-the-only-tool-call", nil))
			}
			request, _ := call.Args["request"].(string)
			why, _ := call.Args["why"].(string)
			if strings.TrimSpace(request) == "" || len(request)+len(why) > state.MaxMessageBytes-16 {
				return errors.New(prompts.Runtime("tool-request-work-needs-a-request-and-its-text-must-fit-in-8-kb", nil))
			}
		default:
			return errors.New(prompts.Runtime("unknown-tool-name", map[string]any{"Tool": call.Name}))
		}
	}
	return nil
}

// Decide performs exactly one model request. Its caller owns retries and budgets.
func (s *Service) Decide(ctx context.Context, messages []transcript.Message, wait, work bool) (resp Response, err error) {
	started := time.Now()
	provider, model := s.Model.Provider(), s.Model.Name()
	ctx, span := otel.Tracer("nori/agent").Start(ctx, "generate_content "+model, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
		attribute.String("openinference.span.kind", "LLM"), attribute.String("session.id", observability.SessionID()),
		attribute.String("gen_ai.operation.name", "generate_content"), attribute.String("gen_ai.provider.name", provider),
		attribute.String("gen_ai.request.model", model), attribute.String("llm.model_name", model),
	))
	span.SetAttributes(attribute.Int("nori.prompt.message_count", len(messages)))
	// Export only an unambiguous latest user text part. The historical context
	// block also has role=user; never scan past a tool response or export it.
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != transcript.RoleUser {
			continue
		}
		if len(messages[i].Parts) == 1 {
			part := messages[i].Parts[0]
			background := strings.HasPrefix(part.Text, prompts.Runtime("task-history-marker", nil))
			if part.Text != "" && !background && part.ToolCall == nil && part.ToolResult == nil && part.Provider == nil {
				span.SetAttributes(attribute.Bool("nori.input.truncated", len(part.Text) > 2048))
				if a, ok := observability.ContentAttribute("input.value", part.Text); ok {
					span.SetAttributes(a)
				}
			}
		}
		break
	}
	defer func() {
		span.SetAttributes(attribute.Bool("nori.output.present", resp.Answer != ""), attribute.Int("nori.tool.call_count", len(resp.Calls)))
		if u := resp.Usage; u != nil {
			input := int64(u.InputTokens)
			output := int64(u.OutputTokens) + int64(u.ReasoningTokens)
			span.SetAttributes(
				attribute.Int64("gen_ai.usage.input_tokens", input), attribute.Int64("gen_ai.usage.output_tokens", output),
				attribute.Int64("gen_ai.usage.cache_read.input_tokens", int64(u.CachedTokens)),
				attribute.Int64("gen_ai.usage.reasoning.output_tokens", int64(u.ReasoningTokens)),
				attribute.Int64("llm.token_count.prompt", input), attribute.Int64("llm.token_count.completion", output),
				attribute.Int64("llm.token_count.total", int64(u.TotalTokens)),
			)
			observability.Metric(ctx, "nori.model.input_tokens", float64(input))
			observability.Metric(ctx, "nori.model.output_tokens", float64(output))
		}
		if resp.CostKnown {
			span.SetAttributes(
				attribute.Float64("nori.cost.usd", resp.Cost), attribute.Float64("llm.cost.total", resp.Cost),
				attribute.String("nori.cost.status", "reported"),
			)
		} else if resp.NotSent {
			span.SetAttributes(attribute.String("nori.cost.status", "not_sent"))
		} else {
			span.SetAttributes(attribute.String("nori.cost.status", "unknown"))
		}
		if a, ok := observability.ContentAttribute("output.value", resp.Answer); ok {
			span.SetAttributes(a)
		}
		identity := []attribute.KeyValue{attribute.String("gen_ai.provider.name", provider), attribute.String("gen_ai.request.model", model)}
		if err != nil && !errors.Is(ctx.Err(), context.Canceled) {
			observability.RecordErrorCategory(ctx, "model.task", err)
			observability.Event(ctx, "model.call.failed", identity...)
		} else if err == nil {
			observability.Event(ctx, "model.call.completed", identity...)
		}
		if !errors.Is(ctx.Err(), context.Canceled) {
			observability.Metric(ctx, "nori.model.duration_ms", float64(time.Since(started).Milliseconds()))
		}
		span.End()
	}()
	return s.Model.Respond(ctx, Request{
		Instructions:    prompts.TaskWorker + "\n" + s.catalogInstruction(),
		Messages:        messages,
		Tools:           s.selectTools(taskTools(wait, work), messages),
		MaxOutputTokens: modelOutputTokenLimit,
	})
}

// selectTools keeps the tools of default capabilities, of capabilities the task loaded, and those no capability lists,
// such as complete. Leaving a schema out only shortens the request: execution still checks every call.
func (s *Service) selectTools(tools []transcript.Tool, messages []transcript.Message) []transcript.Tool {
	results := []map[string]any{}
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.ToolResult != nil && part.ToolResult.Name == "capability_load" {
				results = append(results, part.ToolResult.Output)
			}
		}
	}
	loaded := LoadedCapabilities(results)
	owner := map[string]Capability{}
	for _, capability := range s.Catalog() {
		for _, name := range strings.Split(capability.Tools, ",") {
			owner[strings.TrimSpace(name)] = capability
		}
	}
	selected := []transcript.Tool{}
	for _, tool := range tools {
		capability, listed := owner[tool.Name]
		if !listed || capability.Default || loaded[capability.Name] {
			selected = append(selected, tool)
		}
	}
	return selected
}
