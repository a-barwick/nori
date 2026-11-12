package api

import (
	"errors"
	"net/http"
	"strings"

	"nori/internal/plugins"
	"nori/internal/state"
)

func (a *Assistant) registerPlugins(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/plugins/catalog", a.pluginCatalog)
	mux.HandleFunc("POST /v1/plugins/packages", jsonBody(a.uploadPackage))
	mux.HandleFunc("GET /v1/plugins/installations", a.installations)
	mux.HandleFunc("POST /v1/plugins/installations", jsonBody(a.install))
	mux.HandleFunc("GET /v1/plugins/installations/{id}", a.installation)
	mux.HandleFunc("GET /v1/plugins/installations/{id}/review", a.installationReview)
	mux.HandleFunc("POST /v1/plugins/installations/{id}/actions", jsonBody(a.installationAction))
	mux.HandleFunc("GET /v1/skills", a.skills)
	mux.HandleFunc("POST /v1/skills", jsonBody(a.createSkill))
	mux.HandleFunc("GET /v1/skills/{id}", a.skill)
	mux.HandleFunc("PUT /v1/skills/{id}", jsonBody(a.updateSkill))
	mux.HandleFunc("POST /v1/skills/{id}/actions", jsonBody(a.skillAction))
}

func pluginError(w http.ResponseWriter, err error, requestID string, current any) {
	if errors.Is(err, plugins.ErrUnsupported) {
		writeJSON(w, 400, apiError{Error: err.Error(), Code: "unsupported", RequestID: requestID})
		return
	}
	writeAPIError(w, err, requestID, current)
}

func (a *Assistant) pluginCatalog(w http.ResponseWriter, r *http.Request) {
	items, err := a.Plugins.Catalog(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, list[plugins.CatalogEntry]{Items: items})
}

// uploadPackage accepts a local package manifest. Uploaded packages are unverified and carry no code.
func (a *Assistant) uploadPackage(w http.ResponseWriter, r *http.Request) {
	var manifest plugins.Manifest
	if !decode(w, r, &manifest) {
		return
	}
	saved, err := a.Plugins.Upload(r.Context(), manifest)
	if err != nil {
		pluginError(w, err, "", nil)
		return
	}
	writeJSON(w, 201, saved)
}

func (a *Assistant) installations(w http.ResponseWriter, r *http.Request) {
	items, err := a.Plugins.Installations(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, list[plugins.Installation]{Items: items})
}

func (a *Assistant) installation(w http.ResponseWriter, r *http.Request) {
	item, err := a.Plugins.Installation(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, item)
}

func (a *Assistant) install(w http.ResponseWriter, r *http.Request) {
	var value plugins.InstallationCreate
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if strings.TrimSpace(value.Package.ID) == "" || strings.TrimSpace(value.Package.Version) == "" {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	if expected != 0 {
		writeAPIError(w, state.ErrRevisionConflict, requestID, nil)
		return
	}
	receipt, err := a.Plugins.Install(r.Context(), requestID, value.Package.ID, value.Package.Version, value.ApprovedAccess, value.Configuration)
	if err != nil {
		pluginError(w, err, requestID, nil)
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) installationReview(w http.ResponseWriter, r *http.Request) {
	review, err := a.Plugins.Review(r.Context(), r.PathValue("id"), r.URL.Query().Get("version"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, review)
}

func (a *Assistant) installationAction(w http.ResponseWriter, r *http.Request) {
	var value plugins.InstallationAction
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if !value.Valid() {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	current, err := a.Plugins.Installation(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	receipt, err := a.Plugins.Act(r.Context(), r.PathValue("id"), requestID, expected, value.Action, value.Version, value.ApprovedAccess, value.RemoveState)
	if err != nil {
		pluginError(w, err, requestID, current)
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) skills(w http.ResponseWriter, r *http.Request) {
	items, err := a.Plugins.Skills(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, list[plugins.SkillView]{Items: items})
}

func (a *Assistant) skill(w http.ResponseWriter, r *http.Request) {
	item, err := a.Plugins.Skill(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, item)
}

func (a *Assistant) createSkill(w http.ResponseWriter, r *http.Request) {
	var value plugins.SkillEditable
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if expected != 0 {
		writeAPIError(w, state.ErrRevisionConflict, requestID, nil)
		return
	}
	receipt, err := a.Plugins.SaveSkill(r.Context(), "", requestID, 0, value)
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) updateSkill(w http.ResponseWriter, r *http.Request) {
	var value plugins.SkillEditable
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	current, err := a.Plugins.Skill(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	receipt, err := a.Plugins.SaveSkill(r.Context(), r.PathValue("id"), requestID, expected, value)
	if err != nil {
		writeAPIError(w, err, requestID, current)
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) skillAction(w http.ResponseWriter, r *http.Request) {
	var value plugins.SkillAction
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if !value.Valid() {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	receipt, err := a.Plugins.DeleteSkill(r.Context(), r.PathValue("id"), requestID, expected)
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	writeReceipt(w, receipt)
}
