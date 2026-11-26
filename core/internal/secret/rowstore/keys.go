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
	"strings"
)

var dataKeyAAD = []byte("nori-data-key")

type MasterKey struct {
	key []byte
	id  string
}

type LocalKey = MasterKey

func OpenLocalKey(path string) (*LocalKey, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data = make([]byte, 32)
		if _, err := rand.Read(data); err != nil {
			return nil, err
		}
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		suffix := make([]byte, 8)
		if _, err := rand.Read(suffix); err != nil {
			return nil, err
		}
		tmp := filepath.Join(dir, "."+filepath.Base(path)+".tmp-"+hex.EncodeToString(suffix))
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if writeErr != nil {
			_ = os.Remove(tmp)
			return nil, writeErr
		}
		if closeErr != nil {
			_ = os.Remove(tmp)
			return nil, closeErr
		}
		err = os.Link(tmp, path)
		_ = os.Remove(tmp)
		if errors.Is(err, os.ErrExist) {
			data, err = os.ReadFile(path)
			if err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return newMasterKey("local", data)
}

func ParseMasterKey(encoded string) (*MasterKey, error) {
	trimmed := strings.TrimSpace(encoded)
	if trimmed == "" {
		return nil, errors.New("the credential master key must not be empty")
	}
	if len(trimmed) == 64 {
		if raw, err := hex.DecodeString(trimmed); err == nil && len(raw) == 32 {
			return newMasterKey("key", raw)
		}
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if raw, err := enc.DecodeString(trimmed); err == nil && len(raw) == 32 {
			return newMasterKey("key", raw)
		}
	}
	return nil, errors.New("the credential master key must decode to 32 bytes (64 hex chars or base64)")
}

func newMasterKey(prefix string, data []byte) (*MasterKey, error) {
	if len(data) != 32 {
		return nil, errors.New("the credential master key must hold 32 bytes")
	}
	copied := append([]byte(nil), data...)
	sum := sha256.Sum256(copied)
	return &MasterKey{key: copied, id: prefix + ":" + hex.EncodeToString(sum[:6])}, nil
}

var _ AADKeyWrapper = (*MasterKey)(nil)

func effectiveDataKeyAAD(aad []byte) []byte {
	if len(aad) > 0 {
		return aad
	}
	return dataKeyAAD
}

func (k *MasterKey) Wrap(ctx context.Context, dataKey []byte) ([]byte, string, error) {
	return k.WrapWithAAD(ctx, dataKey, nil)
}

func (k *MasterKey) WrapWithAAD(_ context.Context, dataKey, aad []byte) ([]byte, string, error) {
	gcm, err := newGCM(k.key)
	if err != nil {
		return nil, "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", err
	}
	return append(nonce, gcm.Seal(nil, nonce, dataKey, effectiveDataKeyAAD(aad))...), k.id, nil
}

func (k *MasterKey) Unwrap(ctx context.Context, wrapped []byte, keyID string) ([]byte, error) {
	return k.UnwrapWithAAD(ctx, wrapped, keyID, nil)
}

func (k *MasterKey) UnwrapWithAAD(_ context.Context, wrapped []byte, keyID string, aad []byte) ([]byte, error) {
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
	return gcm.Open(nil, wrapped[:gcm.NonceSize()], wrapped[gcm.NonceSize():], effectiveDataKeyAAD(aad))
}
