package rowstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/api/cloudkms/v1"
)

// LocalKey wraps data keys with a 256-bit master key kept in a file (development only). The file is created with
// mode 600 if it does not exist.
type LocalKey struct {
	key []byte
	id  string
}

func OpenLocalKey(path string) (*LocalKey, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data = make([]byte, 32)
		if _, err := rand.Read(data); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(data) != 32 {
		return nil, errors.New("the credential master key file must hold 32 bytes")
	}
	sum := sha256.Sum256(data)
	return &LocalKey{key: data, id: "local:" + hex.EncodeToString(sum[:6])}, nil
}

func (k *LocalKey) Wrap(_ context.Context, dataKey []byte) ([]byte, string, error) {
	gcm, err := newGCM(k.key)
	if err != nil {
		return nil, "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", err
	}
	return append(nonce, gcm.Seal(nil, nonce, dataKey, []byte("nori-data-key"))...), k.id, nil
}

func (k *LocalKey) Unwrap(_ context.Context, wrapped []byte, keyID string) ([]byte, error) {
	if keyID != k.id {
		return nil, fmt.Errorf("the row was wrapped by key %s, not this deployment's %s", keyID, k.id)
	}
	gcm, err := newGCM(k.key)
	if err != nil {
		return nil, err
	}
	if len(wrapped) < gcm.NonceSize() {
		return nil, errors.New("wrapped key is too short")
	}
	return gcm.Open(nil, wrapped[:gcm.NonceSize()], wrapped[gcm.NonceSize():], []byte("nori-data-key"))
}

// CloudKMS wraps data keys with a Cloud KMS symmetric key (projects/…/locations/…/keyRings/…/cryptoKeys/…), using the
// deployment's own identity. The master key never leaves KMS.
type CloudKMS struct {
	Key     string
	service *cloudkms.Service
}

func OpenCloudKMS(ctx context.Context, key string) (*CloudKMS, error) {
	service, err := cloudkms.NewService(ctx)
	if err != nil {
		return nil, err
	}
	return &CloudKMS{Key: key, service: service}, nil
}

func (k *CloudKMS) Wrap(ctx context.Context, dataKey []byte) ([]byte, string, error) {
	resp, err := k.service.Projects.Locations.KeyRings.CryptoKeys.Encrypt(k.Key, &cloudkms.EncryptRequest{
		Plaintext: base64.StdEncoding.EncodeToString(dataKey), AdditionalAuthenticatedData: base64.StdEncoding.EncodeToString([]byte("nori-data-key")),
	}).Context(ctx).Do()
	if err != nil {
		return nil, "", err
	}
	wrapped, err := base64.StdEncoding.DecodeString(resp.Ciphertext)
	return wrapped, "kms:" + k.Key, err
}

func (k *CloudKMS) Unwrap(ctx context.Context, wrapped []byte, keyID string) ([]byte, error) {
	if keyID != "kms:"+k.Key {
		return nil, fmt.Errorf("the row was wrapped by %s, not this deployment's key", keyID)
	}
	resp, err := k.service.Projects.Locations.KeyRings.CryptoKeys.Decrypt(k.Key, &cloudkms.DecryptRequest{
		Ciphertext: base64.StdEncoding.EncodeToString(wrapped), AdditionalAuthenticatedData: base64.StdEncoding.EncodeToString([]byte("nori-data-key")),
	}).Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(resp.Plaintext)
}
