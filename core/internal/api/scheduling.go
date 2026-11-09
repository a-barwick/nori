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
	mux.HandleFunc("GET /v1/goals", a.goals)
	mux.HandleFunc("POST /v1/goals", jsonBody(a.createGoal))
	mux.HandleFunc("GET /v1/goals/{id}", a.goal)
	mux.HandleFunc("PUT /v1/goals/{id}", jsonBody(a.updateGoal))
	mux.HandleFunc("POST /v1/goals/{id}/actions", jsonBody(a.goalAction))
	mux.HandleFunc("GET /v1/delivery-destinations", a.destinations)
	mux.HandleFunc("POST /v1/delivery-destinations", jsonBody(a.createDestination))
	mux.HandleFunc("PUT /v1/delivery-destinations/{id}", jsonBody(a.updateDestination))
	mux.HandleFunc("POST /v1/delivery-destinations/{id}/actions", jsonBody(a.destinationAction))
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
	receipt, err := a.Scheduler.Act(r.Context(), r.PathValue("id"), requestID, expected, value)
	if err != nil {
		writeAPIError(w, err, requestID, a.currentAutomation(r.Context(), r.PathValue("id")))
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) occurrences(w http.ResponseWriter, r *http.Request) {
	if _, err := a.Scheduler.Get(r.Context(), r.PathValue("id")); err != nil {
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

func (a *Assistant) goals(w http.ResponseWriter, r *http.Request) {
	items, err := a.Scheduler.Goals(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, list[scheduler.Goal]{Items: items})
}

func (a *Assistant) goal(w http.ResponseWriter, r *http.Request) {
	item, err := a.Scheduler.Goal(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, item)
}

func (a *Assistant) currentGoal(ctx context.Context, id string) any {
	item, err := a.Scheduler.Goal(ctx, id)
	if err != nil {
		return nil
	}
	return item
}

func (a *Assistant) createGoal(w http.ResponseWriter, r *http.Request) {
	var value scheduler.GoalEditable
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if expected != 0 {
		writeAPIError(w, state.ErrRevisionConflict, requestID, nil)
		return
	}
	receipt, err := a.Scheduler.SaveGoal(r.Context(), "", requestID, 0, value)
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) updateGoal(w http.ResponseWriter, r *http.Request) {
	var value scheduler.GoalEditable
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if _, err := a.Scheduler.Goal(r.Context(), r.PathValue("id")); err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	receipt, err := a.Scheduler.SaveGoal(r.Context(), r.PathValue("id"), requestID, expected, value)
	if err != nil {
		writeAPIError(w, err, requestID, a.currentGoal(r.Context(), r.PathValue("id")))
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) goalAction(w http.ResponseWriter, r *http.Request) {
	var value scheduler.GoalAction
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	receipt, err := a.Scheduler.ActGoal(r.Context(), r.PathValue("id"), requestID, expected, value)
	if err != nil {
		writeAPIError(w, err, requestID, a.currentGoal(r.Context(), r.PathValue("id")))
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) destinations(w http.ResponseWriter, r *http.Request) {
	items, err := a.Scheduler.Destinations(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, list[scheduler.Destination]{Items: items})
}

func (a *Assistant) createDestination(w http.ResponseWriter, r *http.Request) {
	var value scheduler.DestinationEditable
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if expected != 0 {
		writeAPIError(w, state.ErrRevisionConflict, requestID, nil)
		return
	}
	receipt, err := a.Scheduler.SaveDestination(r.Context(), "", requestID, 0, value)
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) updateDestination(w http.ResponseWriter, r *http.Request) {
	var value scheduler.DestinationEditable
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if r.PathValue("id") == scheduler.InAppDestination {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	if _, err := a.Scheduler.Destination(r.Context(), r.PathValue("id")); err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	receipt, err := a.Scheduler.SaveDestination(r.Context(), r.PathValue("id"), requestID, expected, value)
	if err != nil {
		current, _ := a.Scheduler.Destination(r.Context(), r.PathValue("id"))
		writeAPIError(w, err, requestID, current)
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) destinationAction(w http.ResponseWriter, r *http.Request) {
	var value struct {
		Action string `json:"action"`
	}
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if value.Action != "remove" || r.PathValue("id") == scheduler.InAppDestination {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	receipt, err := a.Scheduler.RemoveDestination(r.Context(), r.PathValue("id"), requestID, expected)
	if err != nil {
		current, _ := a.Scheduler.Destination(r.Context(), r.PathValue("id"))
		writeAPIError(w, err, requestID, current)
		return
	}
	writeReceipt(w, receipt)
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
	var value struct {
		Action string `json:"action"`
	}
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if value.Action != "acknowledge" {
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
