package api

import (
	"errors"
	"net/http"

	"nori/internal/connectors/mcp"
	"nori/internal/state"
)

func (a *Assistant) registerMCP(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/mcp", a.mcpServers)
	mux.HandleFunc("POST /v1/mcp", jsonBody(a.createMCP))
	mux.HandleFunc("GET /v1/mcp/{id}", a.mcpServer)
	mux.HandleFunc("PUT /v1/mcp/{id}", jsonBody(a.updateMCP))
	mux.HandleFunc("POST /v1/mcp/{id}/actions", jsonBody(a.mcpAction))
	mux.HandleFunc("GET /v1/mcp/{id}/tools", a.mcpTools)
	mux.HandleFunc("POST /v1/mcp/{id}/credential", jsonBody(a.mcpCredential))
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
	writeJSON(w, 200, list[mcp.Server]{Items: items})
}

func (a *Assistant) mcpServer(w http.ResponseWriter, r *http.Request) {
	item, err := a.MCP.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
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
	var value struct {
		Action   string `json:"action"`
		Tool     string `json:"tool,omitempty"`
		Exposure string `json:"exposure,omitempty"`
	}
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
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
	var value struct {
		Token string `json:"token"`
	}
	requestID, _, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if err := a.MCP.SetCredential(r.Context(), r.PathValue("id"), value.Token); err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	writeJSON(w, 200, map[string]any{"request_id": requestID, "accepted": true, "status": "applied", "resource": map[string]string{"id": r.PathValue("id")}})
}
