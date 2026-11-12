package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

func journalID(explicit string, scope string, intent any) (string, func(), error) {
	if explicit != "" {
		id, err := uuid.Parse(explicit)
		if err != nil || id.String() != explicit {
			return "", nil, fmt.Errorf("--request-id must be a canonical UUID")
		}
		return explicit, func() {}, nil
	}
	stateDir, err := os.UserConfigDir()
	if err != nil {
		return "", nil, err
	}
	stateDir = filepath.Join(stateDir, "nori-cli", "pending")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", nil, err
	}
	data, err := json.Marshal([]any{scope, intent})
	if err != nil {
		return "", nil, err
	}
	key := sha256.Sum256(data)
	path := filepath.Join(stateDir, hex.EncodeToString(key[:]))
	id := uuid.NewString()
	tmp, err := os.CreateTemp(stateDir, ".intent-")
	if err != nil {
		return "", nil, err
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.WriteString(id + "\n")
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return "", nil, err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return "", nil, err
		}
		saved, err := os.ReadFile(path)
		if err != nil {
			return "", nil, err
		}
		id = strings.TrimSpace(string(saved))
		if _, err := uuid.Parse(id); err != nil {
			return "", nil, fmt.Errorf("invalid request journal entry")
		}
	}
	return id, func() {
		if saved, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(saved)) == id {
			_ = os.Remove(path)
		}
	}, nil
}
