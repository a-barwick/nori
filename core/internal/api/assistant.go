package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"nori/internal/connections"
	"nori/internal/connectors/mcp"
	"nori/internal/conversation"
	"nori/internal/extensions"
	"nori/internal/httpjson"
	"nori/internal/modelkeys"
	"nori/internal/modelprofiles"
	"nori/internal/permissions"
	"nori/internal/scheduler"
	"nori/internal/secret"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

const AssistantContract = "nori-assistant-1"

var (
	ErrAccountDeleteOwnerForbidden = errors.New("The legacy owner account cannot be deleted from self-service settings.")
	ErrAccountDeleteEmailMismatch  = errors.New("Enter your account email to confirm deletion.")
	ErrAccountDeletePending        = errors.New("Your account deletion is incomplete. Finish deleting your account to continue.")
	ErrAccountDeleteNotPending     = errors.New("There is no account deletion to finish.")
)

type Assistant struct {
	Store               state.ConversationStore
	Connections         *connections.Service
	Permissions         *permissions.Service
	Tools               func(ctx context.Context) (*extensions.Snapshot, []extensions.CapabilityState)
	Answer              func(ctx context.Context, commandID, taskID, questionID, text string) error
	Revise              func(ctx context.Context, taskID, resultID, instruction string) (state.Run, error)
	DeliverAnswers      func(ctx context.Context) error
	ScheduleAnswerRetry func(ctx context.Context)
	Resume              func(ctx context.Context) error
	Continue            func(ctx context.Context, taskID, previous, request string, budget float64) (state.Run, error)
	Stop                func(ctx context.Context, commandID, taskID string) (state.Run, error)
	Features            map[string]bool
	Scheduler           *scheduler.Service
	MCP                 *mcp.Service
	ModelKeys           *modelkeys.Service
	Models              *modelprofiles.Account
	HomeAI              *HomeAISettings
	AccountBYOK         bool
	UnattendedWrites    string
	NextScheduled       func(ctx context.Context) *time.Time
	InboxUnread         func(ctx context.Context) int
	ConsolidateMemory   func(ctx context.Context) (state.ConversationSummary, error)
	NormalizeMemory     func(ctx context.Context, input conversation.ManualMemoryInput) (conversation.NormalizedMemory, error)
	DeleteAccount       func(ctx context.Context, confirmation string, resumePending bool) error
	Extra               []func(mux *http.ServeMux, a *Assistant)
}

type apiError struct {
	Error     string          `json:"error"`
	Code      string          `json:"code"`
	Retryable bool            `json:"retryable"`
	Accepted  bool            `json:"accepted"`
	RequestID string          `json:"request_id,omitempty"`
	Current   json.RawMessage `json:"current,omitempty"`
	Receipt   json.RawMessage `json:"receipt,omitempty"`
}

