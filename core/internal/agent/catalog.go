package agent

import (
	"context"
	"errors"
	"time"

	"nori/internal/prompts"
)

// Capabilities holds what this worker was configured with. A missing credential
// makes a capability unavailable; it never removes the tool, so the model can tell
// the user the capability is unavailable rather than appear not to have chosen it.
type Capabilities struct {
	SearchKey string
	Location  *time.Location
}

// Capability is one catalog entry. Listing a capability grants nothing: the runtime
// still checks each call when it runs. A capability that changes something must name
// how to confirm the change in Verify, such as a status and ID or a follow-up read.
// Default capabilities' tools are always sent; a task loads the others with
// capability_load, so their schemas reach the model only when the task needs them.
type Capability struct {
	Name, Tools, Account, Availability, OnFailure, Verify string
	Default                                               bool
}

func (s *Service) Catalog() []Capability {
	search := prompts.Runtime("capability-available", nil)
	if s.Capabilities.SearchKey == "" {
		search = prompts.Runtime("capability-search-unavailable", nil)
	}
	computerAvailability := prompts.Runtime("capability-available", nil)
	if s.Computer == nil {
		computerAvailability = "Computer is not enabled in this deployment."
	}
	return []Capability{
		{Name: "Web search", Tools: "web_search", Account: prompts.Runtime("catalog-web-search-account", nil), Availability: search, OnFailure: prompts.Runtime("catalog-web-search-onfailure", nil), Verify: prompts.Runtime("catalog-web-search-verify", nil), Default: true},
		{Name: "Browser", Tools: "browser_navigate, browser_screenshot, screenshot_inspect", Account: prompts.Runtime("catalog-browser-navigate-account", nil), Availability: computerAvailability, OnFailure: prompts.Runtime("catalog-browser-navigate-onfailure", nil), Verify: prompts.Runtime("catalog-browser-navigate-verify", nil), Default: true},
		{Name: "Attached files", Tools: "upload_open, upload_inspect", Account: prompts.Runtime("catalog-upload-open-account", nil), Availability: prompts.Runtime("capability-available", nil), OnFailure: prompts.Runtime("catalog-upload-open-onfailure", nil), Verify: prompts.Runtime("catalog-upload-open-verify", nil)},
		{Name: "Saved results", Tools: "result_save, result_link, results_find, result_read, result_open", Account: prompts.Runtime("catalog-result-save-account", nil), Availability: prompts.Runtime("capability-available", nil), OnFailure: prompts.Runtime("catalog-result-save-onfailure", nil), Verify: prompts.Runtime("catalog-result-save-verify", nil), Default: true},
		{Name: "Memory and conversation", Tools: "memory_read, conversation_find, operation_read", Account: prompts.Runtime("catalog-memory-read-account", nil), Availability: prompts.Runtime("capability-available", nil), OnFailure: prompts.Runtime("catalog-memory-read-onfailure", nil), Verify: prompts.Runtime("catalog-memory-read-verify", nil), Default: true},
		{Name: "Shell", Tools: "shell, job_start, job_status, job_wait, job_stop", Account: prompts.Runtime("catalog-shell-account", nil), Availability: computerAvailability, OnFailure: prompts.Runtime("catalog-shell-onfailure", nil), Verify: prompts.Runtime("catalog-shell-verify", nil), Default: true},
	}
}

func (s *Service) location() *time.Location {
	if s.Capabilities.Location != nil {
		return s.Capabilities.Location
	}
	return time.UTC
}

func (s *Service) now() time.Time { return time.Now().In(s.location()) }

// catalogInstruction changes at most daily unless an account changes, so it doesn't
// disturb the provider's prompt cache between model calls.
func (s *Service) catalogInstruction() string {
	return prompts.Runtime("capability-catalog", map[string]any{
		"Date": s.now().Format("Monday, 2 January 2006"), "Zone": s.location().String(), "Capabilities": s.Catalog(),
	})
}

// RuntimeTool reports a tool the worker runs itself, without the computer.
func RuntimeTool(name string) bool {
	switch name {
	case "web_search", "screenshot_inspect", "upload_inspect", "result_link", "results_find", "result_read",
		"memory_read", "conversation_find", "capability_load", "operation_read":
		return true
	}
	return false
}

// PaidRead reports a read that's a model call, so its interrupted cost can't be established.
func PaidRead(name string) bool { return name == "screenshot_inspect" || name == "upload_inspect" }

// CallRuntimeTool runs a tool from RuntimeTool. It may return a partial result with
// an error, because a failed paid call can still report what it cost.
func (s *Service) CallRuntimeTool(ctx context.Context, run, name string, args map[string]any) (map[string]any, error) {
	text := func(key string) string { value, _ := args[key].(string); return value }
	switch name {
	case "web_search":
		return s.webSearch(ctx, text("query"))
	case "screenshot_inspect":
		return s.inspectScreenshot(ctx, run, text("artifact_id"), text("question"))
	case "upload_inspect":
		return s.uploadInspect(ctx, run, text("upload_id"), text("question"))
	case "result_link":
		return s.resultLink(ctx, run, text("url"), text("title"))
	case "results_find":
		return s.resultsFind(ctx, text("query"))
	case "result_read":
		return s.resultRead(ctx, text("result_id"))
	case "memory_read":
		return s.memoryRead(ctx)
	case "conversation_find":
		return s.conversationFind(ctx, text("query"))
	case "capability_load":
		names := []string{}
		values, _ := args["names"].([]any)
		for _, value := range values {
			name, _ := value.(string)
			names = append(names, name)
		}
		return s.capabilityLoad(names)
	}
	return nil, errors.New(prompts.Runtime("tool-unknown-tool", nil))
}
