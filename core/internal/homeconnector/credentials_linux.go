package homeconnector

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func readCredential(dir string) (Credential, error) {
	var credential Credential
	path := filepath.Join(dir, "credentials.json")
	info, err := os.Lstat(path)
	if err != nil {
		return credential, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return credential, errors.New("connector credentials must be a private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return credential, err
	}
	err = json.Unmarshal(data, &credential)
	return credential, err
}

func saveCredential(dir string, credential Credential) error {
	data, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, "credentials.json"), data)
}

func deleteCredential(dir string) error {
	err := os.Remove(filepath.Join(dir, "credentials.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