func writeAPIError(w http.ResponseWriter, err error, requestID string, current any) {
	status, code, message, retryable := 502, "internal", "Nori could not finish this request.", true
	switch {
	case errors.Is(err, state.ErrNotFound):
		status, code, message, retryable = 404, "not_found", "Not found.", false
	case errors.Is(err, state.ErrRevisionConflict):
		status, code, message, retryable = 409, "revision_conflict", "It changed since you loaded it. Review the current version.", false
	case errors.Is(err, state.ErrIdempotencyConflict):
		status, code, message, retryable, current = 409, "idempotency_conflict", "This request ID was already used for a different change.", false, nil
	case errors.Is(err, permissions.ErrInteractionStale):
		status, code, message, retryable = 409, "interaction_stale", "This question or approval is no longer pending.", false
	case errors.Is(err, httpjson.ErrTooLarge):
		status, code, message, retryable = http.StatusRequestEntityTooLarge, "payload_too_large", "Request body is too large.", false
	case errors.Is(err, state.ErrInvalidResource), errors.Is(err, connections.ErrInvalidAction), errors.Is(err, connections.ErrUnknownIntegration),
		errors.Is(err, modelkeys.ErrUnknownProvider), errors.Is(err, modelkeys.ErrEmptyAPIKey):
		status, code, message, retryable = 400, "validation_failed", err.Error(), false
	case errors.Is(err, modelkeys.ErrInvalidAPIKey):
		status, code, message, retryable = http.StatusUnprocessableEntity, "invalid_api_key", err.Error(), false
	case errors.Is(err, modelkeys.ErrModelAccess), errors.Is(err, modelkeys.ErrMissingAPIKey):
		status, code, message, retryable = http.StatusUnprocessableEntity, "missing_api_key", err.Error(), false
	case errors.Is(err, modelkeys.ErrValidationFailed):
		status, code, message, retryable = http.StatusServiceUnavailable, "provider_unavailable", "Could not reach the model provider to validate your API key. Try again shortly.", true
	case errors.Is(err, secret.ErrCredentialUnavailable):
		status, code, message, retryable = http.StatusServiceUnavailable, "credential_store_unavailable", "Credential storage is temporarily unavailable. Try again shortly.", true
	case errors.Is(err, connections.ErrBadPassword):
		status, code, message, retryable = 400, "validation_failed", "The provider rejected the username or app-specific password. Create a new app-specific password and try again.", false
	case errors.Is(err, connections.ErrNotConfigured):
		status, code, message, retryable = 503, "provider_unavailable", err.Error(), false
	case errors.Is(err, connections.ErrNotConnected):
		status, code, message, retryable = 409, "not_connected", err.Error(), false
	}
	if status >= 500 {
		attrs := []any{"status", status, "code", code, "err", err}
		if requestID != "" {
			attrs = append(attrs, "request_id", requestID)
		}
		slog.Error("assistant request failed", attrs...)
	}
	body := apiError{Error: message, Code: code, Retryable: retryable, RequestID: requestID}
	if current != nil && status == 409 {
		body.Current, _ = json.Marshal(current)
	}
	writeJSON(w, status, body)
}

type changeRequest struct {
	RequestID        string          `json:"request_id"`
	ExpectedRevision *int64          `json:"expected_revision"`
	Value            json.RawMessage `json:"value"`
}

func decodeChange(w http.ResponseWriter, r *http.Request, value any) (string, int64, bool) {
	var req changeRequest
	if err := httpjson.Decode(w, r, &req, 64<<10); err != nil {
		if errors.Is(err, httpjson.ErrTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiError{Error: "Request body is too large.", Code: "payload_too_large"})
			return "", 0, false
		}
		message := "Send a valid JSON object."
		if errors.Is(err, httpjson.ErrMultiple) {
			message = "Send one JSON object."
		}
		writeJSON(w, http.StatusBadRequest, apiError{Error: message, Code: "validation_failed"})
		return "", 0, false
	}
	trimmed := bytes.TrimSpace(req.Value)
	if !validID(req.RequestID) || req.ExpectedRevision == nil || *req.ExpectedRevision < 0 || len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		writeAPIError(w, state.ErrInvalidResource, req.RequestID, nil)
		return "", 0, false
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeJSON(w, 400, apiError{Error: "The value does not match this request's schema.", Code: "validation_failed", RequestID: req.RequestID})
		return "", 0, false
	}
	return req.RequestID, *req.ExpectedRevision, true
}

func writeReceipt(w http.ResponseWriter, receipt state.MutationReceipt) {
	status := receipt.HTTPStatus
	if status == 0 {
		status = 200
	}
	writeJSON(w, status, receipt)
}

