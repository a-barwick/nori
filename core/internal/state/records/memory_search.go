package records

import (
	"context"

	"nori/internal/state"
	"nori/internal/state/docstore"
)

func coreChunk(owner string, m state.Memory) docstore.MemoryChunk {
	createdAt := m.CreatedAt.UTC()
	updatedAt := m.UpdatedAt.UTC()
	if updatedAt.IsZero() || updatedAt.Before(createdAt) {
		updatedAt = createdAt
	}
	return docstore.MemoryChunk{
		Owner:       owner,
		ChunkID:     docstore.MemoryCoreChunkID(m.ID),
		Layer:       docstore.MemoryLayerCore,
		Section:     m.Section,
		About:       m.About,
		Body:        m.Text,
		Provenance:  string(m.EffectiveProvenance()),
		SourceInput: m.SourceInputID,
		Pinned:      m.Pinned,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}
}

func logEntryChunk(owner, date string, e state.MemoryLogEntry) docstore.MemoryChunk {
	createdAt := e.CreatedAt.UTC()
	updatedAt := e.UpdatedAt.UTC()
	if updatedAt.IsZero() || updatedAt.Before(createdAt) {
		updatedAt = createdAt
	}
	prov := e.Provenance
	if prov == "" {
		prov = state.ProvenancePreCompactFlush
	}
	return docstore.MemoryChunk{
		Owner:       owner,
		ChunkID:     docstore.MemoryLogChunkID(date, e.ID),
		Layer:       docstore.MemoryLayerLog,
		LogDate:     date,
		Section:     e.Section,
		About:       e.About,
		Body:        e.Text,
		Provenance:  string(prov),
		SourceInput: e.SourceInputID,
		RunID:       e.RunID,
		Pinned:      false,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}
}

func (d *Database) SearchMemory(ctx context.Context, query docstore.MemorySearchQuery) ([]docstore.MemorySearchHit, error) {
	query.Owner = d.user.DataOwner()
	if query.Now.IsZero() {
		query.Now = d.now()
	}
	if hits, ok, err := d.db.SearchMemory(ctx, query); ok {
		return hits, err
	}
	return []docstore.MemorySearchHit{}, nil
}
