package secret

import (
	"context"
	"errors"
)

// Connected-account credentials (OAuth tokens, app passwords, configured API keys) have a lifecycle that project
// secrets do not: they are created per user and connection, rotated by refresh, and deleted on disconnect. The
// backing store holds only opaque material and a version; OAuth, refresh policy and account choice belong to the
// Connections service.
//
// Which store production uses is an open decision (D1 in docs/assistant-coordination.md). Until it is made, only
// the local development store exists and production composes Unavailable.

var (
	ErrCredentialNotFound    = errors.New("no credential is stored for this connection")
	ErrCredentialConflict    = errors.New("the credential changed since it was read")
	ErrCredentialUnavailable = errors.New("connected-account credential storage is not configured in this deployment")
)

// Credential is one version of a connection's material. Data is opaque to the store.
type Credential struct {
	Version int64
	Data    []byte
}

// CredentialStore is scoped to one user when opened.
type CredentialStore interface {
	Get(ctx context.Context, connectionID string) (Credential, error)
	// Replace writes new material only if the stored version is still expected (0 creates). It returns the new version.
	Replace(ctx context.Context, connectionID string, expected int64, data []byte) (int64, error)
	// Delete removes the material. A missing credential is not an error.
	Delete(ctx context.Context, connectionID string) error
}

// Unavailable is the credential store for deployments where D1 is unresolved. Connections report
// credential_store_unavailable instead of writing account credentials anywhere.
type Unavailable struct{}

func (Unavailable) Get(context.Context, string) (Credential, error) {
	return Credential{}, ErrCredentialUnavailable
}
func (Unavailable) Replace(context.Context, string, int64, []byte) (int64, error) {
	return 0, ErrCredentialUnavailable
}
func (Unavailable) Delete(context.Context, string) error { return ErrCredentialUnavailable }