type list[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func (a *Assistant) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/integrations", a.integrations)
	mux.HandleFunc("GET /v1/integrations/{id}", a.integration)
	mux.HandleFunc("GET /v1/connections", a.connections)
	mux.HandleFunc("POST /v1/connections", jsonBody(a.createConnection))
	mux.HandleFunc("GET /v1/connections/oauth/callback", a.oauthCallback)
	mux.HandleFunc("GET /v1/connections/{id}", a.connection)
	mux.HandleFunc("PUT /v1/connections/{id}", jsonBody(a.updateConnection))
	mux.HandleFunc("POST /v1/connections/{id}/actions", jsonBody(a.connectionAction))
	mux.HandleFunc("GET /v1/connections/{id}/resources", a.connectionResources)
	mux.HandleFunc("GET /v1/preferences", a.preferences)
	mux.HandleFunc("PUT /v1/preferences", jsonBody(a.savePreferences))
	mux.HandleFunc("GET /v1/permissions", a.rules)
	mux.HandleFunc("POST /v1/permissions", jsonBody(a.createRule))
	mux.HandleFunc("GET /v1/permissions/{id}", a.rule)
	mux.HandleFunc("PUT /v1/permissions/{id}", jsonBody(a.updateRule))
	mux.HandleFunc("POST /v1/permissions/{id}/actions", jsonBody(a.ruleAction))
	mux.HandleFunc("GET /v1/interactions", a.interactions)
	mux.HandleFunc("GET /v1/interactions/{id}", a.interaction)
	mux.HandleFunc("POST /v1/interactions/{id}/responses", jsonBody(a.respond))
	mux.HandleFunc("GET /v1/mutations/{request_id}", a.mutation)
	mux.HandleFunc("GET /v1/tools", a.tools)
	mux.HandleFunc("GET /v1/tools/{id...}", a.tool)
	mux.HandleFunc("GET /v1/work", a.work)
	mux.HandleFunc("GET /v1/work/{task_id}", a.workDetail)
	mux.HandleFunc("GET /v1/work/{task_id}/operations", a.workOperations)
	mux.HandleFunc("POST /v1/work/{task_id}/actions", jsonBody(a.workAction))
	mux.HandleFunc("GET /v1/operations", a.operations)
	mux.HandleFunc("GET /v1/assistant/summary", a.summary)
	mux.HandleFunc("GET /v1/library", a.library)
	mux.HandleFunc("GET /v1/library/{id}", a.libraryGet)
	mux.HandleFunc("PUT /v1/library/{id}", jsonBody(a.libraryRename))
	mux.HandleFunc("GET /v1/library/{id}/versions", a.libraryVersions)
	mux.HandleFunc("GET /v1/library/{id}/file", a.libraryFile)
	mux.HandleFunc("POST /v1/library/{id}/actions", jsonBody(a.libraryAction))
	mux.HandleFunc("GET /v1/search", a.search)
	mux.HandleFunc("GET /v1/memory", a.memory)
	mux.HandleFunc("POST /v1/memory", jsonBody(a.createMemory))
	mux.HandleFunc("POST /v1/memory/actions", jsonBody(a.memoryCollectionAction))
	mux.HandleFunc("GET /v1/memory/search", a.searchMemory)
	mux.HandleFunc("GET /v1/memory/logs", a.memoryLogs)
	mux.HandleFunc("GET /v1/memory/logs/{date}", a.memoryLog)
	mux.HandleFunc("POST /v1/memory/logs/{date}/{entry_id}/actions", jsonBody(a.memoryLogEntryAction))
	mux.HandleFunc("GET /v1/memory/working", a.workingMemory)
	mux.HandleFunc("POST /v1/memory/working/{item_id}/actions", jsonBody(a.workingMemoryItemAction))
	mux.HandleFunc("PUT /v1/memory/{id}", jsonBody(a.editMemory))
	mux.HandleFunc("POST /v1/memory/{id}/actions", jsonBody(a.memoryAction))
	if a.Scheduler != nil {
		a.registerScheduling(mux)
	}
	if a.MCP != nil {
		a.registerMCP(mux)
	}
	if a.ModelKeys != nil {
		a.registerModelKeys(mux)
	}
	if a.Models != nil {
		a.registerModelSettings(mux)
	}
	if a.HomeAI != nil {
		a.HomeAI.register(mux)
	}
	mux.HandleFunc("GET /v1/diagnostics", a.diagnostics)
	mux.HandleFunc("POST /v1/diagnostics/actions", jsonBody(a.diagnosticsAction))
	mux.HandleFunc("POST /v1/feedback", jsonBody(a.feedback))
	mux.HandleFunc("POST /v1/settings/account/delete", jsonBody(a.deleteAccount))
	for _, extra := range a.Extra {
		extra(mux, a)
	}
}

