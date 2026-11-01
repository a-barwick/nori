package agent

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Capabilities holds what this worker was configured with. A missing credential
// makes a capability unavailable; it never removes the tool, so the model can tell
// Austin the capability is unavailable rather than appear not to have chosen it.
type Capabilities struct {
	SearchKey string
	Calendar  CalendarConfig
	Location  *time.Location
}

// Capability is one catalog entry. Listing a capability grants nothing: the runtime
// still checks each call when it runs. A capability that changes something must name
// how to confirm the change in Verify, such as a status and ID or a follow-up read.
type Capability struct {
	Name, Tools, Account, Availability, OnFailure, Verify string
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
		{Name: "Web search", Tools: "web_search", Account: "public web", Availability: search, OnFailure: "report that the search failed; retry only if useful", Verify: "the results are the evidence"},
		{Name: "Browser", Tools: "browser_navigate, browser_screenshot, screenshot_inspect", Account: "the computer's browser", Availability: "available", OnFailure: "report the page or image as unread", Verify: "the page text or screenshot_inspect answer is the evidence"},
		{Name: "Shell", Tools: "shell, job_start, job_status, job_wait, job_stop", Account: "the computer", Availability: "available", OnFailure: "report the observed exit code or job state; never repeat a write automatically", Verify: "use the job's end state and exit code, and read back what the command changed"},
		{Name: "Google Calendar read", Tools: "calendar_read", Account: account, Availability: calendar, OnFailure: "tell Austin it needs connecting; never guess events", Verify: "the result names the account and range it read"},
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
	text.WriteString(" Today is " + s.now().Format("Monday, 2 January 2006") + " in " + s.location().String() + ". Capabilities on this worker:")
	for _, capability := range s.Catalog() {
		text.WriteString("\n- " + capability.Name + " (" + capability.Tools + "): account " + capability.Account + "; " + capability.Availability + "; on failure, " + capability.OnFailure + "; to confirm, " + capability.Verify + ".")
	}
	return text.String()
}

// RuntimeTool reports a tool the worker runs itself, without the computer.
func RuntimeTool(name string) bool {
	switch name {
	case "web_search", "calendar_read", "screenshot_inspect":
		return true
	}
	return false
}

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
	}
	return nil, errors.New("unknown tool")
}
