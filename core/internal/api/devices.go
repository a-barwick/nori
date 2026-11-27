package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"nori/internal/push"
)

// PushDevicesAPI exposes /v1/devices/push endpoints for registering and
// unregistering native iOS APNs device tokens.
type PushDevicesAPI struct {
	Store  push.Store
	UserID string
}

// Register attaches the /v1/devices/push routes to mux.
func (p *PushDevicesAPI) Register(mux *http.ServeMux) {
	p.register(mux)
}

func (p *PushDevicesAPI) register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/devices/push", jsonBody(p.registerDevice))
	mux.HandleFunc("DELETE /v1/devices/push/{id}", p.unregisterDevice)
}

type registerPushDeviceRequest struct {
	Token       string `json:"token"`
	DeviceID    string `json:"device_id,omitempty"`
	Environment string `json:"environment"`
}

type pushDeviceResponse struct {
	ID          string    `json:"id"`
	DeviceID    string    `json:"device_id,omitempty"`
	Environment string    `json:"environment"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (p *PushDevicesAPI) registerDevice(w http.ResponseWriter, r *http.Request) {
	if p == nil || p.Store == nil || strings.TrimSpace(p.UserID) == "" {
		writeJSON(w, http.StatusServiceUnavailable, apiError{
			Error:     "Push device storage is unavailable.",
			Code:      "service_unavailable",
			Retryable: true,
		})
		return
	}

	var req registerPushDeviceRequest
	if !decode(w, r, &req) {
		return
	}

	dev, err := p.Store.RegisterDevice(r.Context(), p.UserID, req.Token, req.DeviceID, req.Environment)
	if err != nil {
		if errors.Is(err, push.ErrInvalidToken) || errors.Is(err, push.ErrInvalidEnvironment) || errors.Is(err, push.ErrInvalidUserID) {
			writeJSON(w, http.StatusBadRequest, apiError{
				Error: err.Error(),
				Code:  "validation_failed",
			})
			return
		}
		writeAPIError(w, err, "", nil)
		return
	}

	writeJSON(w, http.StatusOK, pushDeviceResponse{
		ID:          dev.ID,
		DeviceID:    dev.DeviceID,
		Environment: dev.Environment,
		CreatedAt:   dev.CreatedAt.UTC(),
		UpdatedAt:   dev.UpdatedAt.UTC(),
	})
}

func (p *PushDevicesAPI) unregisterDevice(w http.ResponseWriter, r *http.Request) {
	if p == nil || p.Store == nil || strings.TrimSpace(p.UserID) == "" {
		writeJSON(w, http.StatusServiceUnavailable, apiError{
			Error:     "Push device storage is unavailable.",
			Code:      "service_unavailable",
			Retryable: true,
		})
		return
	}

	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusNotFound, apiError{
			Error: "Push device not found.",
			Code:  "not_found",
		})
		return
	}

	if err := p.Store.DeleteDevice(r.Context(), p.UserID, id); err != nil {
		if errors.Is(err, push.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, apiError{
				Error: "Push device not found.",
				Code:  "not_found",
			})
			return
		}
		writeAPIError(w, err, "", nil)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
