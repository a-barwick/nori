package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"nori/internal/extensions"
	"nori/internal/prompts"
)

// Keep unavailable capabilities visible so the model can explain why they cannot run.
type Capabilities struct {
	SearchKey string
	Location  *time.Location
}

type Capability struct {
	Name, Account, Availability, OnFailure, Verify string
	Tools                                          []string
	Default                                        bool
}

const (
	searchCapability     = "Web search"
	attachmentCapability = "Attached files"
	resultCapability     = "Saved results"
	memoryCapability     = "Memory and conversation"
)

func (c Capability) ToolList() string { return strings.Join(c.Tools, ", ") }

func (s *Service) usesNativeWebSearch() bool {
	if s == nil || s.Model == nil {
		return false
	}
	native, ok := s.Model.(NativeWebSearchModel)
	return ok && native.SupportsNativeWebSearch()
}

func (s *Service) catalog(dynamic []extensions.CapabilityState, snapshot *extensions.Snapshot) []Capability {
	tools := map[string][]string{}
	for _, definition := range taskToolDefinitions {
		if definition.capability != "" {
			tools[definition.capability] = append(tools[definition.capability], definition.Name)
		}
	}
	search := prompts.Runtime("capability-available", nil)
	if !s.usesNativeWebSearch() && s.Capabilities.SearchKey == "" {
		search = prompts.Runtime("capability-search-unavailable", nil)
	}
	catalog := []Capability{
		{Name: searchCapability, Tools: tools[searchCapability], Account: prompts.Runtime("catalog-web-search-account", nil), Availability: search, OnFailure: prompts.Runtime("catalog-web-search-onfailure", nil), Verify: prompts.Runtime("catalog-web-search-verify", nil), Default: true},
		{Name: attachmentCapability, Tools: tools[attachmentCapability], Account: prompts.Runtime("catalog-upload-open-account", nil), Availability: prompts.Runtime("capability-available", nil), OnFailure: prompts.Runtime("catalog-upload-open-onfailure", nil), Verify: prompts.Runtime("catalog-upload-open-verify", nil)},
		{Name: resultCapability, Tools: tools[resultCapability], Account: prompts.Runtime("catalog-result-save-account", nil), Availability: prompts.Runtime("capability-available", nil), OnFailure: prompts.Runtime("catalog-result-save-onfailure", nil), Verify: prompts.Runtime("catalog-result-save-verify", nil), Default: true},
		{Name: memoryCapability, Tools: tools[memoryCapability], Account: prompts.Runtime("catalog-memory-read-account", nil), Availability: prompts.Runtime("capability-available", nil), OnFailure: prompts.Runtime("catalog-memory-read-onfailure", nil), Verify: prompts.Runtime("catalog-memory-read-verify", nil), Default: true},
	}
	// Connected apps, MCP servers and plugins. Their text comes from connection metadata only, never from reading
	// the account's content.
	byCapability := map[string][]string{}
	for _, d := range snapshot.Definitions() {
		if !d.Builtin() && d.Exposure != extensions.ExposureDisabled && d.Exposure != extensions.ExposureProgrammatic {
			byCapability[d.Capability] = append(byCapability[d.Capability], d.ModelName)
		}
	}
	for _, state := range dynamic {
		catalog = append(catalog, Capability{
			Name: state.Title, Tools: byCapability[state.Capability], Account: state.Account, Availability: state.Availability,
			OnFailure: "Report the account's state and the repair step Apps shows; do not switch accounts.",
			Verify:    "Read back the provider's record before reporting a change as done.",
		})
	}
	return catalog
}

func (s *Service) location() *time.Location {
	if s.Capabilities.Location != nil {
		return s.Capabilities.Location
	}
	return time.UTC
}

func (s *Service) currentTime() time.Time {
	if s != nil && s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) now() time.Time { return s.currentTime().In(s.location()) }

// catalogInstruction changes at most daily unless an account changes, so it doesn't
// disturb the provider's prompt cache between model calls.
// location is the user's current zone (see Profile); nil means the zone the worker opened the user with.
func (s *Service) catalogInstruction(catalog []Capability, location *time.Location) string {
	if location == nil {
		location = s.location()
	}
	return prompts.Runtime("capability-catalog", map[string]any{
		"Date": s.currentTime().In(location).Format("Monday, 2 January 2006"), "Zone": location.String(), "Capabilities": catalog,
	})
}

// A failed paid call can still return its cost with the error.
func (s *Service) CallRuntimeTool(ctx context.Context, run, name string, args map[string]any) (map[string]any, error) {
	text := func(key string) string { value, _ := args[key].(string); return value }
	switch name {
	case "web_search":
		return s.webSearch(ctx, text("query"))
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
	case "memory_search":
		limit, ok := parseMemorySearchLimit(args["limit"])
		if !ok {
			return nil, errors.New("memory_search limit must be an integer between 1 and 20")
		}
		return s.memorySearch(ctx, text("query"), text("layer"), limit)
	case "memory_get":
		return s.memoryGet(ctx, text("target"), text("date"), text("entry_id"))
	case "conversation_read":
		input, err := s.Store.ReadConversationMessage(ctx, text("message_id"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"message": input, "note": conversationNote}, nil
	case "conversation_find":
		return s.conversationFind(ctx, text("query"))
	case "capability_load":
		names := []string{}
		values, _ := args["names"].([]any)
		for _, value := range values {
			name, _ := value.(string)
			names = append(names, name)
		}
		return s.capabilityLoad(ctx, names)
	}
	return nil, errors.New(prompts.Runtime("tool-unknown-tool", nil))
}
