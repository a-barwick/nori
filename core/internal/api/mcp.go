package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"nori/internal/connectors/mcp"
	"nori/internal/state"
)

func (a *Assistant) registerMCP(mux *routeMux) {
	mux.handle("GET /v1/mcp", browserRead, a.mcpServers)
	mux.handle("POST /v1/mcp", browserJSON, jsonBody(a.createMCP))
	mux.handle("GET /v1/mcp/{id}", browserRead, a.mcpServer)
	mux.handle("PUT /v1/mcp/{id}", browserJSON, jsonBody(a.updateMCP))
	mux.handle("POST /v1/mcp/{id}/actions", browserJSON, jsonBody(a.mcpAction))
	mux.handle("GET /v1/mcp/{id}/tools", browserRead, a.mcpTools)
	mux.handle("POST /v1/mcp/{id}/credential", browserJSON, jsonBody(a.mcpCredential))
}

func mcpError(w http.ResponseWriter, err error, requestID string, current any) {
	if errors.Is(err, mcp.ErrUnsupported) {
		writeJSON(w, 400, apiError{Error: err.Error(), Code: "unsupported", RequestID: requestID})
		return
	}
	writeAPIError(w, err, requestID, current)
}

func (a *Assistant) mcpServers(w http.ResponseWriter, r *http.Request) {
	items, err := a.MCP.List(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	sources := a.serverSources(r.Context())
	for i := range items {
		applySource(&items[i], sources)
	}
	writeJSON(w, 200, list[mcp.Server]{Items: items})
}

// serverSources maps MCP servers to the package installations that contributed them, so a server saved before
// installations recorded themselves still names its package.
func (a *Assistant) serverSources(ctx context.Context) map[string]string {
	if a.Plugins == nil {
		return nil
	}
	sources, _ := a.Plugins.ServerSources(ctx)
	return sources
}

func applySource(server *mcp.Server, sources map[string]string) {
	if installation, ok := sources[server.ID]; ok && server.Source.Kind != "installation" {
		server.Source = mcp.Source{Kind: "installation", InstallationID: installation}
	}
}

func (a *Assistant) mcpServer(w http.ResponseWriter, r *http.Request) {
	item, err := a.MCP.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	applySource(&item, a.serverSources(r.Context()))
	writeJSON(w, 200, item)
}

func (a *Assistant) createMCP(w http.ResponseWriter, r *http.Request) {
	var value mcp.Editable
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if expected != 0 {
		writeAPIError(w, state.ErrRevisionConflict, requestID, nil)
		return
	}
	receipt, err := a.MCP.Save(r.Context(), "", requestID, 0, value)
	if err != nil {
		mcpError(w, err, requestID, nil)
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) updateMCP(w http.ResponseWriter, r *http.Request) {
	var value mcp.Editable
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	current, err := a.MCP.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	receipt, err := a.MCP.Save(r.Context(), r.PathValue("id"), requestID, expected, value)
	if err != nil {
		mcpError(w, err, requestID, current)
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) mcpAction(w http.ResponseWriter, r *http.Request) {
	var value mcp.Action
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if !value.Valid() {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	current, err := a.MCP.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	receipt, err := a.MCP.Act(r.Context(), r.PathValue("id"), requestID, expected, value.Action, value.Tool, value.Exposure)
	if err != nil {
		mcpError(w, err, requestID, current)
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) mcpTools(w http.ResponseWriter, r *http.Request) {
	items, err := a.MCP.Tools(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, list[mcp.Tool]{Items: items})
}

// mcpCredential stores a configured server credential. It is write-only: no route ever returns it.
func (a *Assistant) mcpCredential(w http.ResponseWriter, r *http.Request) {
	var value mcp.CredentialInput
	if !decode(w, r, &value) {
		return
	}
	if !validID(value.RequestID) || strings.TrimSpace(value.Token) == "" || len(value.Token) > 8192 {
		writeAPIError(w, state.ErrInvalidResource, value.RequestID, nil)
		return
	}
	if err := a.MCP.SetCredential(r.Context(), r.PathValue("id"), value.Token); err != nil {
		writeAPIError(w, err, value.RequestID, nil)
		return
	}
	server, err := a.MCP.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err, value.RequestID, nil)
		return
	}
	data, _ := jsonMarshal(server)
	writeJSON(w, 200, state.MutationReceipt{RequestID: value.RequestID, Accepted: true, Status: "applied", Resource: data})
}
