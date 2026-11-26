package records

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"nori/internal/state"
	"nori/internal/state/docstore"
)

func (d *Database) MemoryLogs(ctx context.Context, afterDate string, limit int) ([]state.MemoryLog, error) {
	const pageSize = 64
	query := d.memoryLogs().OrderBy(docstore.DocumentID, docstore.Desc).Limit(pageSize)
	if afterDate != "" {
		query = query.Where(docstore.DocumentID, ">=", afterDate)
	}
	out := make([]state.MemoryLog, 0)
	var date string
	var shards []state.MemoryLog
	for {
		page, err := query.Documents(ctx).GetAll()
		if err != nil {
			return nil, err
		}
		for _, doc := range page {
			shardDate := parseLogDateFromDocID(doc.Ref.ID)
			if shardDate != date {
				if merged, ok := mergeDailyLogShards(date, shards); ok {
					out = append(out, merged)
					if limit > 0 && len(out) == limit {
						return out, nil
					}
				}
				date, shards = shardDate, shards[:0]
			}
			var shard state.MemoryLog
			if err := doc.DataTo(&shard); err != nil {
				return nil, err
			}
			shard.Date = date
			shard.Shard = effectiveLogShard(shard.Shard, doc.Ref.ID)
			shards = append(shards, shard)
		}
		if len(page) < pageSize {
			break
		}
		query = query.StartAfter(page[len(page)-1])
	}
	if merged, ok := mergeDailyLogShards(date, shards); ok {
		out = append(out, merged)
	}
	return out, nil
}

func (d *Database) MemoryLog(ctx context.Context, date string) (state.MemoryLog, error) {
	if !state.ValidMemoryDate(date) {
		return state.MemoryLog{}, state.ErrNotFound
	}
	docs, err := d.memoryLogs().
		Where(docstore.DocumentID, ">=", date).
		Where(docstore.DocumentID, "<", date+"$").
		OrderBy(docstore.DocumentID, docstore.Asc).
		Documents(ctx).GetAll()
	if err != nil {
		return state.MemoryLog{}, err
	}
	shards := make([]state.MemoryLog, 0, len(docs))
	for _, doc := range docs {
		var shard state.MemoryLog
		if err := doc.DataTo(&shard); err != nil {
			return state.MemoryLog{}, err
		}
		if shard.Date == "" {
			shard.Date = parseLogDateFromDocID(doc.Ref.ID)
		}
		if shard.Date != date {
			continue
		}
		shard.Shard = effectiveLogShard(shard.Shard, doc.Ref.ID)
		shards = append(shards, shard)
	}
	merged, ok := mergeDailyLogShards(date, shards)
	if !ok {
		return state.MemoryLog{}, state.ErrNotFound
	}
	return merged, nil
}

func parseLogDateFromDocID(docID string) string {
	date, _, _ := strings.Cut(docID, "#")
	return date
}

func effectiveLogShard(shard int, docID string) int {
	if shard > 0 {
		return shard
	}
	if _, suffix, ok := strings.Cut(docID, "#"); ok {
		if n, err := strconv.Atoi(suffix); err == nil && n > 0 {
			return n
		}
	}
	return 1
}

func mergeDailyLogShards(date string, shards []state.MemoryLog) (state.MemoryLog, bool) {
	if len(shards) == 0 {
		return state.MemoryLog{}, false
	}
	ordered := slices.Clone(shards)
	slices.SortFunc(ordered, func(a, b state.MemoryLog) int {
		return cmp.Compare(a.Shard, b.Shard)
	})
	var entries []state.MemoryLogEntry
	var maxUpdated time.Time
	for _, s := range ordered {
		entries = append(entries, s.Entries...)
		if s.UpdatedAt.After(maxUpdated) {
			maxUpdated = s.UpdatedAt
		}
	}
	if len(entries) == 0 {
		return state.MemoryLog{}, false
	}
	return state.MemoryLog{
		Date:      date,
		Shard:     1,
		Entries:   entries,
		UpdatedAt: maxUpdated,
	}, true
}

func (d *Database) readDailyLogShards(tx *docstore.Transaction, date string) ([]state.MemoryLog, error) {
	if !state.ValidMemoryDate(date) {
		return nil, state.ErrInvalidResource
	}
	it := tx.Documents(d.memoryLogs().
		Where(docstore.DocumentID, ">=", date).
		Where(docstore.DocumentID, "<", date+"$").
		OrderBy(docstore.DocumentID, docstore.Asc))
	defer it.Stop()
	docs, err := it.GetAll()
	if err != nil {
		return nil, err
	}
	shards := make([]state.MemoryLog, 0, len(docs))
	for _, doc := range docs {
		var shard state.MemoryLog
		if err := doc.DataTo(&shard); err != nil {
			return nil, err
		}
		if shard.Date == "" {
			shard.Date = parseLogDateFromDocID(doc.Ref.ID)
		}
		if shard.Date != date {
			continue
		}
		shard.Shard = effectiveLogShard(shard.Shard, doc.Ref.ID)
		shards = append(shards, shard)
	}
	slices.SortFunc(shards, func(a, b state.MemoryLog) int {
		return cmp.Compare(a.Shard, b.Shard)
	})
	return shards, nil
}

