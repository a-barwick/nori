// Package encrypted provides application-layer envelope encryption wrappers for
// Nori artifact blobs and string record payloads, supporting zero-downtime
// migration from legacy plaintext data.
package encrypted

import (
	"bytes"
	"context"
	"errors"

	"nori/internal/state"
)

// ArtifactMagic prefixes encrypted artifact blobs so readers can transparently
// distinguish encrypted blobs from legacy plaintext blobs.
const ArtifactMagic = "NORIENC1"

var artifactMagicBytes = []byte(ArtifactMagic)

// Sealer encrypts and decrypts tenant-scoped byte payloads with AAD binding.
// *tenantkey.Manager satisfies this interface.
type Sealer interface {
	Seal(ctx context.Context, scope, recordID string, plaintext []byte) ([]byte, error)
	Open(ctx context.Context, scope, recordID string, ciphertext []byte) ([]byte, error)
}

// ArtifactStore wraps any state.ArtifactStore to encrypt PNGs and uploaded/result
// files at rest using a per-tenant Sealer. Legacy plaintext blobs without the
// ArtifactMagic header are returned untouched on read.
type ArtifactStore struct {
	Inner  state.ArtifactStore
	Sealer Sealer
}

var _ state.ArtifactStore = (*ArtifactStore)(nil)

// NewArtifactStore creates an encrypting ArtifactStore wrapping inner.
func NewArtifactStore(inner state.ArtifactStore, sealer Sealer) (*ArtifactStore, error) {
	if inner == nil {
		return nil, errors.New("encrypted artifact store requires an inner store")
	}
	if sealer == nil {
		return nil, errors.New("encrypted artifact store requires a sealer")
	}
	return &ArtifactStore{Inner: inner, Sealer: sealer}, nil
}

func (s *ArtifactStore) validate() error {
	if s == nil || s.Inner == nil {
		return errors.New("encrypted artifact store has no inner store")
	}
	if s.Sealer == nil {
		return errors.New("encrypted artifact store has no sealer")
	}
	return nil
}

func (s *ArtifactStore) sealArtifact(ctx context.Context, scope, recordID string, data []byte) ([]byte, error) {
	ct, err := s.Sealer.Seal(ctx, scope, recordID, data)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(artifactMagicBytes)+len(ct))
	copy(out, artifactMagicBytes)
	copy(out[len(artifactMagicBytes):], ct)
	return out, nil
}

func (s *ArtifactStore) openArtifact(ctx context.Context, scope, recordID string, raw []byte) ([]byte, error) {
	if !bytes.HasPrefix(raw, artifactMagicBytes) {
		return raw, nil
	}
	return s.Sealer.Open(ctx, scope, recordID, raw[len(artifactMagicBytes):])
}

func (s *ArtifactStore) PutPNG(ctx context.Context, run, id string, data []byte) error {
	if err := s.validate(); err != nil {
		return err
	}
	sealed, err := s.sealArtifact(ctx, "artifact:png", run+"/"+id, data)
	if err != nil {
		return err
	}
	return s.Inner.PutPNG(ctx, run, id, sealed)
}

func (s *ArtifactStore) GetPNG(ctx context.Context, run, id string) ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	raw, err := s.Inner.GetPNG(ctx, run, id)
	if err != nil {
		return nil, err
	}
	return s.openArtifact(ctx, "artifact:png", run+"/"+id, raw)
}

func (s *ArtifactStore) PutFile(ctx context.Context, key, mediaType string, data []byte) error {
	if err := s.validate(); err != nil {
		return err
	}
	sealed, err := s.sealArtifact(ctx, "artifact:file", key, data)
	if err != nil {
		return err
	}
	return s.Inner.PutFile(ctx, key, mediaType, sealed)
}

func (s *ArtifactStore) GetFile(ctx context.Context, key string) ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	raw, err := s.Inner.GetFile(ctx, key)
	if err != nil {
		return nil, err
	}
	return s.openArtifact(ctx, "artifact:file", key, raw)
}

func (s *ArtifactStore) DeleteFile(ctx context.Context, key string) error {
	if s == nil || s.Inner == nil {
		return errors.New("encrypted artifact store has no inner store")
	}
	return s.Inner.DeleteFile(ctx, key)
}

func (s *ArtifactStore) Close() error {
	if s == nil || s.Inner == nil {
		return nil
	}
	return s.Inner.Close()
}
