package api

import (
	"context"
	"errors"
	"net/http"

	"nori/internal/homeai"
	"nori/internal/settings"
)

type HomeAISettings struct {
	Service   *homeai.Service
	UserID    string
	Downloads settings.HomeAIDownloads
	Test      func(context.Context, string) error
}

func (h *HomeAISettings) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/settings/home-connectors", h.list)
	mux.HandleFunc("POST /v1/settings/home-connectors/pairing/lookup", jsonBody(h.lookup))
	mux.HandleFunc("POST /v1/settings/home-connectors/pairing/approve", jsonBody(h.approve))
	mux.HandleFunc("POST /v1/settings/home-connectors/{id}/test", h.test)
	mux.HandleFunc("DELETE /v1/settings/home-connectors/{id}", h.revoke)
}

type pairingRequest struct {
	UserCode string `json:"user_code"`
}

func (h *HomeAISettings) list(w http.ResponseWriter, r *http.Request) {
	devices, err := h.Service.List(r.Context(), h.UserID)
	if err != nil {
		writeHomeAIError(w, err)
		return
	}
	if devices == nil {
		devices = []homeai.Device{}
	}
	writeJSON(w, http.StatusOK, struct {
		Enabled   bool                     `json:"enabled"`
		Devices   []homeai.Device          `json:"devices"`
		Downloads settings.HomeAIDownloads `json:"downloads,omitempty"`
	}{Enabled: true, Devices: devices, Downloads: h.Downloads})
}

func (h *HomeAISettings) lookup(w http.ResponseWriter, r *http.Request) {
	var req pairingRequest
	if !decode(w, r, &req) {
		return
	}
	if err := h.Service.CheckPairingAttempt(r.Context(), h.UserID); err != nil {
		writeHomeAIError(w, err)
		return
	}
	pairing, err := h.Service.LookupPairing(r.Context(), req.UserCode)
	if err != nil {
		writeHomeAIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": pairing.DeviceName, "user_code": pairing.UserCode, "expires_at": pairing.ExpiresAt})
}

func (h *HomeAISettings) approve(w http.ResponseWriter, r *http.Request) {
	var req pairingRequest
	if !decode(w, r, &req) {
		return
	}
	if err := h.Service.CheckPairingAttempt(r.Context(), h.UserID); err != nil {
		writeHomeAIError(w, err)
		return
	}
	if _, err := h.Service.Approve(r.Context(), h.UserID, req.UserCode); err != nil {
		writeHomeAIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *HomeAISettings) test(w http.ResponseWriter, r *http.Request) {
	if err := h.Test(r.Context(), r.PathValue("id")); err != nil {
		writeHomeAIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *HomeAISettings) revoke(w http.ResponseWriter, r *http.Request) {
	if err := h.Service.Revoke(r.Context(), h.UserID, r.PathValue("id")); err != nil && !errors.Is(err, homeai.ErrDeviceNotFound) && !errors.Is(err, homeai.ErrRevoked) {
		writeHomeAIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeHomeAIError(w http.ResponseWriter, err error) {
	status, code, message, retry := 0, "", "", false
	switch {
	case errors.Is(err, homeai.ErrPairingRateLimited):
		w.Header().Set("Retry-After", "60")
		status, code, message, retry = 429, "pairing_rate_limited", "Try pairing again in a minute.", true
	case errors.Is(err, homeai.ErrDeviceLimit):
		status, code, message = 409, "home_device_limit", "Disconnect your current home server before pairing another."
	case errors.Is(err, homeai.ErrPairingNotFound):
		status, code, message = 404, "pairing_not_found", "Check the code shown on your computer and try again."
	case errors.Is(err, homeai.ErrPairingExpired), errors.Is(err, homeai.ErrPairingUsed):
		status, code, message = 409, "pairing_expired", "Get a new code from the connector."
	case errors.Is(err, homeai.ErrDeviceNotFound), errors.Is(err, homeai.ErrRevoked):
		status, code, message = 404, "home_device_not_found", "This home server is disconnected."
	case errors.Is(err, homeai.ErrOffline):
		status, code, message, retry = 503, "home_offline", "Your home server is offline.", true
	case errors.Is(err, homeai.ErrBusy):
		status, code, message, retry = 429, "home_busy", "Your home server is busy.", true
	case errors.Is(err, homeai.ErrOllamaUnavailable):
		status, code, message, retry = 503, "home_ollama_unavailable", "Start Ollama on your home server.", true
	case errors.Is(err, homeai.ErrModelUnavailable), errors.Is(err, homeai.ErrModelChanged):
		status, code, message = 409, "home_model_unavailable", "Choose an installed model for your home server."
	case errors.Is(err, homeai.ErrInterrupted):
		status, code, message, retry = 503, "home_interrupted", "The connection to your home server was interrupted.", true
	default:
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, status, apiError{Error: message, Code: code, Retryable: retry})
}
