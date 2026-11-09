package plugins

// Builtin is the first-party registry catalog. Its packages are verified and contribute instructions and remote
// MCP configurations only; they carry no executable code.
var Builtin = []Manifest{
	{
		SchemaVersion: 1, Package: Ref{ID: "nori/text-stats", Version: "1.0.0"}, Publisher: map[string]any{"id": "nori", "name": "Nori"},
		Title: "Text statistics", Description: "An executable example package: counts words, lines and characters in text, in the computer's sandbox.",
		Categories: []string{"developer"}, ContractVersion: "r2",
		RequestedAccess: Access{ConnectionTypes: []string{}, ActionClasses: []string{"unknown"}},
		Executor: &Executor{Kind: "isolated", Runtime: "python3", Entry: "main.py", Limits: &struct {
			MemoryMB int `json:"memory_mb,omitempty"`
			CPUMS    int `json:"cpu_ms,omitempty"`
			WallMS   int `json:"wall_ms,omitempty"`
		}{MemoryMB: 64, WallMS: 5000}},
		Files: map[string]string{"main.py": `import json, sys
request = json.load(sys.stdin)
args = request.get("arguments", {})
text = args.get("text", "")
if args.get("loop_forever"):
    while True:
        pass
if args.get("allocate_mb"):
    hog = bytearray(int(args["allocate_mb"]) * 1024 * 1024)
print(json.dumps({"words": len(text.split()), "lines": len(text.splitlines()), "characters": len(text)}))
`},
		Contributions: []Contribution{{Kind: "tool", Tool: []byte(`{"model_name": "text_stats", "title": "Text statistics", "description": "Count words, lines and characters in a text.", "input_schema": {"type": "object", "properties": {"text": {"type": "string"}, "loop_forever": {"type": "boolean"}, "allocate_mb": {"type": "integer"}}, "required": ["text"], "additionalProperties": false}}`)}},
	},
	{
		SchemaVersion: 1, Package: Ref{ID: "nori/text-stats", Version: "1.1.0"}, Publisher: map[string]any{"id": "nori", "name": "Nori"},
		Title: "Text statistics", Description: "Counts words, lines and characters in text, in the computer's sandbox. 1.1 adds a policy hook that refuses texts over 20,000 characters and a result hook that adds a one-line summary.",
		Categories: []string{"developer"}, ContractVersion: "r2",
		RequestedAccess: Access{ConnectionTypes: []string{}, ActionClasses: []string{"unknown"}},
		Executor: &Executor{Kind: "isolated", Runtime: "python3", Entry: "main.py", Limits: &struct {
			MemoryMB int `json:"memory_mb,omitempty"`
			CPUMS    int `json:"cpu_ms,omitempty"`
			WallMS   int `json:"wall_ms,omitempty"`
		}{MemoryMB: 64, WallMS: 5000}},
		Files: map[string]string{"main.py": `import json, sys, time
request = json.load(sys.stdin)
args = request.get("arguments", {})
text = args.get("text", "")
hook = request.get("hook")
if hook == "pre_invocation":
    if args.get("slow_policy"):
        time.sleep(30)
    if len(text) > 20000:
        print(json.dumps({"allow": False, "reason": "texts over 20,000 characters are refused by this package"}))
    else:
        print(json.dumps({"allow": True}))
    sys.exit(0)
if hook == "result":
    r = request.get("result") or {}
    r = dict(r, summary="%d words on %d lines" % (r.get("words", 0), r.get("lines", 0)))
    print(json.dumps({"result": r}))
    sys.exit(0)
if args.get("loop_forever"):
    while True:
        pass
if args.get("allocate_mb"):
    hog = bytearray(int(args["allocate_mb"]) * 1024 * 1024)
print(json.dumps({"words": len(text.split()), "lines": len(text.splitlines()), "characters": len(text)}))
`},
		Contributions: []Contribution{
			{Kind: "tool", Tool: []byte(`{"model_name": "text_stats", "title": "Text statistics", "description": "Count words, lines and characters in a text.", "input_schema": {"type": "object", "properties": {"text": {"type": "string"}, "loop_forever": {"type": "boolean"}, "allocate_mb": {"type": "integer"}, "slow_policy": {"type": "boolean"}}, "required": ["text"], "additionalProperties": false}}`)},
			{Kind: "hook", Hook: HookPreInvocation, Handler: "size_policy", DeadlineMS: 2000},
			{Kind: "hook", Hook: HookResult, Handler: "summary", DeadlineMS: 2000},
		},
	},
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
	// First-party remote MCP servers that accept a bearer credential, which Nori's MCP client supports. Their tools
	// arrive with unknown effect and need the user's authority like any external tool. Providers whose servers need
	// their own OAuth app (Slack, Google Drive, Dropbox) or Microsoft's Agent 365 preview (Teams, OneDrive) are not
	// packaged yet; see the capability matrix in the backend provider notes.
	{
		SchemaVersion: 1, Package: Ref{ID: "linear/mcp", Version: "1.0.0"}, Publisher: map[string]any{"id": "linear", "name": "Linear"},
		Title: "Linear", Description: "Find, create and update Linear issues, projects and comments through Linear's MCP server (linear.app/docs/mcp). Connect with a Linear API key from Settings → Security & access; a read-only server is at /mcp/readonly.",
		Categories: []string{"productivity", "developer"}, ContractVersion: "r2",
		RequestedAccess: Access{ConnectionTypes: []string{}, ActionClasses: []string{"read", "unknown"}, Network: []string{"mcp.linear.app"}},
		Contributions:   []Contribution{{Kind: "mcp_server", Server: &mcpServerTemplate{Name: "Linear", Transport: "http", Endpoint: "https://mcp.linear.app/mcp", Auth: "configured"}}},
	},
	{
		SchemaVersion: 1, Package: Ref{ID: "linear/mcp", Version: "1.1.0"}, Publisher: map[string]any{"id": "linear", "name": "Linear"},
		Title: "Linear", Description: "Find, create and update Linear issues, projects and comments through Linear's MCP server (linear.app/docs/mcp). 1.1 signs in with your Linear account (OAuth with dynamic registration) instead of an API key.",
		Categories: []string{"productivity", "developer"}, ContractVersion: "r2",
		RequestedAccess: Access{ConnectionTypes: []string{}, ActionClasses: []string{"read", "unknown"}, Network: []string{"mcp.linear.app"}},
		Contributions:   []Contribution{{Kind: "mcp_server", Server: &mcpServerTemplate{Name: "Linear", Transport: "http", Endpoint: "https://mcp.linear.app/mcp", Auth: "oauth"}}},
	},
	{
		SchemaVersion: 1, Package: Ref{ID: "github/mcp", Version: "1.0.0"}, Publisher: map[string]any{"id": "github", "name": "GitHub"},
		Title: "GitHub", Description: "Repositories, issues, pull requests and Actions through GitHub's remote MCP server (github/github-mcp-server docs/remote-server.md). Connect with a fine-grained personal access token. Organizations on Copilot Business or Enterprise must allow MCP servers; Enterprise Managed Users may have tokens disabled.",
		Categories: []string{"developer"}, ContractVersion: "r2",
		RequestedAccess: Access{ConnectionTypes: []string{}, ActionClasses: []string{"read", "unknown"}, Network: []string{"api.githubcopilot.com"}},
		Contributions:   []Contribution{{Kind: "mcp_server", Server: &mcpServerTemplate{Name: "GitHub", Transport: "http", Endpoint: "https://api.githubcopilot.com/mcp/", Auth: "configured"}}},
	},
	{
		SchemaVersion: 1, Package: Ref{ID: "notion/mcp", Version: "1.0.0"}, Publisher: map[string]any{"id": "notion", "name": "Notion"},
		Title: "Notion", Description: "Search the workspace and create or edit pages and databases through Notion's MCP server (developers.notion.com/docs/mcp). Connecting with a token needs a Business or Enterprise workspace: a workspace owner creates an MCP token in the Developer portal. Guests cannot connect.",
		Categories: []string{"productivity"}, ContractVersion: "r2",
		RequestedAccess: Access{ConnectionTypes: []string{}, ActionClasses: []string{"read", "unknown"}, Network: []string{"mcp.notion.com"}},
		Contributions:   []Contribution{{Kind: "mcp_server", Server: &mcpServerTemplate{Name: "Notion", Transport: "http", Endpoint: "https://mcp.notion.com/mcp", Auth: "configured"}}},
	},
}
