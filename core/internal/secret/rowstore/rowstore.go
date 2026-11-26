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

const Collection = "credential_rows"

type KeyWrapper interface {
	Wrap(ctx context.Context, dataKey []byte) (wrapped []byte, keyID string, err error)
	Unwrap(ctx context.Context, wrapped []byte, keyID string) ([]byte, error)
}

type row struct {
	WrappedKey []byte `json:"wrapped_key"`
	KeyID      string `json:"key_id"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
	Owner      bool   `json:"owner,omitempty"`
	AADVersion int    `json:"aad_version,omitempty"`
}

type Store struct {
	Resources state.ResourceStore
	Keys      KeyWrapper
	Owner     string
	Now       func() time.Time
}

var _ secret.CredentialStore = (*Store)(nil)

func (s *Store) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

func legacyOwnerAAD(owner, connectionID string) []byte {
	return []byte("nori-credential:" + owner + ":" + connectionID)
}

func ownerAAD(owner, connectionID string) []byte {
	return []byte(fmt.Sprintf("nori-credential:v2:%d:%s\x00%d:%s", len(owner), owner, len(connectionID), connectionID))
}

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
	if !stored.Owner || s.Owner == "" {
		return secret.Credential{}, errors.New("the credential could not be decrypted")
	}
	dataKey, err := s.Keys.Unwrap(ctx, stored.WrappedKey, stored.KeyID)
	if err != nil {
		return secret.Credential{}, fmt.Errorf("the credential's data key could not be unwrapped: %w", err)
	}
	gcm, err := newGCM(dataKey)
	if err != nil {
		return secret.Credential{}, err
	}
	if len(stored.Nonce) != gcm.NonceSize() {
		return secret.Credential{}, errors.New("the credential could not be decrypted")
	}
	data, err := gcm.Open(nil, stored.Nonce, stored.Ciphertext, ownerAAD(s.Owner, connectionID))
	if err != nil && stored.AADVersion == 0 {
		data, err = gcm.Open(nil, stored.Nonce, stored.Ciphertext, legacyOwnerAAD(s.Owner, connectionID))
	}
	if err != nil {
		return secret.Credential{}, errors.New("the credential could not be decrypted")
	}
	return secret.Credential{Version: r.Revision, Data: data}, nil
}

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
	if s.Owner == "" {
		return 0, errors.New("the credential store has no owner")
	}
	r, err := state.NewResource(Collection, connectionID, expected+1, row{
		WrappedKey: wrapped,
		KeyID:      keyID,
		Nonce:      nonce,
		Ciphertext: gcm.Seal(nil, nonce, data, ownerAAD(s.Owner, connectionID)),
		Owner:      true,
		AADVersion: 2,
	}, nil, s.now())
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
