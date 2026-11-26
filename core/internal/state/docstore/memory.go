package docstore

import (
	"context"
	"time"
	"unicode/utf8"
)

const (
	MemoryLayerCore = "core"
	MemoryLayerLog  = "log"

	DefaultMemorySearchLimit   = 8
	MaxMemorySearchLimit       = 20
	MaxMemorySnippetBytes      = 700
	MemorySearchCandidateLimit = 32
	MemoryRecencyHalfLifeDays  = 30.0
	MemoryMMRLambda            = 0.75
	MemoryPinnedBoost          = 1.15
)

type MemoryChunk struct {
	Owner       string    `json:"owner"`
	ChunkID     string    `json:"chunk_id"`
	Layer       string    `json:"layer"`
	LogDate     string    `json:"log_date,omitempty"`
	Section     string    `json:"section,omitempty"`
	About       string    `json:"about,omitempty"`
	Body        string    `json:"body"`
	Provenance  string    `json:"provenance"`
	SourceInput string    `json:"source_input,omitempty"`
	RunID       string    `json:"run_id,omitempty"`
	Pinned      bool      `json:"pinned,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type MemorySearchQuery struct {
	Owner        string
	Query        string
	Layer        string
	CreatedAfter time.Time
	Limit        int
	Now          time.Time
}

type MemorySearchHit struct {
	Chunk   MemoryChunk `json:"chunk"`
	Snippet string      `json:"snippet"`
	Score   float64     `json:"score"`
}

type MemoryIndexWriter interface {
	UpsertMemoryChunk(ctx context.Context, chunk MemoryChunk) error
	DeleteMemoryChunk(ctx context.Context, owner, chunkID string) error
}

type MemoryIndex interface {
	SearchMemory(ctx context.Context, query MemorySearchQuery) ([]MemorySearchHit, error)
	ListMemoryChunks(ctx context.Context, owner string) ([]MemoryChunk, error)
	MemoryIndexWriter
}

type BackendUnwrapper interface {
	Unwrap() Backend
}

func AsMemoryIndex(b Backend) (MemoryIndex, bool) {
	for b != nil {
		if idx, ok := b.(MemoryIndex); ok {
			return idx, true
		}
		u, ok := b.(BackendUnwrapper)
		if !ok {
			return nil, false
		}
		b = u.Unwrap()
	}
	return nil, false
}

func MemoryCoreChunkID(memoryID string) string {
	return "core:" + memoryID
}

func MemoryLogChunkID(date, entryID string) string {
	return "log:" + date + ":" + entryID
}

func ClipUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func (t *Transaction) HasMemoryIndex() bool {
	_, ok := t.backend.(MemoryIndexWriter)
	return ok
}

func (t *Transaction) UpsertMemoryChunk(chunk MemoryChunk) error {
	w, ok := t.backend.(MemoryIndexWriter)
	if !ok {
		return nil
	}
	return w.UpsertMemoryChunk(t.ctx, chunk)
}

func (t *Transaction) DeleteMemoryChunk(owner, chunkID string) error {
	w, ok := t.backend.(MemoryIndexWriter)
	if !ok {
		return nil
	}
	return w.DeleteMemoryChunk(t.ctx, owner, chunkID)
}

func (c *Client) HasMemoryIndex() bool {
	_, ok := AsMemoryIndex(c.Backend)
	return ok
}

func (c *Client) SearchMemory(ctx context.Context, query MemorySearchQuery) ([]MemorySearchHit, bool, error) {
	idx, ok := AsMemoryIndex(c.Backend)
	if !ok {
		return nil, false, nil
	}
	hits, err := idx.SearchMemory(ctx, query)
	return hits, true, err
}

func (c *Client) ListMemoryChunks(ctx context.Context, owner string) ([]MemoryChunk, bool, error) {
	idx, ok := AsMemoryIndex(c.Backend)
	if !ok {
		return nil, false, nil
	}
	chunks, err := idx.ListMemoryChunks(ctx, owner)
	return chunks, true, err
}
