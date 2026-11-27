package api

import (
	"context"
	"net/http"

	"nori/internal/scheduler"
	"nori/internal/state"
)

func (a *Assistant) registerScheduling(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/automations", a.automations)
	mux.HandleFunc("POST /v1/automations", jsonBody(a.createAutomation))
	mux.HandleFunc("POST /v1/automations/preview", jsonBody(a.previewAutomation))
	mux.HandleFunc("GET /v1/automations/{id}", a.automation)
	mux.HandleFunc("PUT /v1/automations/{id}", jsonBody(a.updateAutomation))
	mux.HandleFunc("POST /v1/automations/{id}/actions", jsonBody(a.automationAction))
	mux.HandleFunc("GET /v1/automations/{id}/occurrences", a.occurrences)
	mux.HandleFunc("GET /v1/inbox", a.inbox)
	mux.HandleFunc("POST /v1/inbox/{id}/actions", jsonBody(a.inboxAction))
}

func (a *Assistant) automations(w http.ResponseWriter, r *http.Request) {
	items, err := a.Scheduler.List(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, list[scheduler.Automation]{Items: items})
}

func (a *Assistant) automation(w http.ResponseWriter, r *http.Request) {
	item, err := a.Scheduler.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, item)
}

func (a *Assistant) currentAutomation(ctx context.Context, id string) any {
	item, err := a.Scheduler.Get(ctx, id)
	if err != nil {
		return nil
	}
	return item
}

func (a *Assistant) previewAutomation(w http.ResponseWriter, r *http.Request) {
	var value scheduler.Editable
	if !decode(w, r, &value) {
		return
	}
	preview, err := a.Scheduler.Preview(r.Context(), value, a.UnattendedWrites)
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, preview)
}

func (a *Assistant) createAutomation(w http.ResponseWriter, r *http.Request) {
	var value scheduler.Editable
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if expected != 0 {
		writeAPIError(w, state.ErrRevisionConflict, requestID, nil)
		return
	}
	receipt, err := a.Scheduler.Save(r.Context(), "", requestID, 0, value)
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) updateAutomation(w http.ResponseWriter, r *http.Request) {
	var value scheduler.Editable
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if _, err := a.Scheduler.Get(r.Context(), r.PathValue("id")); err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	receipt, err := a.Scheduler.Save(r.Context(), r.PathValue("id"), requestID, expected, value)
	if err != nil {
		writeAPIError(w, err, requestID, a.currentAutomation(r.Context(), r.PathValue("id")))
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) automationAction(w http.ResponseWriter, r *http.Request) {
	var value scheduler.Action
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if !value.Valid() {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	receipt, err := a.Scheduler.Act(r.Context(), r.PathValue("id"), requestID, expected, value)
	if err != nil {
		writeAPIError(w, err, requestID, a.currentAutomation(r.Context(), r.PathValue("id")))
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) occurrences(w http.ResponseWriter, r *http.Request) {
	if err := a.Scheduler.Exists(r.Context(), r.PathValue("id")); err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	items, err := a.Scheduler.Occurrences(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, list[scheduler.Occurrence]{Items: items})
}

func (a *Assistant) inbox(w http.ResponseWriter, r *http.Request) {
	items, err := a.Scheduler.Inbox(r.Context(), r.URL.Query().Get("unread") == "true")
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, list[scheduler.InboxItem]{Items: items})
}

func (a *Assistant) inboxAction(w http.ResponseWriter, r *http.Request) {
	var value inboxAction
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if !value.valid() {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	receipt, err := a.Scheduler.Acknowledge(r.Context(), r.PathValue("id"), requestID, expected)
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	writeReceipt(w, receipt)
}
