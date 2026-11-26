package secret

import (
	"context"
	"errors"
)

var (
	ErrCredentialNotFound    = errors.New("no credential is stored for this connection")
	ErrCredentialConflict    = errors.New("the credential changed since it was read")
	ErrCredentialUnavailable = errors.New("connected-account credential storage is not configured in this deployment")
)

type Credential struct {
	Version int64
	Data    []byte
}

type CredentialStore interface {
	Get(ctx context.Context, connectionID string) (Credential, error)
	Replace(ctx context.Context, connectionID string, expected int64, data []byte) (int64, error)
	Delete(ctx context.Context, connectionID string) error
}

type Unavailable struct{}

func (Unavailable) Get(context.Context, string) (Credential, error) {
	return Credential{}, ErrCredentialUnavailable
}
func (Unavailable) Replace(context.Context, string, int64, []byte) (int64, error) {
	return 0, ErrCredentialUnavailable
}
func (Unavailable) Delete(context.Context, string) error { return ErrCredentialUnavailable }
