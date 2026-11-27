package conversation

type ModelSummary struct {
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
	Location string `json:"location,omitempty"`
}

type ConnectedAppSummary struct {
	App          string   `json:"app"`
	Account      string   `json:"account,omitempty"`
	Status       string   `json:"status"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type MCPServerSummary struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	ToolCount int    `json:"tool_count,omitempty"`
}

type CapabilitySummary struct {
	WebSearch     string                `json:"web_search"`
	UploadedFiles []string              `json:"uploaded_files"`
	BuiltIn       []string              `json:"built_in"`
	ConnectedApps []ConnectedAppSummary `json:"connected_apps"`
	MCPServers    []MCPServerSummary    `json:"mcp_servers"`
}
