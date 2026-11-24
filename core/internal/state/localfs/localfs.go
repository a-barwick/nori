// Package localfs stores artifact bytes in a private local directory for development. It implements
// state.ArtifactStore.
package localfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"nori/internal/state"
)

// Store keeps one file per artifact at <directory>/<run>/<id>.png, confined to the directory by an os.Root.
type Store struct {
	root *os.Root
}

var _ state.ArtifactStore = (*Store)(nil)

// Open creates the directory when needed, with mode 0700, and opens it. The directory must be absolute and must not
// be a symbolic link.
func Open(directory string) (*Store, error) {
	if !filepath.IsAbs(directory) {
		return nil, fmt.Errorf("must be an absolute directory")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("must be a directory, not a symbolic link")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	if err := root.Chmod(".", 0700); err != nil {
		_ = root.Close()
		return nil, err
	}
	return &Store{root: root}, nil
}

// PutPNG writes the file completely before it appears under its final name, and refuses to replace an existing
// artifact.
func (s *Store) PutPNG(ctx context.Context, run, id string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.root.Mkdir(run, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := s.root.Lstat(run)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("artifact task path must be a directory")
	}
	if err := s.root.Chmod(run, 0700); err != nil {
		return err
	}
	temporary := filepath.Join(run, "."+uuid.NewString()+".tmp")
	file, err := s.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer s.root.Remove(temporary)
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if err := errors.Join(writeErr, file.Close(), ctx.Err()); err != nil {
		return err
	}
	err = s.root.Link(temporary, filepath.Join(run, id+".png"))
	if errors.Is(err, os.ErrExist) {
		return state.ErrFileExists
	}
	return err
}

func (s *Store) GetPNG(ctx context.Context, run, id string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := s.root.Open(filepath.Join(run, id+".png"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, state.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return state.ReadArtifact(file)
}

func mkdirAll(root *os.Root, dir string) error {
	clean := filepath.Clean(dir)
	if clean == "." || clean == "" {
		return nil
	}
	parts := strings.Split(filepath.ToSlash(clean), "/")
	cur := ""
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if cur == "" {
			cur = part
		} else {
			cur = filepath.Join(cur, part)
		}
		if err := root.Mkdir(cur, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := root.Lstat(cur)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("artifact path must be a directory")
		}
	}
	return nil
}

func (s *Store) PutFile(ctx context.Context, key, _ string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(key)
	if err := mkdirAll(s.root, dir); err != nil {
		return err
	}
	temporary := filepath.Join(dir, "."+uuid.NewString()+".tmp")
	file, err := s.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer s.root.Remove(temporary)
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if err := errors.Join(writeErr, file.Close(), ctx.Err()); err != nil {
		return err
	}
	err = s.root.Link(temporary, key)
	if errors.Is(err, os.ErrExist) {
		return state.ErrFileExists
	}
	return err
}

func (s *Store) GetFile(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := s.root.Open(key)
	if errors.Is(err, os.ErrNotExist) {
		return nil, state.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return state.ReadFile(file)
}

func (s *Store) DeleteFile(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.root.Remove(key); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Store) Close() error { return s.root.Close() }
