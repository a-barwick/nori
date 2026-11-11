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

// KMSClient is the part of a key service the store needs: symmetric encryption under a named key, with associated data.
type KMSClient interface {
	Encrypt(ctx context.Context, key string, plaintext, aad []byte) ([]byte, error)
	Decrypt(ctx context.Context, key string, ciphertext, aad []byte) ([]byte, error)
}

// CloudKMS wraps data keys with a Cloud KMS symmetric key (projects/…/locations/…/keyRings/…/cryptoKeys/…), using the
// deployment's own identity. The master key never leaves KMS.
type CloudKMS struct {
	Key    string
	client KMSClient
}

func OpenCloudKMS(ctx context.Context, key string) (*CloudKMS, error) {
	service, err := cloudkms.NewService(ctx)
	if err != nil {
		return nil, err
	}
	return NewCloudKMS(key, googleKMS{service}), nil
}

// NewCloudKMS wraps data keys under key with the given client.
func NewCloudKMS(key string, client KMSClient) *CloudKMS { return &CloudKMS{Key: key, client: client} }

var dataKeyAAD = []byte("nori-data-key")

func (k *CloudKMS) Wrap(ctx context.Context, dataKey []byte) ([]byte, string, error) {
	wrapped, err := k.client.Encrypt(ctx, k.Key, dataKey, dataKeyAAD)
	if err != nil {
		return nil, "", err
	}
	return wrapped, "kms:" + k.Key, nil
}

func (k *CloudKMS) Unwrap(ctx context.Context, wrapped []byte, keyID string) ([]byte, error) {
	if keyID != "kms:"+k.Key {
		return nil, fmt.Errorf("the row was wrapped by %s, not this deployment's key", keyID)
	}
	return k.client.Decrypt(ctx, k.Key, wrapped, dataKeyAAD)
}

// googleKMS is KMSClient over the Cloud KMS API.
type googleKMS struct{ service *cloudkms.Service }

func (g googleKMS) Encrypt(ctx context.Context, key string, plaintext, aad []byte) ([]byte, error) {
	resp, err := g.service.Projects.Locations.KeyRings.CryptoKeys.Encrypt(key, &cloudkms.EncryptRequest{
		Plaintext: base64.StdEncoding.EncodeToString(plaintext), AdditionalAuthenticatedData: base64.StdEncoding.EncodeToString(aad),
	}).Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(resp.Ciphertext)
}

func (g googleKMS) Decrypt(ctx context.Context, key string, ciphertext, aad []byte) ([]byte, error) {
	resp, err := g.service.Projects.Locations.KeyRings.CryptoKeys.Decrypt(key, &cloudkms.DecryptRequest{
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext), AdditionalAuthenticatedData: base64.StdEncoding.EncodeToString(aad),
	}).Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(resp.Plaintext)
}
