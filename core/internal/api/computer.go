package api

import (
	"errors"
	"net/http"

	"nori/internal/computersession"
	"nori/internal/permissions"
	"nori/internal/state"
)

func (a *Assistant) computerSessions(w http.ResponseWriter, r *http.Request) {
	session, err := a.Computer.Get(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, list[computersession.Session]{Items: []computersession.Session{session}})
}

func (a *Assistant) computerSession(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("id") != computersession.SessionID {
		writeAPIError(w, state.ErrNotFound, "", nil)
		return
	}
	a.computerSessionView(w, r)
}

func (a *Assistant) computerSessionView(w http.ResponseWriter, r *http.Request) {
	session, err := a.Computer.Get(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, session)
}

// computerAction takes over, renews, releases or resumes. Resuming also answers the task's takeover interaction so
// the task continues; a stale lease or task revision is refused with the current session.
func (a *Assistant) computerAction(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("id") != computersession.SessionID {
		writeAPIError(w, state.ErrNotFound, "", nil)
		return
	}
	var value struct {
		Action              string `json:"action"`
		Reason              string `json:"reason,omitempty"`
		LeaseID             string `json:"lease_id,omitempty"`
		TaskID              string `json:"task_id,omitempty"`
		InstructionRevision *int64 `json:"instruction_revision,omitempty"`
	}
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	current := func() any { session, _ := a.Computer.Get(r.Context()); return session }
	var receipt state.MutationReceipt
	var err error
	switch value.Action {
	case "take_over":
		receipt, err = a.Computer.TakeOver(r.Context(), requestID, expected, value.Reason)
	case "renew", "release":
		receipt, err = a.Computer.Renew(r.Context(), requestID, expected, value.LeaseID, value.Action == "release")
	case "resume":
		if value.InstructionRevision == nil {
			writeAPIError(w, state.ErrInvalidResource, requestID, nil)
			return
		}
		var interaction string
		receipt, interaction, err = a.Computer.Resume(r.Context(), requestID, expected, value.LeaseID, value.TaskID, *value.InstructionRevision)
		if err == nil && interaction != "" {
			if saved, err := a.Permissions.Interaction(r.Context(), interaction); err == nil && saved.Status == "pending" {
				_, answer, err := a.Permissions.Respond(r.Context(), interaction, newRequestIDFrom(requestID), saved.Revision, permissionsResume(value.LeaseID))
				if err == nil && answer != nil {
					// An answer the task did not accept yet stays for the delivery pass.
					if err := a.Answer(r.Context(), answer.CommandID, answer.TaskID, answer.Question, answer.Text); err == nil || errors.Is(err, state.ErrDuplicate) {
						a.Permissions.MarkDelivered(r.Context(), interaction)
					}
				}
			}
		}
	default:
		err = state.ErrInvalidResource
	}
	if err != nil {
		if errors.Is(err, computersession.ErrLease) {
			writeJSON(w, 409, apiErrorWithCurrent("interaction_stale", err.Error(), requestID, current()))
			return
		}
		writeAPIError(w, err, requestID, current())
		return
	}
	writeReceipt(w, receipt)
}

func permissionsResume(lease string) permissions.Response {
	return permissions.Response{Kind: "resume_computer", SessionID: computersession.SessionID, LeaseID: lease}
}

func apiErrorWithCurrent(code, message, requestID string, current any) apiError {
	body := apiError{Error: message, Code: code, RequestID: requestID}
	body.Current, _ = jsonMarshal(current)
	return body
}
