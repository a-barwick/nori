// Package gcs stores artifact bytes in a Cloud Storage bucket. It implements state.ArtifactStore.
package gcs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"nori/internal/state"
)

// Store keeps one object per artifact under artifacts/<run>/<id>.png.
type Store struct {
	client *storage.Client
	bucket string
}

var _ state.ArtifactStore = (*Store)(nil)

// Open connects to Cloud Storage with Application Default Credentials. The bucket name is not checked here, so a
// process that never stores artifacts needs none.
func Open(ctx context.Context, bucket string) (*Store, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, err
	}
	return &Store{client: client, bucket: bucket}, nil
}

func objectPath(run, id string) string { return "artifacts/" + run + "/" + id + ".png" }

// PutPNG refuses to replace an existing artifact.
func (s *Store) PutPNG(ctx context.Context, run, id string, data []byte) error {
	w := s.client.Bucket(s.bucket).Object(objectPath(run, id)).If(storage.Conditions{DoesNotExist: true}).NewWriter(ctx)
	w.ContentType = "image/png"
	w.CacheControl = "private, no-store"
	if _, err := io.Copy(w, bytes.NewReader(data)); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

func (s *Store) GetPNG(ctx context.Context, run, id string) ([]byte, error) {
	reader, err := s.client.Bucket(s.bucket).Object(objectPath(run, id)).NewReader(ctx)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return state.ReadArtifact(reader)
}

// PutFile keeps files under artifacts/ too, because that's the prefix the runtime's storage role allows. It refuses to
// replace an existing file. A dropped write commits nothing.
func (s *Store) PutFile(ctx context.Context, key, mediaType string, data []byte) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := s.client.Bucket(s.bucket).Object("artifacts/" + key).If(storage.Conditions{DoesNotExist: true}).NewWriter(ctx)
	w.ContentType = mediaType
	w.CacheControl = "private, no-store"
	if _, err := io.Copy(w, bytes.NewReader(data)); err != nil {
		cancel()
		_ = w.Close()
		return err
	}
	err := w.Close()
	var api *googleapi.Error
	if errors.As(err, &api) && api.Code == http.StatusPreconditionFailed {
		return state.ErrFileExists
	}
	return err
}

func (s *Store) GetFile(ctx context.Context, key string) ([]byte, error) {
	reader, err := s.client.Bucket(s.bucket).Object("artifacts/" + key).NewReader(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil, state.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return state.ReadFile(reader)
}

func (s *Store) DeleteFile(ctx context.Context, key string) error {
	err := s.client.Bucket(s.bucket).Object("artifacts/" + key).Delete(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil
	}
	return err
}

func (s *Store) Close() error { return s.client.Close() }
