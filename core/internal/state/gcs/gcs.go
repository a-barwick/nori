// Package gcs stores artifact bytes in a Cloud Storage bucket. It implements state.ArtifactStore.
package gcs

import (
	"bytes"
	"context"
	"io"

	"cloud.google.com/go/storage"
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

func (s *Store) Close() error { return s.client.Close() }
