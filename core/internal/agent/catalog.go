package agent

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Capabilities holds what this worker was configured with. A missing credential
// makes a capability unavailable; it never removes the tool, so the model can tell
// the user the capability is unavailable rather than appear not to have chosen it.
type Capabilities struct {
	SearchKey string
	Calendar  CalendarConfig
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
	search := "available"
	if s.Capabilities.SearchKey == "" {
		search = "unavailable: no API key on this worker"
	}
	account, calendar := s.Capabilities.Calendar.Account, "available"
	if !s.Capabilities.Calendar.connected() {
		account, calendar = "none connected", "unavailable: not connected"
	}
	return []Capability{
		{Name: "Web search", Tools: "web_search", Account: "public web", Availability: search, OnFailure: "report that the search failed; retry only if useful", Verify: "the results are the evidence", Default: true},
		{Name: "Browser", Tools: "browser_navigate, browser_screenshot, screenshot_inspect", Account: "the computer's browser", Availability: "available", OnFailure: "report the page or image as unread", Verify: "the page text or screenshot_inspect answer is the evidence", Default: true},
		{Name: "Attached files", Tools: "upload_open, upload_inspect", Account: "files the user attached to this task", Availability: "available", OnFailure: "say the file couldn't be read; never guess what it says", Verify: "the shell output or upload_inspect answer is the evidence"},
		{Name: "Saved results", Tools: "result_save, result_link, results_find, result_read, result_open", Account: "Nori's saved results", Availability: "available", OnFailure: "say the result couldn't be saved or read", Verify: "the returned result_id is the evidence", Default: true},
		{Name: "Memory and conversation", Tools: "memory_read, conversation_find, operation_read", Account: "Nori's saved memory, conversation and this task's tool outputs", Availability: "available", OnFailure: "say it couldn't be read; never guess what it says", Verify: "the returned memory, message or operation is the evidence", Default: true},
		{Name: "Shell", Tools: "shell, job_start, job_status, job_wait, job_stop", Account: "the computer", Availability: "available", OnFailure: "report the observed exit code or job state; never repeat a write automatically", Verify: "use the job's end state and exit code, and read back what the command changed", Default: true},
		{Name: "Google Calendar read", Tools: "calendar_read", Account: account, Availability: calendar, OnFailure: "tell the user it needs connecting; never guess events", Verify: "the result names the account and range it read"},
		{Name: "Google Calendar write", Tools: "calendar_create, calendar_move", Account: account, Availability: calendar, OnFailure: "report the outcome; never repeat a write automatically", Verify: "the result carries the provider's status, the event ID and a read of the event"},
		{Name: "Email send", Tools: "email_send", Account: account, Availability: calendar, OnFailure: "report the outcome; never repeat a send automatically", Verify: "the result carries the provider's status, the message ID and a read of the sent message"},
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
	var text strings.Builder
	text.WriteString(" Today is " + s.now().Format("Monday, 2 January 2006") + " in " + s.location().String() + ". Capabilities on this worker. " +
		"Those marked load first have no tools until you call capability_load with their names, alone or with other reads:")
	for _, capability := range s.Catalog() {
		loading := "always loaded"
		if !capability.Default {
			loading = "load first"
		}
		text.WriteString("\n- " + capability.Name + " (" + capability.Tools + "; " + loading + "): account " + capability.Account + "; " + capability.Availability + "; on failure, " + capability.OnFailure + "; to confirm, " + capability.Verify + ".")
	}
	return text.String()
}

// RuntimeTool reports a tool the worker runs itself, without the computer.
func RuntimeTool(name string) bool {
	switch name {
	case "web_search", "calendar_read", "screenshot_inspect", "upload_inspect", "result_link", "results_find", "result_read",
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
	case "calendar_read":
		return s.calendarRead(ctx, text("start"), text("end"))
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
	return nil, errors.New("unknown tool")
}
