package api

import (
	"net/http"
	"time"

	"nori/internal/servicekeys"
)

type serviceKeyStatusView struct {
	Service     string     `json:"service"`
	Label       string     `json:"label"`
	Configured  bool       `json:"configured"`
	Last4       string     `json:"last4,omitempty"`
	MaskedKey   string     `json:"masked_key,omitempty"`
	Source      string     `json:"source,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
	ValidatedAt *time.Time `json:"validated_at,omitempty"`
}

type serviceKeysListView struct {
	Services []serviceKeyStatusView `json:"services"`
}

func (a *Assistant) formatServiceKeyStatus(st servicekeys.ServiceStatus) serviceKeyStatusView {
	masked := ""
	if st.Configured {
		if st.Last4 != "" {
			masked = "••••" + st.Last4
		} else {
			masked = "••••"
		}
	}
	source := "none"
	switch {
	case st.Configured:
		source = "byok"
	case st.Service == servicekeys.ServiceExa && a.PlatformExaAvailable:
		source = "platform"
	}
	return serviceKeyStatusView{
		Service:     st.Service,
		Label:       st.Label,
		Configured:  st.Configured,
		Last4:       st.Last4,
		MaskedKey:   masked,
		Source:      source,
		UpdatedAt:   st.UpdatedAt,
		ValidatedAt: st.ValidatedAt,
	}
}

func (a *Assistant) registerServiceKeys(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/settings/service-keys", a.listServiceKeys)
	mux.HandleFunc("PUT /v1/settings/service-keys/{service}", jsonBody(a.setServiceKey))
	mux.HandleFunc("DELETE /v1/settings/service-keys/{service}", a.deleteServiceKey)
}

func (a *Assistant) listServiceKeys(w http.ResponseWriter, r *http.Request) {
	res, err := a.ServiceKeys.List(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	out := serviceKeysListView{
		Services: make([]serviceKeyStatusView, 0, len(res.Services)),
	}
	for _, st := range res.Services {
		out.Services = append(out.Services, a.formatServiceKeyStatus(st))
	}
	writeJSON(w, http.StatusOK, out)
}

type setServiceKeyRequest struct {
	APIKey string `json:"api_key"`
}

func (a *Assistant) setServiceKey(w http.ResponseWriter, r *http.Request) {
	if err := a.checkBYOKAllowed(r.Context()); err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	var body setServiceKeyRequest
	if !decode(w, r, &body) {
		return
	}
	st, err := a.ServiceKeys.Set(r.Context(), r.PathValue("service"), body.APIKey)
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, http.StatusOK, a.formatServiceKeyStatus(st))
}

func (a *Assistant) deleteServiceKey(w http.ResponseWriter, r *http.Request) {
	st, err := a.ServiceKeys.Delete(r.Context(), r.PathValue("service"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, http.StatusOK, a.formatServiceKeyStatus(st))
}
