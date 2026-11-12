package clientauth

import (
	"context"
	"time"
)

const (
	DeviceStartPath  = "/v1/client-authorizations"
	DeviceStatusPath = "/v1/client-authorizations/status"
	DeviceLifetime   = 10 * time.Minute
	DevicePollGrace  = time.Minute
	DeviceInterval   = 3
	CLILifetime      = 90 * 24 * time.Hour
)

type DeviceStart struct {
	Name           string `json:"name"`
	CredentialHash string `json:"credential_hash"`
	ProofHash      string `json:"proof_hash"`
}

type DeviceChallenge struct {
	ID              string    `json:"id"`
	Code            string    `json:"code"`
	ExpiresAt       time.Time `json:"expires_at"`
	IntervalSeconds int       `json:"interval_seconds"`
}

type DeviceView struct {
	Name      string    `json:"name"`
	ExpiresAt time.Time `json:"expires_at"`
}

type DeviceApproval struct {
	Code   string   `json:"code"`
	Scopes []string `json:"scopes"`
}

type DevicePoll struct {
	ID    string `json:"id"`
	Proof string `json:"proof"`
}

type DeviceStatus struct {
	Status  string `json:"status"`
	OwnerID string `json:"owner_id,omitempty"`
	TokenID string `json:"token_id,omitempty"`
}

type DeviceRecord struct {
	Name           string    `firestore:"name"`
	Code           string    `firestore:"code"`
	ExpiresAt      time.Time `firestore:"expires_at"`
	CredentialHash string    `firestore:"credential_hash"`
	ProofHash      string    `firestore:"proof_hash"`
	OwnerID        string    `firestore:"owner_id,omitempty"`
	Scopes         []string  `firestore:"scopes,omitempty"`
}

type DeviceStore interface {
	CreateDevice(context.Context, string, DeviceRecord) error
	Device(context.Context, string) (DeviceRecord, error)
	ApproveDevice(context.Context, string, string, DeviceApproval) error
}
