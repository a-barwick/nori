// Package localvault is the development-only connected-account credential store. It encrypts each connection's
// material with AES-256-GCM under a key generated on first use and kept beside the data with mode 0600. It exists so
// OAuth flows can be exercised on the local stack; it is refused outside NORI_CLOUD=local and is not a production
// choice (that is decision D1).
package localvault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"

	"nori/internal/secret"
)

type Vault struct {
	dir  string
	aead cipher.AEAD
	mu   sync.Mutex
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Open prepares the vault directory and its key.
func Open(dir string) (*Vault, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dir, "vault.key")
	key, err := os.ReadFile(keyPath)
	if errors.Is(err, fs.ErrNotExist) {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		if err = os.WriteFile(keyPath, key, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("local credential vault key is malformed")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{dir: dir, aead: aead}, nil
}

// lock serializes writers across goroutines and across local processes (the API and the worker both refresh).
func (v *Vault) lock() (func(), error) {
	v.mu.Lock()
	file, err := os.OpenFile(filepath.Join(v.dir, "vault.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		v.mu.Unlock()
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		v.mu.Unlock()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		file.Close()
		v.mu.Unlock()
	}, nil
}

// ForUser scopes the vault to one user's connections.
func (v *Vault) ForUser(userID string) (secret.CredentialStore, error) {
	if !validName.MatchString(userID) {
		return nil, errors.New("invalid user scope for credentials")
	}
	return &scoped{v: v, user: userID}, nil
}

type scoped struct {
	v    *Vault
	user string
}

type sealed struct {
	Version int64  `json:"version"`
	Nonce   []byte `json:"nonce"`
	Data    []byte `json:"data"`
}

func (s *scoped) path(connectionID string) (string, error) {
	if !validName.MatchString(connectionID) {
		return "", errors.New("invalid connection id")
	}
	return filepath.Join(s.v.dir, s.user, connectionID+".json"), nil
}

func (s *scoped) read(path, connectionID string) (sealed, []byte, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return sealed{}, nil, secret.ErrCredentialNotFound
	}
	if err != nil {
		return sealed{}, nil, err
	}
	var box sealed
	if err = json.Unmarshal(raw, &box); err != nil {
		return sealed{}, nil, errors.New("stored credential is malformed")
	}
	// The user and connection are additional data, so a file moved to another user's directory does not decrypt.
	plain, err := s.v.aead.Open(nil, box.Nonce, box.Data, []byte(s.user+"/"+connectionID))
	if err != nil {
		return sealed{}, nil, errors.New("stored credential does not decrypt")
	}
	return box, plain, nil
}

func (s *scoped) Get(_ context.Context, connectionID string) (secret.Credential, error) {
	path, err := s.path(connectionID)
	if err != nil {
		return secret.Credential{}, err
	}
	box, plain, err := s.read(path, connectionID)
	if err != nil {
		return secret.Credential{}, err
	}
	return secret.Credential{Version: box.Version, Data: plain}, nil
}

func (s *scoped) Replace(_ context.Context, connectionID string, expected int64, data []byte) (int64, error) {
	path, err := s.path(connectionID)
	if err != nil {
		return 0, err
	}
	unlock, err := s.v.lock()
	if err != nil {
		return 0, err
	}
	defer unlock()
	current := int64(0)
	box, _, err := s.read(path, connectionID)
	switch {
	case err == nil:
		current = box.Version
	case !errors.Is(err, secret.ErrCredentialNotFound):
		return 0, err
	}
	if current != expected {
		return 0, secret.ErrCredentialConflict
	}
	nonce := make([]byte, s.v.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return 0, err
	}
	next := sealed{Version: current + 1, Nonce: nonce, Data: s.v.aead.Seal(nil, nonce, data, []byte(s.user+"/"+connectionID))}
	raw, _ := json.Marshal(next)
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return 0, err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, raw, 0o600); err != nil {
		return 0, err
	}
	if err = os.Rename(tmp, path); err != nil {
		return 0, err
	}
	return next.Version, nil
}

func (s *scoped) Delete(_ context.Context, connectionID string) error {
	path, err := s.path(connectionID)
	if err != nil {
		return err
	}
	unlock, err := s.v.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err = os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
