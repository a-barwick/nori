package plugins

import (
	"context"
	"sort"
	"strings"

	"nori/internal/connections"
)

// mcpMatrix is the capability matrix of a built-in MCP package, so Apps can show it beside the native connectors.
// Capability effects describe what the provider can do; Nori still treats each server tool as unknown effect and asks
// for the user's authority before a change.
type mcpMatrix struct {
	categories   []string
	capabilities []connections.Capability
	limits       string
}

var mcpMatrices = map[string]mcpMatrix{
	"linear/mcp": {categories: []string{"projects"}, limits: "Signs in with your Linear account (1.1) or a Linear API key (1.0). A read-only server is at /mcp/readonly.",
		capabilities: []connections.Capability{
			{ID: "projects.read", Title: "Find issues, projects and comments", Effect: "read", Supported: true},
			{ID: "projects.write", Title: "Create and update issues, projects and comments", Effect: "reversible_write", Supported: true},
		}},
	"github/mcp": {categories: []string{"developer"}, limits: "Needs a fine-grained personal access token. Copilot Business and Enterprise organizations must allow MCP servers; Enterprise Managed Users may have tokens turned off.",
		capabilities: []connections.Capability{
			{ID: "code.read", Title: "Read repositories, issues, pull requests and Actions runs", Effect: "read", Supported: true},
			{ID: "code.write", Title: "Open and comment on issues and pull requests, and other repository changes the token allows", Effect: "unknown", Supported: true},
		}},
	"notion/mcp": {categories: []string{"documents"}, limits: "Connecting with a token needs a Business or Enterprise workspace; a workspace owner creates the MCP token. Guests cannot connect.",
		capabilities: []connections.Capability{
			{ID: "docs.read", Title: "Search the workspace and read pages", Effect: "read", Supported: true},
			{ID: "docs.write", Title: "Create and edit pages and databases", Effect: "reversible_write", Supported: true},
		}},
	"deepwiki/mcp": {categories: []string{"developer"}, limits: "Public repositories only; no sign-in.",
		capabilities: []connections.Capability{{ID: "code.read", Title: "Ask about public GitHub repositories", Effect: "read", Supported: true}}},
}

// IntegrationEntries describes each package that contributes an MCP server as an integration (source "mcp"), with
// its operations, auth requirement and limits. Installed packages carry their installation ID.
func (s *Service) IntegrationEntries(ctx context.Context) ([]connections.IntegrationEntry, error) {
	all, err := s.packages(ctx)
	if err != nil {
		return nil, err
	}
	installs, err := s.Installations(ctx)
	if err != nil {
		return nil, err
	}
	installed := map[string]string{}
	for _, inst := range installs {
		installed[inst.Package.ID] = inst.ID
	}
	latest := map[string]Manifest{}
	for _, doc := range all {
		m := doc.Manifest
		if current, ok := latest[m.Package.ID]; !ok || newer(m.Package.Version, current.Package.Version) {
			latest[m.Package.ID] = m
		}
	}
	out := []connections.IntegrationEntry{}
	for id, m := range latest {
		var server *mcpServerTemplate
		for _, c := range m.Contributions {
			if c.Kind == "mcp_server" && c.Server != nil {
				server = c.Server
			}
		}
		if server == nil {
			continue
		}
		matrix, known := mcpMatrices[id]
		if !known {
			matrix = mcpMatrix{categories: m.Categories, capabilities: []connections.Capability{
				{ID: "tools", Title: "Tools from its MCP server", Effect: "unknown", Supported: true}}}
		}
		auth := map[string]string{"configured": "api_key", "oauth": "oauth"}[server.Auth]
		if auth == "" {
			auth = "none"
		}
		description := m.Description
		if matrix.limits != "" && !strings.Contains(description, matrix.limits) {
			description += " " + matrix.limits
		}
		out = append(out, connections.IntegrationEntry{
			Info: connections.Info{ID: "mcp_" + slugify(id), Title: fallback(m.Title, id), Description: description, Categories: nonNil(matrix.categories),
				Source: "mcp", Auth: auth, Purposes: []connections.Purpose{}, Capabilities: matrix.capabilities, Availability: "available"},
			InstallationID: installed[id], ConnectionIDs: []string{},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out, nil
}
