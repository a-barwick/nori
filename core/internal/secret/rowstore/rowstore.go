// Package rowstore keeps connected-account credentials as envelope-encrypted rows in the user's own database scope
// (decision D1). Each write encrypts the material with a fresh 256-bit data key (AES-GCM, bound to the connection ID);
// the data key is wrapped by a master key that never leaves its key service: a local key file in development, a
// deployment key service (Cloud KMS) in production. Rows carry the wrapped key, the master key's identity, the nonce
// and the ciphertext; plaintext material and data keys are never stored.
package rowstore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"nori/internal/secret"
	"nori/internal/state"
)

// Collection holds one row per connection credential.
const Collection = "credential_rows"

// KeyWrapper wraps and unwraps data keys with a master key it holds.
type KeyWrapper interface {
	Wrap(ctx context.Context, dataKey []byte) (wrapped []byte, keyID string, err error)
	Unwrap(ctx context.Context, wrapped []byte, keyID string) ([]byte, error)
}

type row struct {
	WrappedKey []byte `json:"wrapped_key"`
	KeyID      string `json:"key_id"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// Store is a secret.CredentialStore over one user's resource store.
type Store struct {
	Resources state.ResourceStore
	Keys      KeyWrapper
	Now       func() time.Time
}

var _ secret.CredentialStore = (*Store)(nil)

func (s *Store) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

func aad(connectionID string) []byte { return []byte("nori-credential:" + connectionID) }

func (s *Store) Get(ctx context.Context, connectionID string) (secret.Credential, error) {
	if !state.ValidResourceID(connectionID) {
		return secret.Credential{}, errors.New("invalid connection id")
	}
	r, err := s.Resources.Resource(ctx, Collection, connectionID)
	if errors.Is(err, state.ErrNotFound) {
		return secret.Credential{}, secret.ErrCredentialNotFound
	}
	if err != nil {
		return secret.Credential{}, err
	}
	var stored row
	if err := r.Decode(&stored); err != nil {
		return secret.Credential{}, err
	}
	dataKey, err := s.Keys.Unwrap(ctx, stored.WrappedKey, stored.KeyID)
	if err != nil {
		return secret.Credential{}, fmt.Errorf("the credential's data key could not be unwrapped: %w", err)
	}
	gcm, err := newGCM(dataKey)
	if err != nil {
		return secret.Credential{}, err
	}
	data, err := gcm.Open(nil, stored.Nonce, stored.Ciphertext, aad(connectionID))
	if err != nil {
		return secret.Credential{}, errors.New("the credential could not be decrypted")
	}
	return secret.Credential{Version: r.Revision, Data: data}, nil
}

// Replace writes new material only if the stored version is still expected (0 creates) and returns the new version.
func (s *Store) Replace(ctx context.Context, connectionID string, expected int64, data []byte) (int64, error) {
	if !state.ValidResourceID(connectionID) {
		return 0, errors.New("invalid connection id")
	}
	dataKey := make([]byte, 32)
	if _, err := rand.Read(dataKey); err != nil {
		return 0, err
	}
	gcm, err := newGCM(dataKey)
	if err != nil {
		return 0, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return 0, err
	}
	wrapped, keyID, err := s.Keys.Wrap(ctx, dataKey)
	if err != nil {
		return 0, fmt.Errorf("the data key could not be wrapped: %w", err)
	}
	r, err := state.NewResource(Collection, connectionID, expected+1, row{WrappedKey: wrapped, KeyID: keyID, Nonce: nonce,
		Ciphertext: gcm.Seal(nil, nonce, data, aad(connectionID))}, nil, s.now())
	if err != nil {
		return 0, err
	}
	saved, err := s.Resources.PutResource(ctx, r, expected)
	if errors.Is(err, state.ErrRevisionConflict) {
		return 0, secret.ErrCredentialConflict
	}
	if err != nil {
		return 0, err
	}
	return saved.Revision, nil
}

// Delete removes the row. A missing credential is not an error.
func (s *Store) Delete(ctx context.Context, connectionID string) error {
	if !state.ValidResourceID(connectionID) {
		return errors.New("invalid connection id")
	}
	_, err := s.Resources.Mutate(ctx, state.Mutation{
		RequestID: uuid.NewString(), Method: "DELETE", Target: "credential:" + connectionID, Payload: connectionID,
		Collection: Collection, ID: connectionID, SkipRevisionCheck: true,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{Receipt: state.MutationReceipt{Status: "applied"}}, nil
			}
			return state.MutationResult{Deletes: []state.ResourceKey{{Collection: Collection, ID: connectionID}},
				Receipt: state.MutationReceipt{Status: "applied"}}, nil
		},
	})
	return err
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
