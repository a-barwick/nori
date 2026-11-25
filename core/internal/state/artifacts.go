package state

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
)

const MaxArtifactBytes = 2 << 20

// ArtifactStore persists the bytes of private PNG artifacts, one object per run and artifact ID. An implementation
// stores exactly the bytes it is given and never replaces an existing artifact. Compose validates the IDs and the
// size before calling PutPNG or GetPNG, and GetPNG returns at most MaxArtifactBytes (see ReadArtifact).
type ArtifactStore interface {
	Artifacts
	Close() error
}

// ValidateArtifactIDs requires canonical UUIDs, so an ID can never name anything but one object.
func ValidateArtifactIDs(run, id string) error {
	for _, value := range []string{run, id} {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed.String() != value {
			return fmt.Errorf("artifact run and ID must be canonical UUIDs")
		}
	}
	return nil
}

func ValidatePNG(run, id string, data []byte) error {
	if err := ValidateArtifactIDs(run, id); err != nil {
		return err
	}
	if len(data) > MaxArtifactBytes {
		return fmt.Errorf("screenshot exceeds 2 MiB")
	}
	return nil
}

// ArtifactListed reports whether a run records the artifact. Only recorded artifacts can be read.
func ArtifactListed(run Run, id string) bool {
	for _, a := range run.Artifacts {
		if a.ID == id {
			return true
		}
	}
	return false
}

var ErrFileExists = errors.New("file already exists")

// ReadFile reads a stored file, refusing anything over the limit without reading it all.
func ReadFile(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxUploadBytes+1))
	if len(data) > MaxUploadBytes {
		return nil, fmt.Errorf("file exceeds limit")
	}
	return data, err
}

// ReadArtifact reads an artifact's bytes, refusing anything over MaxArtifactBytes without reading it all.
func ReadArtifact(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxArtifactBytes+1))
	if len(data) > MaxArtifactBytes {
		return nil, fmt.Errorf("artifact exceeds limit")
	}
	return data, err
}

// Compose joins a document backend and an artifact store into the store the rest of Nori uses. Artifact IDs are
// validated here, and an artifact can be read only when its run records it, so a blob adapter holds only bytes.
func Compose(db Database, artifacts ArtifactStore) ConversationStore {
	return &composed{Database: db, artifacts: artifacts}
}

type composed struct {
	Database
	artifacts ArtifactStore
}

func (s *composed) UsageWindowSummary(ctx context.Context, hourCutoff, dayCutoff time.Time) (int, float64, int, error) {
	if r, ok := s.Database.(interface {
		UsageWindowSummary(context.Context, time.Time, time.Time) (int, float64, int, error)
	}); ok {
		return r.UsageWindowSummary(ctx, hourCutoff, dayCutoff)
	}
	return 0, 0, 0, nil
}

// ErrReserveUnsupported indicates the underlying Database does not implement atomic
// single-transaction quota reservation and callers should fall back to WindowReader.
var ErrReserveUnsupported = errors.New("state: atomic usage reservation unsupported")

func (s *composed) ReserveUsage(
	ctx context.Context,
	u Usage,
	hourCutoff, dayCutoff time.Time,
	maxCallsPerHour int,
	maxUSDPerDay float64,
	unknownEstimateUSD float64,
) (bool, error) {
	if r, ok := s.Database.(interface {
		ReserveUsage(context.Context, Usage, time.Time, time.Time, int, float64, float64) (bool, error)
	}); ok {
		return r.ReserveUsage(ctx, u, hourCutoff, dayCutoff, maxCallsPerHour, maxUSDPerDay, unknownEstimateUSD)
	}
	return false, ErrReserveUnsupported
}

func (s *composed) PutPNG(ctx context.Context, run, id string, data []byte) error {
	if err := ValidatePNG(run, id, data); err != nil {
		return err
	}
	return s.artifacts.PutPNG(ctx, run, id, data)
}

func (s *composed) GetPNG(ctx context.Context, run, id string) ([]byte, error) {
	if err := ValidateArtifactIDs(run, id); err != nil {
		return nil, err
	}
	record, err := s.GetRun(ctx, run)
	if err != nil {
		return nil, err
	}
	if !ArtifactListed(record, id) {
		return nil, ErrNotFound
	}
	return s.artifacts.GetPNG(ctx, run, id)
}

func (s *composed) PutFile(ctx context.Context, key, mediaType string, data []byte) error {
	if !ValidFileKey(key) || len(data) > MaxUploadBytes {
		return fmt.Errorf("invalid file")
	}
	return s.artifacts.PutFile(ctx, key, mediaType, data)
}

func (s *composed) GetFile(ctx context.Context, key string) ([]byte, error) {
	if !ValidFileKey(key) {
		return nil, ErrNotFound
	}
	return s.artifacts.GetFile(ctx, key)
}

func (s *composed) DeleteFile(ctx context.Context, key string) error {
	if !ValidFileKey(key) {
		return ErrNotFound
	}
	return s.artifacts.DeleteFile(ctx, key)
}

func (s *composed) Close() error { return errors.Join(s.Database.Close(), s.artifacts.Close()) }
