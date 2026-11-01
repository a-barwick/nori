package state

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

const (
	MaxHistoryBlockBytes = 512 << 10
	MaxHistoryBytes      = 16 << 20
	MaxHistoryBlocks     = 512
)

// HistoryBlock is one immutable exact JSON payload of a task's history, including opaque provider items. A block's
// reference binds the task, its parent and its payload, and blocks form a chain through Parent. No history is
// exported to tracing. Only references, not cumulative transcripts, belong in workflows.
type HistoryBlock struct {
	RunID  string `firestore:"run_id"`
	Parent string `firestore:"parent"`
	Data   []byte `firestore:"data"`
	Depth  int    `firestore:"depth"`
	Bytes  int    `firestore:"bytes"`
}

// ValidHistoryRef reports whether ref has the form of a history reference: a lowercase SHA-256 in hex.
func ValidHistoryRef(ref string) bool {
	decoded, err := hex.DecodeString(ref)
	return err == nil && len(decoded) == sha256.Size && ref == strings.ToLower(ref)
}

// HistoryID is the reference of a block with the given task, parent and payload.
func HistoryID(runID, parent string, data []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(runID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(parent))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}

// ValidHistoryLookup reports whether runID and ref can name a history block at all.
func ValidHistoryLookup(runID, ref string) bool {
	return ValidEvidenceID(runID) && ValidHistoryRef(ref)
}

// ValidateSavedHistoryBlock checks that a block read from storage is the block its reference names.
func ValidateSavedHistoryBlock(runID, ref string, block HistoryBlock) error {
	if block.RunID != runID || HistoryID(runID, block.Parent, block.Data) != ref ||
		len(block.Data) == 0 || len(block.Data) > MaxHistoryBlockBytes || !json.Valid(block.Data) ||
		(block.Parent != "" && !ValidHistoryRef(block.Parent)) ||
		block.Depth < 1 || block.Depth > MaxHistoryBlocks || block.Bytes < len(block.Data) || block.Bytes > MaxHistoryBytes {
		return errors.New("invalid saved task history block")
	}
	return nil
}

// ValidateHistoryAppend checks an append request before anything is read.
func ValidateHistoryAppend(runID, parent string, data []byte) error {
	if !ValidEvidenceID(runID) || (parent != "" && !ValidHistoryRef(parent)) || len(data) == 0 || len(data) > MaxHistoryBlockBytes ||
		!json.Valid(data) {
		return errors.New("task history requires a valid task, parent and JSON payload within 512 KiB")
	}
	return nil
}

// NextHistoryBlock builds the block that extends parent (nil for the first block) with data, and its reference.
func NextHistoryBlock(runID, parentRef string, parent *HistoryBlock, data []byte) (HistoryBlock, string, error) {
	block := HistoryBlock{RunID: runID, Parent: parentRef, Data: bytes.Clone(data), Depth: 1, Bytes: len(data)}
	if parent != nil {
		block.Depth, block.Bytes = parent.Depth+1, parent.Bytes+len(data)
	}
	if block.Depth > MaxHistoryBlocks || block.Bytes > MaxHistoryBytes {
		return HistoryBlock{}, "", errors.New("task history exceeds 512 blocks or 16 MiB")
	}
	return block, HistoryID(runID, parentRef, data), nil
}

// CheckHistoryReplay verifies that the block already saved under a reference is the one being appended again.
func CheckHistoryReplay(saved, block HistoryBlock) error {
	if saved.Parent != block.Parent || !bytes.Equal(saved.Data, block.Data) || saved.Depth != block.Depth || saved.Bytes != block.Bytes {
		return errors.New("task history identity conflict")
	}
	return nil
}

// HistoryChain walks a history from its newest block back to its first, failing closed on anything missing, foreign,
// malformed or over the limits rather than returning partial context.
type HistoryChain struct {
	blocks        [][]byte
	seen          map[string]bool
	total         int
	expectedDepth int
	expectedBytes int
}

func NewHistoryChain() *HistoryChain {
	return &HistoryChain{blocks: [][]byte{}, seen: map[string]bool{}}
}

// Visit records that ref is about to be read.
func (c *HistoryChain) Visit(ref string) error {
	if c.seen[ref] || len(c.blocks) >= MaxHistoryBlocks {
		return errors.New("invalid task history chain")
	}
	c.seen[ref] = true
	return nil
}

// Add takes the block just read and returns the reference of its parent, empty at the start of the history.
func (c *HistoryChain) Add(block HistoryBlock) (string, error) {
	if len(c.blocks) > 0 && (block.Depth != c.expectedDepth || block.Bytes != c.expectedBytes) {
		return "", errors.New("task history chain metadata conflict")
	}
	c.total += len(block.Data)
	if c.total > MaxHistoryBytes {
		return "", errors.New("task history exceeds 16 MiB")
	}
	c.blocks = append(c.blocks, bytes.Clone(block.Data))
	c.expectedDepth, c.expectedBytes = block.Depth-1, block.Bytes-len(block.Data)
	return block.Parent, nil
}

// Payloads returns the exact payloads in chronological order once the chain has been walked to its start.
func (c *HistoryChain) Payloads() ([][]byte, error) {
	if len(c.blocks) > 0 && (c.expectedDepth != 0 || c.expectedBytes != 0) {
		return nil, errors.New("task history chain is incomplete")
	}
	return Chronological(c.blocks), nil
}
