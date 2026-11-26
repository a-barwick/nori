package api

import (
	"errors"
	"log/slog"
	"net/http"

	"nori/internal/state"
)

func (a *Assistant) registerModelKeys(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/settings/model-keys", a.listModelKeys)
	mux.HandleFunc("PUT /v1/settings/model-keys/{provider}", jsonBody(a.setModelKey))
	mux.HandleFunc("DELETE /v1/settings/model-keys/{provider}", a.deleteModelKey)
}

func (a *Assistant) listModelKeys(w http.ResponseWriter, r *http.Request) {
	res, err := a.ModelKeys.List(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type setModelKeyRequest struct {
	APIKey string `json:"api_key"`
}

func (a *Assistant) setModelKey(w http.ResponseWriter, r *http.Request) {
	var body setModelKeyRequest
	if !decode(w, r, &body) {
		return
	}
	status, err := a.ModelKeys.Set(r.Context(), r.PathValue("provider"), body.APIKey)
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	if a.Models != nil {
		if err := a.Models.EnsureSelection(r.Context()); err != nil {
			slog.WarnContext(r.Context(), "model choices could not be initialized after saving a key", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *Assistant) deleteModelKey(w http.ResponseWriter, r *http.Request) {
	if a.Models != nil {
		if err := a.Models.EnsureSelection(r.Context()); err != nil {
			writeAPIError(w, err, "", nil)
			return
		}
	}
	status, err := a.ModelKeys.Delete(r.Context(), r.PathValue("provider"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *Assistant) registerModelSettings(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/settings/models", a.getModelSettings)
	mux.HandleFunc("PUT /v1/settings/models", jsonBody(a.saveModelSettings))
}

func (a *Assistant) getModelSettings(w http.ResponseWriter, r *http.Request) {
	view, err := a.Models.View(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *Assistant) saveModelSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Selection *struct {
			state.ModelSelection
			HomeOnly *bool `json:"home_only"`
		} `json:"selection"`
		ExpectedRevision *int64 `json:"expected_revision"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Selection == nil || body.ExpectedRevision == nil || *body.ExpectedRevision < 0 {
		writeAPIError(w, state.ErrInvalidResource, "", nil)
		return
	}
	view, err := a.Models.Save(r.Context(), body.Selection.ModelSelection, *body.ExpectedRevision, body.Selection.HomeOnly)
	if errors.Is(err, state.ErrRevisionConflict) {
		current, _ := a.Models.View(r.Context())
		writeAPIError(w, err, "", current)
		return
	}
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