func (a *Assistant) deleteAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Confirmation  string `json:"confirmation"`
		ResumePending bool   `json:"resume_pending,omitempty"`
	}
	if err := httpjson.Decode(w, r, &body, 64<<10); err != nil {
		if errors.Is(err, httpjson.ErrTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiError{Error: "Request body is too large.", Code: "payload_too_large"})
			return
		}
		message := "Send a valid JSON object."
		if errors.Is(err, httpjson.ErrMultiple) {
			message = "Send one JSON object."
		}
		writeJSON(w, http.StatusBadRequest, apiError{Error: message, Code: "validation_failed"})
		return
	}
	if strings.TrimSpace(body.Confirmation) == "" {
		writeJSON(w, http.StatusBadRequest, apiError{Error: ErrAccountDeleteEmailMismatch.Error(), Code: "validation_failed"})
		return
	}
	if a.DeleteAccount == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Error: "Account deletion is unavailable.", Code: "provider_unavailable"})
		return
	}
	if err := a.DeleteAccount(r.Context(), body.Confirmation, body.ResumePending); err != nil {
		switch {
		case errors.Is(err, ErrAccountDeleteOwnerForbidden):
			writeJSON(w, http.StatusForbidden, apiError{Error: ErrAccountDeleteOwnerForbidden.Error(), Code: "account_delete_forbidden"})
		case errors.Is(err, ErrAccountDeleteEmailMismatch):
			writeJSON(w, http.StatusBadRequest, apiError{Error: ErrAccountDeleteEmailMismatch.Error(), Code: "validation_failed"})
		case errors.Is(err, docstore.ErrUserDataBusy):
			writeJSON(w, http.StatusConflict, apiError{Error: docstore.ErrUserDataBusy.Error(), Code: "account_delete_busy", Retryable: true})
		case errors.Is(err, ErrAccountDeleteNotPending):
			writeJSON(w, http.StatusConflict, apiError{Error: ErrAccountDeleteNotPending.Error(), Code: "account_delete_not_pending"})
		case errors.Is(err, ErrAccountDeletePending):
			slog.ErrorContext(r.Context(), "account deletion incomplete", "err", err)
			writeJSON(w, http.StatusBadGateway, apiError{Error: ErrAccountDeletePending.Error(), Code: "account_delete_pending", Retryable: true})
		case errors.Is(err, state.ErrNotFound), errors.Is(err, docstore.ErrNotFound):
			writeJSON(w, http.StatusNotFound, apiError{Error: "Not found.", Code: "not_found"})
		default:
			slog.ErrorContext(r.Context(), "account deletion failed", "err", err)
			writeJSON(w, http.StatusBadGateway, apiError{Error: "Nori could not delete your account. Try again.", Code: "internal", Retryable: true})
		}
		return
	}
	slog.InfoContext(r.Context(), "account deletion completed")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (a *Assistant) integrations(w http.ResponseWriter, r *http.Request) {
	query, category := r.URL.Query().Get("q"), r.URL.Query().Get("category")
	entries, err := a.Connections.Catalog(r.Context(), query, category)
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, list[connections.IntegrationEntry]{Items: entries})
}

func (a *Assistant) integration(w http.ResponseWriter, r *http.Request) {
	entry, err := a.Connections.Integration(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, entry)
}

