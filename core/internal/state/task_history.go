package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	MaxHistoryBlockBytes = 512 << 10
	MaxHistoryBytes      = 16 << 20
	MaxHistoryBlocks     = 512
)

// History blocks are immutable exact JSON payloads, including opaque provider
// items. References bind the task, parent and payload; no history is exported to
// tracing. Only references, not cumulative transcripts, belong in workflows.
type historyBlock struct {
	RunID  string `firestore:"run_id"`
	Parent string `firestore:"parent"`
	Data   []byte `firestore:"data"`
	Depth  int    `firestore:"depth"`
	Bytes  int    `firestore:"bytes"`
}

func validEvidenceID(id string) bool {
	return id != "" && len(id) <= 256 && id != "." && id != ".." && !strings.ContainsAny(id, "/\\\x00")
}

func validHistoryRef(ref string) bool {
	decoded, err := hex.DecodeString(ref)
	return err == nil && len(decoded) == sha256.Size && ref == strings.ToLower(ref)
}

func historyID(runID, parent string, data []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(runID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(parent))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}

func (s *Google) readHistoryBlock(ctx context.Context, runID, ref string) (historyBlock, error) {
	var block historyBlock
	if !validEvidenceID(runID) || !validHistoryRef(ref) {
		return block, errors.New("invalid task history reference")
	}
	doc, err := s.runs().Doc(runID).Collection("history").Doc(ref).Get(ctx)
	if err != nil {
		return block, err
	}
	if err := doc.DataTo(&block); err != nil {
		return block, err
	}
	if block.RunID != runID || historyID(runID, block.Parent, block.Data) != ref ||
		len(block.Data) == 0 || len(block.Data) > MaxHistoryBlockBytes || !json.Valid(block.Data) ||
		(block.Parent != "" && !validHistoryRef(block.Parent)) ||
		block.Depth < 1 || block.Depth > MaxHistoryBlocks || block.Bytes < len(block.Data) || block.Bytes > MaxHistoryBytes {
		return historyBlock{}, errors.New("invalid saved task history block")
	}
	return block, nil
}

func (s *Google) AppendHistory(ctx context.Context, runID, parent string, data []byte) (string, error) {
	if !validEvidenceID(runID) || (parent != "" && !validHistoryRef(parent)) || len(data) == 0 || len(data) > MaxHistoryBlockBytes || !json.Valid(data) {
		return "", errors.New("task history requires a valid task, parent and JSON payload within 512 KiB")
	}
	if _, err := s.GetRun(ctx, runID); err != nil {
		return "", err
	}
	block := historyBlock{RunID: runID, Parent: parent, Data: bytes.Clone(data), Depth: 1, Bytes: len(data)}
	if parent != "" {
		previous, err := s.readHistoryBlock(ctx, runID, parent)
		if err != nil {
			return "", fmt.Errorf("task history parent unavailable: %w", err)
		}
		block.Depth, block.Bytes = previous.Depth+1, previous.Bytes+len(data)
	}
	if block.Depth > MaxHistoryBlocks || block.Bytes > MaxHistoryBytes {
		return "", errors.New("task history exceeds 512 blocks or 16 MiB")
	}
	ref := historyID(runID, parent, data)
	_, err := s.runs().Doc(runID).Collection("history").Doc(ref).Create(ctx, block)
	if status.Code(err) == codes.AlreadyExists {
		saved, readErr := s.readHistoryBlock(ctx, runID, ref)
		if readErr != nil {
			return "", readErr
		}
		if saved.Parent != parent || !bytes.Equal(saved.Data, data) || saved.Depth != block.Depth || saved.Bytes != block.Bytes {
			return "", errors.New("task history identity conflict")
		}
		return ref, nil
	}
	if err != nil {
		return "", err
	}
	return ref, nil
}

// ReadHistory returns exact payloads in chronological order. Missing, foreign,
// malformed or over-limit chains fail closed rather than return partial context.
func (s *Google) ReadHistory(ctx context.Context, runID, ref string) ([][]byte, error) {
	if !validEvidenceID(runID) {
		return nil, errors.New("invalid task history task")
	}
	if _, err := s.GetRun(ctx, runID); err != nil {
		return nil, err
	}
	blocks := [][]byte{}
	seen := map[string]bool{}
	total, expectedDepth, expectedBytes := 0, 0, 0
	for ref != "" {
		if seen[ref] || len(blocks) >= MaxHistoryBlocks {
			return nil, errors.New("invalid task history chain")
		}
		seen[ref] = true
		block, err := s.readHistoryBlock(ctx, runID, ref)
		if err != nil {
			return nil, err
		}
		if len(blocks) > 0 && (block.Depth != expectedDepth || block.Bytes != expectedBytes) {
			return nil, errors.New("task history chain metadata conflict")
		}
		total += len(block.Data)
		if total > MaxHistoryBytes {
			return nil, errors.New("task history exceeds 16 MiB")
		}
		blocks = append(blocks, bytes.Clone(block.Data))
		expectedDepth, expectedBytes = block.Depth-1, block.Bytes-len(block.Data)
		ref = block.Parent
	}
	if len(blocks) > 0 && (expectedDepth != 0 || expectedBytes != 0) {
		return nil, errors.New("task history chain is incomplete")
	}
	for i, j := 0, len(blocks)-1; i < j; i, j = i+1, j-1 {
		blocks[i], blocks[j] = blocks[j], blocks[i]
	}
	return blocks, nil
}
