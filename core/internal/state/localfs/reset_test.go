package localfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPurgeUserArtifacts(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	write := func(path, value string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	legacyFile := filepath.Join(root, "uploads", "legacy")
	userFile := filepath.Join(root, "users", "alice", "results", "report")
	otherFile := filepath.Join(root, "users", "bob", "uploads", "photo")
	externalFile := filepath.Join(outside, "keep")
	for _, path := range []string{legacyFile, userFile, otherFile, externalFile} {
		write(path, "content")
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	legacy, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if err := legacy.PurgeLegacy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := legacy.PurgeLegacy(context.Background()); err != nil {
		t.Fatalf("repeated purge: %v", err)
	}
	if _, err := os.Stat(legacyFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy file survived purge: %v", err)
	}
	for _, path := range []string{userFile, otherFile, externalFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unrelated file was removed: %v", err)
		}
	}
	user, err := Open(filepath.Join(root, "users", "alice"))
	if err != nil {
		t.Fatal(err)
	}
	defer user.Close()
	if err := user.Purge(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(userFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("user file survived purge: %v", err)
	}
	if _, err := os.Stat(otherFile); err != nil {
		t.Fatalf("another user's file was removed: %v", err)
	}
}
