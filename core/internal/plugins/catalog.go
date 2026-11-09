package plugins

// Builtin is the first-party registry catalog. Its packages are verified and contribute instructions and remote
// MCP configurations only; they carry no executable code.
var Builtin = []Manifest{
	{
		SchemaVersion: 1, Package: Ref{ID: "nori/morning-briefing", Version: "1.0.0"}, Publisher: map[string]any{"id": "nori", "name": "Nori"},
		Title: "Morning briefing", Description: "A skill that prepares a short briefing from the user's agenda, urgent mail and goal blockers.",
		Categories: []string{"productivity"}, ContractVersion: "r2",
		RequestedAccess: Access{ConnectionTypes: []string{"google"}, ActionClasses: []string{"read"}},
		Contributions: []Contribution{{Kind: "skill", Skill: &Skill{Title: "Morning briefing", Description: "Today's agenda, urgent messages and goal blockers.",
			Instructions:    "Read today's events from the user's selected calendars, search mail from the last day for messages that need a reply, and list active goals with blockers. Report each item with its source link. Do not change anything.",
			RequiredToolIDs: []string{"google/calendar/calendar-events", "google/mail/mail-search", "nori/assistant/goals-list"}, Scope: "user"}}},
	},
	{
		SchemaVersion: 1, Package: Ref{ID: "nori/morning-briefing", Version: "1.1.0"}, Publisher: map[string]any{"id": "nori", "name": "Nori"},
		Title: "Morning briefing", Description: "A skill that prepares a short briefing and can draft replies to urgent mail.",
		Categories: []string{"productivity"}, ContractVersion: "r2",
		RequestedAccess: Access{ConnectionTypes: []string{"google"}, ActionClasses: []string{"read", "reversible_write"}},
		Contributions: []Contribution{{Kind: "skill", Skill: &Skill{Title: "Morning briefing", Description: "Today's agenda, urgent messages, goal blockers and draft replies.",
			Instructions:    "Read today's events from the user's selected calendars, search mail from the last day for messages that need a reply, and list active goals with blockers. Draft (never send) a reply to each urgent message. Report each item with its source link.",
			RequiredToolIDs: []string{"google/calendar/calendar-events", "google/mail/mail-search", "google/mail/mail-draft-create", "nori/assistant/goals-list"}, Scope: "user"}}},
	},
	{
		SchemaVersion: 1, Package: Ref{ID: "deepwiki/mcp", Version: "1.0.0"}, Publisher: map[string]any{"id": "deepwiki", "name": "DeepWiki"},
		Title: "DeepWiki", Description: "Ask questions about public GitHub repositories through DeepWiki's MCP server.",
		Categories: []string{"developer"}, ContractVersion: "r2",
		RequestedAccess: Access{ConnectionTypes: []string{}, ActionClasses: []string{"read"}, Network: []string{"mcp.deepwiki.com"}},
		Contributions:   []Contribution{{Kind: "mcp_server", Server: &mcpServerTemplate{Name: "DeepWiki", Transport: "http", Endpoint: "https://mcp.deepwiki.com/mcp", Auth: "none"}}},
	},
}
