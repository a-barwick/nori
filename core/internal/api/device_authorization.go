package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/clientauth"
	"nori/internal/state"
	"nori/internal/users"
)

func (s *userServer) startDevice(w http.ResponseWriter, r *http.Request) {
	var input clientauth.DeviceStart
	if !decode(w, r, &input) {
		return
	}
	if !validClientName(input.Name) || !clientauth.ValidHash(input.CredentialHash) || !clientauth.ValidHash(input.ProofHash) {
		writeAPIError(w, state.ErrInvalidResource, "", nil)
		return
	}
	id, code := uuid.NewString(), strings.ToUpper(uuid.NewString()[:8])
	record := clientauth.DeviceRecord{
		Name: strings.TrimSpace(input.Name), Code: code, ExpiresAt: time.Now().UTC().Add(clientauth.DeviceLifetime),
		CredentialHash: input.CredentialHash, ProofHash: input.ProofHash,
	}
	if err := s.devices.CreateDevice(r.Context(), id, record); err != nil {
		deviceUnavailable(w)
		return
	}
	writeJSON(w, 201, clientauth.DeviceChallenge{ID: id, Code: code, ExpiresAt: record.ExpiresAt, IntervalSeconds: clientauth.DeviceInterval})
}

func (s *userServer) pollDevice(w http.ResponseWriter, r *http.Request) {
	var input clientauth.DevicePoll
	if !decode(w, r, &input) {
		return
	}
	if !validID(input.ID) || !clientauth.ValidSecret(input.Proof) {
		writeAPIError(w, state.ErrInvalidResource, "", nil)
		return
	}
	ctx := r.Context()
	record, err := s.devices.Device(ctx, input.ID)
	if err != nil || subtle.ConstantTimeCompare([]byte(record.ProofHash), []byte(clientauth.ProofHash(input.Proof))) != 1 {
		writeAPIError(w, state.ErrNotFound, "", nil)
		return
	}
	if record.OwnerID == "" {
		writeJSON(w, 200, clientauth.DeviceStatus{Status: "pending"})
		return
	}
	row, err := s.directory.Get(ctx, record.OwnerID)
	if err != nil {
		deviceUnavailable(w)
		return
	}
	routes, release, err := s.open(ctx, row)
	if err != nil {
		deviceUnavailable(w)
		return
	}
	defer release()
	expires := record.ExpiresAt.Add(clientauth.CLILifetime)
	_, err = createClient(ctx, routes.ClientStore, input.ID, mcpClientEditable{
		Name: record.Name, Kind: "cli", Scopes: record.Scopes, ExpiresAt: &expires, CredentialHash: record.CredentialHash,
	})
	if err != nil {
		deviceUnavailable(w)
		return
	}
	writeJSON(w, 200, clientauth.DeviceStatus{Status: "approved", OwnerID: record.OwnerID, TokenID: clientID(input.ID)})
}

func (a *Assistant) deviceView(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID(id) {
		writeAPIError(w, state.ErrNotFound, "", nil)
		return
	}
	record, err := a.Devices.Device(r.Context(), id)
	if err != nil || record.OwnerID != "" {
		writeAPIError(w, state.ErrNotFound, "", nil)
		return
	}
	writeJSON(w, 200, clientauth.DeviceView{Name: record.Name, ExpiresAt: record.ExpiresAt})
}

func (a *Assistant) approveDevice(w http.ResponseWriter, r *http.Request) {
	var approval clientauth.DeviceApproval
	if !decode(w, r, &approval) {
		return
	}
	if !validID(r.PathValue("id")) || len(approval.Code) != 8 || !validScopes(approval.Scopes) {
		writeAPIError(w, state.ErrInvalidResource, "", nil)
		return
	}
	approval.Code = strings.ToUpper(approval.Code)
	if err := a.Devices.ApproveDevice(r.Context(), r.PathValue("id"), users.ID(r.Context()), approval); err != nil {
		writeAPIError(w, state.ErrNotFound, "", nil)
		return
	}
	writeJSON(w, 200, clientauth.DeviceStatus{Status: "approved"})
}

func deviceUnavailable(w http.ResponseWriter) {
	writeJSON(w, 503, apiError{Error: "Client authorization is unavailable. Try again.", Code: "internal", Retryable: true})
}