func (a *Assistant) connections(w http.ResponseWriter, r *http.Request) {
	items, err := a.Connections.List(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	if integration := r.URL.Query().Get("integration_id"); integration != "" {
		filtered := items[:0]
		for _, item := range items {
			if item.IntegrationID == integration {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	writeJSON(w, 200, list[connections.Projection]{Items: items})
}

func (a *Assistant) connection(w http.ResponseWriter, r *http.Request) {
	item, err := a.Connections.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, item)
}

func (a *Assistant) currentConnection(ctx context.Context, id string) any {
	item, err := a.Connections.Get(ctx, id)
	if err != nil {
		return nil
	}
	return item
}

func (a *Assistant) createConnection(w http.ResponseWriter, r *http.Request) {
	var value connections.Editable
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if expected != 0 {
		writeAPIError(w, state.ErrRevisionConflict, requestID, nil)
		return
	}
	receipt, err := a.Connections.Create(r.Context(), requestID, value)
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) updateConnection(w http.ResponseWriter, r *http.Request) {
	var value connections.Editable
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	receipt, err := a.Connections.Update(r.Context(), r.PathValue("id"), requestID, expected, value)
	if err != nil {
		writeAPIError(w, err, requestID, a.currentConnection(r.Context(), r.PathValue("id")))
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) connectionAction(w http.ResponseWriter, r *http.Request) {
	var value connections.Action
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if value.ReturnTo != "" && !validReturnTo(value.ReturnTo) {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	receipt, err := a.Connections.Act(r.Context(), r.PathValue("id"), requestID, expected, value)
	if err != nil {
		writeAPIError(w, err, requestID, a.currentConnection(r.Context(), r.PathValue("id")))
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) connectionResources(w http.ResponseWriter, r *http.Request) {
	items, err := a.Connections.Resources(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, list[connections.Resource]{Items: items})
}

func (a *Assistant) oauthCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	state, code, providerError := query.Get("state"), query.Get("code"), query.Get("error")
	if len(state) < 16 || (code == "") == (providerError == "") {
		writeJSON(w, 400, apiError{Error: "This sign-in link is incomplete.", Code: "validation_failed"})
		return
	}
	if a.MCP != nil {
		location, err := a.MCP.Callback(r.Context(), state, code, providerError)
		if err == nil {
			callbackRedirect(w, r, location)
			return
		}
		if !errors.Is(err, mcp.ErrNotMCPAttempt) {
			writeAPIError(w, err, "", nil)
			return
		}
	}
	location, err := a.Connections.Callback(r.Context(), state, code, providerError)
	if errors.Is(err, connections.ErrAttemptInvalid) {
		callbackRedirect(w, r, "/apps?result=failed")
		return
	}
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	callbackRedirect(w, r, location)
}

func callbackRedirect(w http.ResponseWriter, r *http.Request, location string) {
	if r.Header.Get("X-Nori-Redirect") == "json" || r.URL.Query().Get("nori_redirect") == "json" {
		writeJSON(w, 200, map[string]string{"location": location})
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (a *Assistant) preferences(w http.ResponseWriter, r *http.Request) {
	prefs, _, err := a.Connections.Preferences(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	prefs.DataResetPending = userDataResetPending(r.Context())
	writeJSON(w, 200, prefs)
}

func (a *Assistant) savePreferences(w http.ResponseWriter, r *http.Request) {
	var value connections.PreferencesEditable
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	receipt, err := a.Connections.SavePreferences(r.Context(), requestID, expected, value)
	if err != nil {
		prefs, _, _ := a.Connections.Preferences(r.Context())
		writeAPIError(w, err, requestID, prefs)
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) rules(w http.ResponseWriter, r *http.Request) {
	rules, err := a.Permissions.Rules(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	if rules == nil {
		rules = []permissions.Rule{}
	}
	automation, goal := r.URL.Query().Get("automation_id"), r.URL.Query().Get("goal_id")
	filtered := rules[:0]
	for _, rule := range rules {
		if (automation == "" || rule.AutomationID == automation) && (goal == "" || rule.GoalID == goal) {
			filtered = append(filtered, rule)
		}
	}
	writeJSON(w, 200, list[permissions.Rule]{Items: filtered})
}

func (a *Assistant) rule(w http.ResponseWriter, r *http.Request) {
	rule, err := a.Permissions.Rule(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, rule)
}

func (a *Assistant) createRule(w http.ResponseWriter, r *http.Request) {
	var value permissions.RuleEditable
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if expected != 0 {
		writeAPIError(w, state.ErrRevisionConflict, requestID, nil)
		return
	}
	receipt, err := a.Permissions.SaveRule(r.Context(), "", requestID, 0, value)
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) currentRule(ctx context.Context, id string) any {
	rule, err := a.Permissions.Rule(ctx, id)
	if err != nil {
		return nil
	}
	return rule
}

func (a *Assistant) updateRule(w http.ResponseWriter, r *http.Request) {
	var value permissions.RuleEditable
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if _, err := a.Permissions.Rule(r.Context(), r.PathValue("id")); err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	receipt, err := a.Permissions.SaveRule(r.Context(), r.PathValue("id"), requestID, expected, value)
	if err != nil {
		writeAPIError(w, err, requestID, a.currentRule(r.Context(), r.PathValue("id")))
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) ruleAction(w http.ResponseWriter, r *http.Request) {
	var value struct {
		Action string `json:"action"`
	}
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if value.Action != "revoke" {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	receipt, err := a.Permissions.RevokeRule(r.Context(), r.PathValue("id"), requestID, expected)
	if err != nil {
		writeAPIError(w, err, requestID, a.currentRule(r.Context(), r.PathValue("id")))
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) interactions(w http.ResponseWriter, r *http.Request) {
	status, taskID := r.URL.Query().Get("status"), r.URL.Query().Get("task_id")
	items, next, err := a.Permissions.Interactions(r.Context(), status, taskID, r.URL.Query().Get("cursor"), 100)
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	if r.URL.Query().Get("cursor") == "" && (status == "" || status == "pending") {
		legacy, err := a.legacyQuestions(r.Context(), taskID, items)
		if err != nil {
			writeAPIError(w, err, "", nil)
			return
		}
		items = append(items, legacy...)
	}
	writeJSON(w, 200, list[permissions.Interaction]{Items: items, NextCursor: next})
}

func (a *Assistant) legacyQuestions(ctx context.Context, taskID string, stored []permissions.Interaction) ([]permissions.Interaction, error) {
	known := map[string]bool{}
	for _, i := range stored {
		known[i.ID] = true
	}
	runs, err := a.Store.OpenTasks(ctx)
	if err != nil {
		return nil, err
	}
	out := []permissions.Interaction{}
	for _, run := range runs {
		q := run.Question
		if q == nil || q.ResolvedBy != "" || known[q.ID] || (taskID != "" && run.ID != taskID) || q.Revision != run.Revision {
			continue
		}
		if existing, err := a.Permissions.Interaction(ctx, q.ID); err == nil && existing.Status != "pending" {
			continue
		}
		revision := q.Revision
		out = append(out, permissions.Interaction{
			ID: q.ID, Revision: 1, TaskID: run.ID, InstructionRevision: &revision, Kind: "question", Status: "pending",
			Prompt: q.Text, Payload: json.RawMessage(`{"input":"text"}`), LegacyQuestionID: q.ID, TaskTitle: clip(run.Request, 200),
		})
	}
	return out, nil
}

func validReturnTo(path string) bool {
	return connections.ValidReturnTo(path)
}

func clip(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

func (a *Assistant) interaction(w http.ResponseWriter, r *http.Request) {
	item, err := a.Permissions.Interaction(r.Context(), r.PathValue("id"))
	if errors.Is(err, state.ErrNotFound) {
		legacy, legacyErr := a.legacyQuestions(r.Context(), "", nil)
		if legacyErr == nil {
			for _, q := range legacy {
				if q.ID == r.PathValue("id") {
					writeJSON(w, 200, q)
					return
				}
			}
		}
	}
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, item)
}

func (a *Assistant) respond(w http.ResponseWriter, r *http.Request) {
	var value permissions.Response
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if value.Kind == "signed_in" {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	id := r.PathValue("id")
	if _, err := a.Permissions.Interaction(r.Context(), id); errors.Is(err, state.ErrNotFound) {
		if err := a.adoptLegacy(r.Context(), id); err != nil {
			writeAPIError(w, err, requestID, nil)
			return
		}
	}
	receipt, answer, err := a.Permissions.Respond(r.Context(), id, requestID, expected, value)
	if err != nil {
		current, _ := a.Permissions.Interaction(r.Context(), id)
		writeAPIError(w, err, requestID, current)
		return
	}
	if answer != nil {
		if answer.DefaultPurpose != "" {
			a.rememberDefault(r.Context(), answer.DefaultPurpose, answer.DefaultConnection)
		}
		if a.Answer == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError{Error: "Background tasks are disabled.", Code: "tasks_disabled", Accepted: true, RequestID: requestID})
			return
		}
		err := a.Answer(r.Context(), answer.CommandID, answer.TaskID, answer.Question, answer.Text)
		switch {
		case err == nil || errors.Is(err, state.ErrDuplicate):
			a.Permissions.MarkDelivered(r.Context(), id)
		case errors.Is(err, state.ErrBusy) && !a.taskRunning(r.Context(), answer.TaskID):
			a.Permissions.MarkDelivered(r.Context(), id)
		case errors.Is(err, state.ErrQuestionStale), errors.Is(err, state.ErrBusy):
			if a.ScheduleAnswerRetry != nil {
				a.ScheduleAnswerRetry(context.WithoutCancel(r.Context()))
			}
		default:
			writeJSON(w, 503, apiError{Error: "Your answer is saved; delivering it to the task failed. Retry to finish.", Code: "delivery_pending",
				Retryable: true, Accepted: true, RequestID: requestID})
			return
		}
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) taskRunning(ctx context.Context, id string) bool {
	run, err := a.Store.GetRun(ctx, id)
	return err == nil && run.Status == state.RunRunning
}

func (a *Assistant) adoptLegacy(ctx context.Context, id string) error {
	legacy, err := a.legacyQuestions(ctx, "", nil)
	if err != nil {
		return err
	}
	for _, q := range legacy {
		if q.ID == id {
			_, err := a.Permissions.Open(ctx, q, "")
			return err
		}
	}
	return state.ErrNotFound
}

func (a *Assistant) rememberDefault(ctx context.Context, purpose, connectionID string) {
	prefs, _, err := a.Connections.Preferences(ctx)
	if err != nil {
		return
	}
	defaults := map[string]string{}
	for k, v := range prefs.AccountDefaults {
		if _, err := a.Connections.Get(ctx, v); err == nil {
			defaults[k] = v
		}
	}
	defaults[purpose] = connectionID
	_, err = a.Connections.SavePreferences(ctx, newRequestID(), prefs.Revision, connections.PreferencesEditable{
		Timezone: prefs.Timezone, Locale: prefs.Locale, WorkingHours: prefs.WorkingHours, QuietHours: prefs.QuietHours,
		DefaultDestinationIDs: prefs.DefaultDestinationIDs, AccountDefaults: defaults, Budgets: prefs.Budgets, Input: prefs.Input,
	})
	if err != nil {
		slog.WarnContext(ctx, "connection account default save failed", "purpose", purpose, "connection_id", connectionID, "err", err)
	}
}

func (a *Assistant) mutation(w http.ResponseWriter, r *http.Request) {
	receipt, err := a.Store.Receipt(r.Context(), r.PathValue("request_id"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, receipt)
}

type availableTool struct {
	Tool          extensions.DefinitionRef `json:"tool"`
	Title         string                   `json:"title"`
	Description   string                   `json:"description"`
	Capability    string                   `json:"capability"`
	ConnectionIDs []string                 `json:"connection_ids"`
	Availability  string                   `json:"availability"`
	ReasonCode    string                   `json:"reason_code,omitempty"`
	Recovery      string                   `json:"recovery,omitempty"`
	IntegrationID string                   `json:"integration_id,omitempty"`
	Effect        string                   `json:"effect,omitempty"`
	Exposure      string                   `json:"exposure,omitempty"`
}

func (a *Assistant) availableTools(ctx context.Context) ([]availableTool, error) {
	snapshot, _ := a.Tools(ctx)
	records, _, err := a.Connections.Records(ctx)
	if err != nil {
		return nil, err
	}
	out := []availableTool{}
	seen := map[string]bool{}
	add := func(d extensions.Definition, availability, reason, recovery string, ids []string) {
		if seen[d.Ref.ID] {
			return
		}
		seen[d.Ref.ID] = true
		if ids == nil {
			ids = []string{}
		}
		out = append(out, availableTool{Tool: d.Ref, Title: d.Title, Description: d.Description, Capability: d.Capability,
			ConnectionIDs: ids, Availability: availability, ReasonCode: reason, Recovery: recovery, IntegrationID: d.IntegrationID,
			Effect: string(d.Policy.Effect), Exposure: string(d.Exposure)})
	}
	for _, d := range snapshot.Definitions() {
		if d.Exposure == extensions.ExposureDisabled {
			add(d, "disabled", "disabled", "enable", nil)
			continue
		}
		if d.ConnectionType == "" {
			add(d, "ready", "", "", nil)
			continue
		}
		ids := []string{}
		for _, record := range records {
			if record.IntegrationID == d.ConnectionType && record.Status == connections.StatusConnected &&
				(d.RequiredCapability == "" || contains(record.Capabilities, d.RequiredCapability)) {
				ids = append(ids, record.ID)
			}
		}
		add(d, "ready", "", "", ids)
	}
	for id, integration := range a.Connections.Integrations {
		for _, d := range integration.Definitions() {
			if seen[d.Ref.ID] {
				continue
			}
			availability, reason, recovery := "needs_connection", "not_connected", "connect"
			for _, record := range records {
				if record.IntegrationID == id && record.Status == connections.StatusConnected {
					availability, reason, recovery = "needs_permission", "needs_more_access", "request_access"
				}
			}
			add(d, availability, reason, recovery, nil)
		}
	}
	return out, nil
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func (a *Assistant) tools(w http.ResponseWriter, r *http.Request) {
	items, err := a.availableTools(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	q := r.URL.Query()
	filtered := []availableTool{}
	words := strings.Fields(strings.ToLower(q.Get("q")))
	for _, item := range items {
		if v := q.Get("availability"); v != "" && item.Availability != v {
			continue
		}
		if v := q.Get("integration_id"); v != "" && item.IntegrationID != v {
			continue
		}
		if v := q.Get("capability"); v != "" && item.Capability != v {
			continue
		}
		text := strings.ToLower(item.Title + " " + item.Description + " " + item.Capability)
		match := true
		for _, word := range words {
			match = match && strings.Contains(text, word)
		}
		if match {
			filtered = append(filtered, item)
		}
	}
	writeJSON(w, 200, list[availableTool]{Items: filtered})
}

func (a *Assistant) tool(w http.ResponseWriter, r *http.Request) {
	items, err := a.availableTools(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	for _, item := range items {
		if item.Tool.ID == r.PathValue("id") {
			writeJSON(w, 200, item)
			return
		}
	}
	writeAPIError(w, state.ErrNotFound, "", nil)
}

func newRequestID() string { return uuid.NewString() }

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func mustJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

func (a *Assistant) ResolveSignIns(ctx context.Context, connectionID string) {
	if a.Permissions == nil || a.Answer == nil {
		return
	}
	pending, _, err := a.Permissions.Interactions(ctx, "pending", "", "", 200)
	if err != nil {
		return
	}
	for _, i := range pending {
		if i.Kind != "sign_in" {
			continue
		}
		var payload struct {
			ConnectionID string `json:"connection_id"`
		}
		if json.Unmarshal(i.Payload, &payload) != nil || payload.ConnectionID != connectionID {
			continue
		}
		requestID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("signed-in:"+i.ID)).String()
		_, answer, err := a.Permissions.Respond(ctx, i.ID, requestID, i.Revision, permissions.Response{Kind: "signed_in",
			Text: "The user signed in to the account again. Retry the same call."})
		if err != nil || answer == nil {
			continue
		}
		if err := a.Answer(ctx, answer.CommandID, answer.TaskID, answer.Question, answer.Text); err == nil || errors.Is(err, state.ErrDuplicate) {
			a.Permissions.MarkDelivered(ctx, i.ID)
		} else if a.ScheduleAnswerRetry != nil && (errors.Is(err, state.ErrQuestionStale) || errors.Is(err, state.ErrBusy)) {
			a.ScheduleAnswerRetry(context.WithoutCancel(ctx))
		}
	}
}