func (d *Database) writeDailyLogAppend(
	tx *docstore.Transaction,
	date string,
	existing []state.MemoryLog,
	incoming []state.MemoryLogEntry,
	now time.Time,
) error {
	if len(incoming) == 0 {
		return nil
	}
	shards := make([]state.MemoryLog, len(existing))
	seen := map[string]bool{}
	for i, s := range existing {
		cp := s
		cp.Entries = slices.Clone(s.Entries)
		if cp.Date == "" {
			cp.Date = date
		}
		if cp.Shard <= 0 {
			cp.Shard = i + 1
		}
		shards[i] = cp
		for _, e := range cp.Entries {
			seen[e.ID] = true
		}
	}

	nowUTC := now.UTC()
	dirty := map[int]bool{}
	added := make([]state.MemoryLogEntry, 0, len(incoming))

	for _, entry := range incoming {
		if strings.TrimSpace(entry.ID) == "" || strings.TrimSpace(entry.Text) == "" {
			continue
		}
		if seen[entry.ID] {
			continue
		}
		if entry.CreatedAt.IsZero() {
			entry.CreatedAt = nowUTC
		} else {
			entry.CreatedAt = entry.CreatedAt.UTC()
		}
		if entry.UpdatedAt.IsZero() || entry.UpdatedAt.Before(entry.CreatedAt) {
			entry.UpdatedAt = entry.CreatedAt
		} else {
			entry.UpdatedAt = entry.UpdatedAt.UTC()
		}
		if entry.Provenance == "" {
			entry.Provenance = state.ProvenancePreCompactFlush
		}
		seen[entry.ID] = true

		needNewShard := len(shards) == 0
		if !needNewShard {
			last := &shards[len(shards)-1]
			if len(last.Entries) >= state.MaxDailyLogEntries {
				needNewShard = true
			} else if len(last.Entries) > 0 && dailyLogShardSizeWith(*last, entry, nowUTC) > state.MaxDailyLogBytes {
				needNewShard = true
			}
		}
		if needNewShard {
			nextShard := 1
			if len(shards) > 0 {
				nextShard = shards[len(shards)-1].Shard + 1
			}
			shards = append(shards, state.MemoryLog{
				Date:      date,
				Shard:     nextShard,
				Entries:   []state.MemoryLogEntry{entry},
				UpdatedAt: nowUTC,
			})
			dirty[len(shards)-1] = true
		} else {
			lastIdx := len(shards) - 1
			shards[lastIdx].Entries = append(shards[lastIdx].Entries, entry)
			shards[lastIdx].UpdatedAt = nowUTC
			dirty[lastIdx] = true
		}
		added = append(added, entry)
	}

	if len(added) == 0 {
		return nil
	}

	for i, shard := range shards {
		if !dirty[i] {
			continue
		}
		if err := tx.Set(d.memoryLogs().Doc(state.MemoryLogDocID(date, shard.Shard)), shard); err != nil {
			return err
		}
	}
	owner := d.user.DataOwner()
	for _, entry := range added {
		if err := tx.UpsertMemoryChunk(logEntryChunk(owner, date, entry)); err != nil {
			return err
		}
	}
	return nil
}

func dailyLogShardSizeWith(shard state.MemoryLog, extra state.MemoryLogEntry, updatedAt time.Time) int {
	candidate := state.MemoryLog{
		Date:      shard.Date,
		Shard:     shard.Shard,
		Entries:   append(slices.Clone(shard.Entries), extra),
		UpdatedAt: updatedAt,
	}
	raw, _ := json.Marshal(candidate)
	return len(raw)
}

func dailyLogContainsEntryID(shards []state.MemoryLog, entryID string) bool {
	for _, s := range shards {
		for _, e := range s.Entries {
			if e.ID == entryID {
				return true
			}
		}
	}
	return false
}

func findDailyLogEntryPos(shards []state.MemoryLog, entryID string) (shardIdx, entryIdx int, logRev int64, found bool) {
	var maxUpdated time.Time
	shardIdx, entryIdx = -1, -1
	for i, s := range shards {
		if s.UpdatedAt.After(maxUpdated) {
			maxUpdated = s.UpdatedAt
		}
		for j, e := range s.Entries {
			if e.ID == entryID {
				shardIdx, entryIdx, found = i, j, true
			}
		}
	}
	return shardIdx, entryIdx, maxUpdated.UnixMilli(), found
}

func findDailyLogEntry(shards []state.MemoryLog, entryID string) (state.MemoryLogEntry, int64, bool) {
	si, ei, rev, ok := findDailyLogEntryPos(shards, entryID)
	if !ok {
		return state.MemoryLogEntry{}, rev, false
	}
	return shards[si].Entries[ei], rev, true
}

func buildTaskOutcomeEntry(run state.Run, now time.Time) (state.MemoryLogEntry, bool) {
	if run.Status != state.RunCompleted || strings.TrimSpace(run.ID) == "" {
		return state.MemoryLogEntry{}, false
	}
	reqText := strings.Join(strings.Fields(run.Request), " ")
	resText := strings.Join(strings.Fields(run.Result), " ")
	var text string
	switch {
	case reqText != "" && resText != "":
		prefix := docstore.ClipUTF8(reqText, 160)
		text = docstore.ClipUTF8(prefix+": "+resText, state.MaxTaskOutcomeBytes)
	case resText != "":
		text = docstore.ClipUTF8(resText, state.MaxTaskOutcomeBytes)
	case reqText != "":
		text = docstore.ClipUTF8("Completed task: "+reqText, state.MaxTaskOutcomeBytes)
	default:
		text = docstore.ClipUTF8("Completed task "+run.ID, state.MaxTaskOutcomeBytes)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return state.MemoryLogEntry{}, false
	}
	nowUTC := now.UTC()
	return state.MemoryLogEntry{
		ID:         "task_" + run.ID,
		Section:    "Completed Work",
		Text:       text,
		About:      "task",
		Provenance: state.ProvenanceTaskOutcome,
		RunID:      run.ID,
		CreatedAt:  nowUTC,
		UpdatedAt:  nowUTC,
	}, true
}
