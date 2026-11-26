package api

import (
	"net/http"
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
	writeJSON(w, http.StatusOK, status)
}

func (a *Assistant) deleteModelKey(w http.ResponseWriter, r *http.Request) {
	status, err := a.ModelKeys.Delete(r.Context(), r.PathValue("provider"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, http.StatusOK, status)
}
