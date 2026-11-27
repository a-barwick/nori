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

type ArtifactStore interface {
	Artifacts
	Close() error
}

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

func ArtifactListed(run Run, id string) bool {
	for _, a := range run.Artifacts {
		if a.ID == id {
			return true
		}
	}
	return false
}

var ErrFileExists = errors.New("file already exists")

func ReadFile(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxUploadBytes+1))
	if len(data) > MaxUploadBytes {
		return nil, fmt.Errorf("file exceeds limit")
	}
	return data, err
}

func ReadArtifact(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxArtifactBytes+1))
	if len(data) > MaxArtifactBytes {
		return nil, fmt.Errorf("artifact exceeds limit")
	}
	return data, err
}

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

func (s *composed) AppendProactiveMessage(ctx context.Context, deliveryID string, message Message) (Message, error) {
	if r, ok := s.Database.(interface {
		AppendProactiveMessage(context.Context, string, Message) (Message, error)
	}); ok {
		return r.AppendProactiveMessage(ctx, deliveryID, message)
	}
	return Message{}, fmt.Errorf("proactive message append not supported by store")
}

func (s *composed) EnqueueProactiveTaskEvent(ctx context.Context, event TaskEvent) error {
	if r, ok := s.Database.(interface {
		EnqueueProactiveTaskEvent(context.Context, TaskEvent) error
	}); ok {
		return r.EnqueueProactiveTaskEvent(ctx, event)
	}
	return fmt.Errorf("proactive task event enqueue not supported by store")
}

func (s *composed) OperationsByRuns(ctx context.Context, runIDs []string) (map[string][]OperationRecord, error) {
	if r, ok := s.Database.(interface {
		OperationsByRuns(context.Context, []string) (map[string][]OperationRecord, error)
	}); ok {
		return r.OperationsByRuns(ctx, runIDs)
	}
	out := make(map[string][]OperationRecord, len(runIDs))
	for _, id := range runIDs {
		ops, err := s.Database.Operations(ctx, id)
		if err != nil {
			return nil, err
		}
		out[id] = ops
	}
	return out, nil
}

func (s *composed) GetRuns(ctx context.Context, ids []string) (map[string]Run, error) {
	if r, ok := s.Database.(interface {
		GetRuns(context.Context, []string) (map[string]Run, error)
	}); ok {
		return r.GetRuns(ctx, ids)
	}
	out := make(map[string]Run, len(ids))
	for _, id := range ids {
		if run, err := s.Database.GetRun(ctx, id); err == nil {
			out[run.ID] = run
		}
	}
	return out, nil
}

func (s *composed) ResourcesByIDs(ctx context.Context, collection string, ids []string) ([]Resource, error) {
	if r, ok := s.Database.(interface {
		ResourcesByIDs(context.Context, string, []string) ([]Resource, error)
	}); ok {
		return r.ResourcesByIDs(ctx, collection, ids)
	}
	out := make([]Resource, 0, len(ids))
	for _, id := range ids {
		if res, err := s.Database.Resource(ctx, collection, id); err == nil {
			out = append(out, res)
		}
	}
	return out, nil
}

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
