package homeconnector

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type keychainRequest struct {
	Action  string `json:"action"`
	Account string `json:"account"`
	Value   string `json:"value,omitempty"`
}

type keychainResponse struct {
	Value   string `json:"value"`
	Missing bool   `json:"missing"`
	Error   string `json:"error"`
}

func keychainCall(dir, action, value string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	helper := filepath.Join(filepath.Dir(exe), "nori-connector-keychain")
	input, err := json.Marshal(keychainRequest{Action: action, Account: dir, Value: value})
	if err != nil {
		return "", err
	}
	cmd := exec.Command(helper)
	cmd.Stdin = bytes.NewReader(input)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("access connector keychain: %w", err)
	}
	var response keychainResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return "", err
	}
	if response.Missing {
		return "", os.ErrNotExist
	}
	if response.Error != "" {
		return "", errors.New(response.Error)
	}
	return response.Value, nil
}

func readCredential(dir string) (Credential, error) {
	var credential Credential
	value, err := keychainCall(dir, "read", "")
	if err != nil {
		return credential, err
	}
	err = json.Unmarshal([]byte(value), &credential)
	return credential, err
}

func saveCredential(dir string, credential Credential) error {
	data, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	_, err = keychainCall(dir, "write", string(data))
	return err
}

func deleteCredential(dir string) error {
	_, err := keychainCall(dir, "delete", "")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
