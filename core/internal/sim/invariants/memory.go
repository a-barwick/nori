package invariants

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

const maxConversationExcerptBytes = 32 << 10

type memoryBaseline struct {
	hadSummary                bool
	epoch                     time.Time
	flushedThrough            time.Time
	extractedThrough          time.Time
	through                   time.Time
	flushFailures             int
	lastFailureThrough        time.Time
	extractFailures           int
	lastExtractFailureThrough time.Time
	summaryItems              map[string]state.SummaryItem
	dismissedIDs              map[string]bool
	pinnedCore                map[string]state.Memory
}

type userMemorySnapshot struct {
	userID        string
	owner         string
	target        UserTarget
	conv          state.ConversationControl
	hasConv       bool
	summary       state.ConversationSummary
	hasSummary    bool
	memories      map[string]state.Memory
	logs          map[string]state.MemoryLog
	inputs        map[string]state.ConversationInput
	turns         map[string]state.ConversationTurn
	attempts      map[string]state.ConversationAttempt
	manualDemoted map[string]bool
	chunks        map[string]docstore.MemoryChunk
	msgs          []state.Message
}

func decodeMemoriesFromSnapshot(snap memstore.BackendSnapshot, owner string) (map[string]state.Memory, []Violation) {
	col := "owners/" + owner + "/memories"
	docs := snap.Collections[col]
	out := make(map[string]state.Memory, len(docs))
	var vs []Violation
	for _, id := range sortedKeys(docs) {
		var m state.Memory
		if err := docstore.Decode(docs[id], &m); err != nil {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-03.decode",
				EntityID:  id,
				Message:   fmt.Sprintf("failed to decode memory document: %v", err),
			})
			continue
		}
		if m.ID != id {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-03.id_matches_doc",
				EntityID:  id,
				Message:   fmt.Sprintf("memory document ID %q does not match Memory.ID %q", id, m.ID),
			})
		}
		out[id] = m
	}
	return out, vs
}

func decodeMemoryLogsFromSnapshot(snap memstore.BackendSnapshot, owner string) (map[string]state.MemoryLog, []Violation) {
	col := "owners/" + owner + "/memory_logs"
	docs := snap.Collections[col]
	out := make(map[string]state.MemoryLog, len(docs))
	var vs []Violation
	for _, id := range sortedKeys(docs) {
		var logDoc state.MemoryLog
		if err := docstore.Decode(docs[id], &logDoc); err != nil {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-06.decode_log",
				EntityID:  id,
				Message:   fmt.Sprintf("failed to decode memory_log document: %v", err),
			})
			continue
		}
		if logDoc.Date == "" {
			date, _, _ := strings.Cut(id, "#")
			logDoc.Date = date
		}
		out[id] = logDoc
	}
	return out, vs
}

func decodeSummaryFromSnapshot(snap memstore.BackendSnapshot, owner string) (state.ConversationSummary, bool, error) {
	col := snap.Collections["owners/"+owner+"/conversation"]
	fields, ok := col["summary"]
	if !ok {
		return state.ConversationSummary{}, false, nil
	}
	var sum state.ConversationSummary
	if err := docstore.Decode(fields, &sum); err != nil {
		return state.ConversationSummary{}, true, err
	}
	return sum, true, nil
}

func decodeConversationInputsFromSnapshot(snap memstore.BackendSnapshot, owner string) (map[string]state.ConversationInput, []Violation) {
	col := "owners/" + owner + "/conversation_inputs"
	docs := snap.Collections[col]
	out := make(map[string]state.ConversationInput, len(docs))
	var vs []Violation
	for _, id := range sortedKeys(docs) {
		var in state.ConversationInput
		if err := docstore.Decode(docs[id], &in); err != nil {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-04.decode_input",
				EntityID:  id,
				Message:   fmt.Sprintf("failed to decode conversation_input document: %v", err),
			})
			continue
		}
		out[id] = in
	}
	return out, vs
}

func decodeConversationTurnsFromSnapshot(snap memstore.BackendSnapshot, owner string) (map[string]state.ConversationTurn, []Violation) {
	col := "owners/" + owner + "/conversation_turns"
	docs := snap.Collections[col]
	out := make(map[string]state.ConversationTurn, len(docs))
	var vs []Violation
	for _, id := range sortedKeys(docs) {
		var turn state.ConversationTurn
		if err := docstore.Decode(docs[id], &turn); err != nil {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-01.decode_turn",
				EntityID:  id,
				Message:   fmt.Sprintf("failed to decode conversation_turn document: %v", err),
			})
			continue
		}
		out[id] = turn
	}
	return out, vs
}

func decodeConversationAttemptsFromSnapshot(snap memstore.BackendSnapshot, owner string) (map[string]state.ConversationAttempt, []Violation) {
	col := "owners/" + owner + "/conversation_attempts"
	docs := snap.Collections[col]
	out := make(map[string]state.ConversationAttempt, len(docs))
	var vs []Violation
	for _, id := range sortedKeys(docs) {
		var att state.ConversationAttempt
		if err := docstore.Decode(docs[id], &att); err != nil {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-05.decode_attempt",
				EntityID:  id,
				Message:   fmt.Sprintf("failed to decode conversation_attempt document: %v", err),
			})
			continue
		}
		out[id] = att
	}
	return out, vs
}

// extractManualDemotedIDsFromSnapshot scans mutation_receipts for explicit user
// demote_core operations so INV-MEM-03 can distinguish user-initiated demotion
// of a pinned item from forbidden background auto-demotion.
func extractManualDemotedIDsFromSnapshot(snap memstore.BackendSnapshot, owner string) map[string]bool {
	col := "owners/" + owner + "/mutation_receipts"
	docs := snap.Collections[col]
	if len(docs) == 0 {
		return nil
	}
	demoted := map[string]bool{}
	for _, id := range sortedKeys(docs) {
		raw, _ := docs[id]["resource"].(string)
		if raw == "" {
			continue
		}
		var payload struct {
			Memory *struct {
				ID string `json:"id"`
			} `json:"memory"`
			Demoted  json.RawMessage `json:"demoted"`
			LogEntry *struct {
				ID string `json:"id"`
			} `json:"log_entry"`
		}
		if json.Unmarshal([]byte(raw), &payload) != nil || payload.LogEntry == nil || payload.LogEntry.ID == "" {
			continue
		}
		if payload.Memory != nil && payload.Memory.ID != "" {
			demoted[payload.Memory.ID] = true
		}
		if len(payload.Demoted) > 0 && payload.Demoted[0] == '{' {
			var single struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(payload.Demoted, &single) == nil && single.ID != "" {
				demoted[single.ID] = true
			}
		}
	}
	return demoted
}

// checkMemorySafetyFromSnapshotLocked checks INV-MEM-01 through INV-MEM-07 across
// all users from a single atomic BackendSnapshot.
func (c *Checker) checkMemorySafetyFromSnapshotLocked(
	ctx context.Context,
	dataList []userSnapshotData,
	knownOwners map[string]string,
	snap memstore.BackendSnapshot,
	now time.Time,
) []Violation {
	var vs []Violation

	// INV-MEM-07: Every indexed MemoryChunk must belong to a known DataOwner.
	chunksByOwner := make(map[string]map[string]docstore.MemoryChunk, len(dataList))
	for _, chunk := range snap.MemoryChunks {
		if _, known := knownOwners[chunk.Owner]; !known {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-07.unknown_chunk_owner",
				EntityID:  chunk.Owner + "/" + chunk.ChunkID,
				Message:   fmt.Sprintf("MemoryIndex chunk %q belongs to unknown owner %q", chunk.ChunkID, chunk.Owner),
			})
			continue
		}
		byID, ok := chunksByOwner[chunk.Owner]
		if !ok {
			byID = map[string]docstore.MemoryChunk{}
			chunksByOwner[chunk.Owner] = byID
		}
		byID[chunk.ChunkID] = chunk
	}

	memList := make([]userMemorySnapshot, 0, len(dataList))
	for _, u := range dataList {
		ms := userMemorySnapshot{
			userID:        u.userID,
			owner:         u.owner,
			target:        u.target,
			manualDemoted: extractManualDemotedIDsFromSnapshot(snap, u.owner),
			chunks:        chunksByOwner[u.owner],
			msgs:          u.msgs,
		}
		if ms.chunks == nil {
			ms.chunks = map[string]docstore.MemoryChunk{}
		}

		conv, hasConv, err := decodeConversationFromSnapshot(snap, u.owner)
		if err == nil {
			ms.conv = conv
			ms.hasConv = hasConv
		}

		sum, hasSum, err := decodeSummaryFromSnapshot(snap, u.owner)
		if err != nil {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-01.decode_summary",
				UserID:    u.userID,
				Message:   fmt.Sprintf("failed to decode conversation summary: %v", err),
			})
		} else {
			ms.summary = sum
			ms.hasSummary = hasSum
		}

		var dVs []Violation
		ms.memories, dVs = decodeMemoriesFromSnapshot(snap, u.owner)
		vs = append(vs, tagUser(u.userID, dVs)...)

		ms.logs, dVs = decodeMemoryLogsFromSnapshot(snap, u.owner)
		vs = append(vs, tagUser(u.userID, dVs)...)

		ms.inputs, dVs = decodeConversationInputsFromSnapshot(snap, u.owner)
		vs = append(vs, tagUser(u.userID, dVs)...)

		ms.turns, dVs = decodeConversationTurnsFromSnapshot(snap, u.owner)
		vs = append(vs, tagUser(u.userID, dVs)...)

		ms.attempts, dVs = decodeConversationAttemptsFromSnapshot(snap, u.owner)
		vs = append(vs, tagUser(u.userID, dVs)...)

		// Register authoritative user-input ownership for cross-user isolation (INV-MEM-07).
		for _, inID := range sortedKeys(ms.inputs) {
			in := ms.inputs[inID]
			if in.IsUser() && inID != state.IntroductionInputID() {
				vs = append(vs, c.registerOwnerLocked(c.inputOwners, "conversation_input", inID, u.userID)...)
			}
		}

		memList = append(memList, ms)
	}

	for _, ms := range memList {
		vs = append(vs, c.checkUserMemorySafetyLocked(ctx, ms, now)...)
	}

	return vs
}

func (c *Checker) checkUserMemorySafetyLocked(ctx context.Context, ms userMemorySnapshot, now time.Time) []Violation {
	var vs []Violation
	if c.memoryBaselines == nil {
		c.memoryBaselines = map[string]memoryBaseline{}
	}
	prev := c.memoryBaselines[ms.userID]

	// Collect all log entries across all shards, validating uniqueness per date (INV-MEM-05)
	// and building lookup maps for INV-MEM-02, INV-MEM-03, INV-MEM-04, INV-MEM-06, INV-MEM-07.
	allLogEntriesByID := map[string]state.MemoryLogEntry{}
	evictedEntries := []state.MemoryLogEntry{}
	expectedChunks := map[string]docstore.MemoryChunk{}
	seenDateEntry := map[string]map[string]string{} // date -> entryID -> docID
	var preContextLogEntries []state.MemoryLogEntry

	for _, docID := range sortedKeys(ms.logs) {
		logDoc := ms.logs[docID]
		date := logDoc.Date
		if date == "" {
			date, _, _ = strings.Cut(docID, "#")
		}
		if seenDateEntry[date] == nil {
			seenDateEntry[date] = map[string]string{}
		}
		for _, entry := range logDoc.Entries {
			if firstDoc, dup := seenDateEntry[date][entry.ID]; dup {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-05.duplicate_log_entry",
					UserID:    ms.userID,
					EntityID:  date + "/" + entry.ID,
					Message:   fmt.Sprintf("duplicate Daily Log entry ID %q on %s (docs %q and %q)", entry.ID, date, firstDoc, docID),
				})
			} else {
				seenDateEntry[date][entry.ID] = docID
			}
			allLogEntriesByID[entry.ID] = entry
			if entry.Provenance == state.ProvenanceSummaryEvicted {
				evictedEntries = append(evictedEntries, entry)
			}

			// INV-MEM-04: Log entry provenance validity & non-user input citation check.
			if entry.Provenance != "" && !state.ValidMemoryProvenance(entry.Provenance) {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-04.invalid_provenance",
					UserID:    ms.userID,
					EntityID:  entry.ID,
					Message:   fmt.Sprintf("Daily Log entry has invalid provenance %q", entry.Provenance),
				})
			}
			if entry.SourceInputID != "" && entry.SourceInputID != "ui" {
				if in, exists := ms.inputs[entry.SourceInputID]; exists && !in.IsUser() {
					vs = append(vs, Violation{
						Invariant: "INV-MEM-04.unverified_source_input",
						UserID:    ms.userID,
						EntityID:  entry.ID,
						Message:   fmt.Sprintf("Daily Log entry cites non-user conversation_input %q", entry.SourceInputID),
					})
				}
				// INV-MEM-07: Cross-user reference check on log source_input_id.
				vs = append(vs, c.checkMemoryCrossUserRefLocked(c.inputOwners, "conversation_input", entry.SourceInputID, ms.userID, "memory_log "+docID+"/"+entry.ID)...)
			}
			if entry.RunID != "" {
				// INV-MEM-07: Cross-user reference check on log run_id.
				vs = append(vs, c.checkMemoryCrossUserRefLocked(c.runOwners, "run", entry.RunID, ms.userID, "memory_log "+docID+"/"+entry.ID)...)
			}
			if !ms.conv.ContextFrom.IsZero() && !entry.CreatedAt.IsZero() && entry.CreatedAt.Before(ms.conv.ContextFrom) {
				preContextLogEntries = append(preContextLogEntries, entry)
			}

			chunk := expectedLogChunk(ms.owner, date, entry)
			expectedChunks[chunk.ChunkID] = chunk
		}
	}

	// --- INV-MEM-03 & INV-MEM-04 & INV-MEM-05 (Core Memory) ---
	coreSlice := make([]state.Memory, 0, len(ms.memories))
	currPinned := map[string]state.Memory{}
	seenTurnMem := map[string]string{} // "prov\x00sourceInputID\x00text" -> memID
	for _, id := range sortedKeys(ms.memories) {
		m := ms.memories[id]
		coreSlice = append(coreSlice, m)
		if m.Pinned {
			currPinned[id] = m
		}

		// INV-MEM-03: per-item size, valid UTF-8, CreatedAt <= UpdatedAt.
		if strings.TrimSpace(m.Text) == "" || len(m.Text) < 1 || len(m.Text) > state.MaxMemoryText || !utf8.ValidString(m.Text) {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-03.item_bounds",
				UserID:    ms.userID,
				EntityID:  id,
				Message:   fmt.Sprintf("Core memory text invalid (len=%d, valid_utf8=%v)", len(m.Text), utf8.ValidString(m.Text)),
			})
		}
		if len(m.About) > state.MaxMemoryAbout || !utf8.ValidString(m.About) {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-03.item_bounds",
				UserID:    ms.userID,
				EntityID:  id,
				Message:   fmt.Sprintf("Core memory about invalid (len=%d, valid_utf8=%v)", len(m.About), utf8.ValidString(m.About)),
			})
		}
		if len(m.Section) > state.MaxMemorySection || !utf8.ValidString(m.Section) {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-03.item_bounds",
				UserID:    ms.userID,
				EntityID:  id,
				Message:   fmt.Sprintf("Core memory section invalid (len=%d, valid_utf8=%v)", len(m.Section), utf8.ValidString(m.Section)),
			})
		}
		if !m.CreatedAt.IsZero() && !m.UpdatedAt.IsZero() && m.CreatedAt.After(m.UpdatedAt) {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-03.timestamps",
				UserID:    ms.userID,
				EntityID:  id,
				Message:   fmt.Sprintf("Core memory CreatedAt (%s) is after UpdatedAt (%s)", m.CreatedAt.UTC().Format(time.RFC3339Nano), m.UpdatedAt.UTC().Format(time.RFC3339Nano)),
			})
		}

		// INV-MEM-04: Provenance & injection isolation.
		if m.Provenance == state.ProvenanceTaskOutcome {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-04.task_outcome_in_core",
				UserID:    ms.userID,
				EntityID:  id,
				Message:   "task_outcome item found in Core memories",
			})
		} else if m.Provenance != "" && !state.ValidMemoryProvenance(m.Provenance) {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-04.invalid_provenance",
				UserID:    ms.userID,
				EntityID:  id,
				Message:   fmt.Sprintf("Core memory has invalid provenance %q", m.Provenance),
			})
		} else {
			prov := m.EffectiveProvenance()
			switch prov {
			case state.ProvenancePreCompactFlush:
				in, ok := ms.inputs[m.SourceInputID]
				if m.SourceInputID == "" || m.SourceInputID == "ui" || !ok || !in.IsUser() {
					vs = append(vs, Violation{
						Invariant: "INV-MEM-04.unverified_source_input",
						UserID:    ms.userID,
						EntityID:  id,
						Message:   fmt.Sprintf("pre_compact_flush Core memory cites invalid or non-user SourceInputID %q", m.SourceInputID),
					})
				}
			case state.ProvenanceUserExplicit:
				legacyEmpty := m.Provenance == "" && m.SourceInputID == ""
				if m.SourceInputID != "ui" && !legacyEmpty {
					in, ok := ms.inputs[m.SourceInputID]
					if m.SourceInputID == "" || !ok || !in.IsUser() {
						vs = append(vs, Violation{
							Invariant: "INV-MEM-04.unverified_source_input",
							UserID:    ms.userID,
							EntityID:  id,
							Message:   fmt.Sprintf("user_explicit Core memory cites invalid or non-user SourceInputID %q", m.SourceInputID),
						})
					}
				}
			case state.ProvenanceDreamCurated:
				if m.SourceInputID != "ui" {
					in, ok := ms.inputs[m.SourceInputID]
					if m.SourceInputID == "" || !ok || !in.IsUser() {
						vs = append(vs, Violation{
							Invariant: "INV-MEM-04.unverified_source_input",
							UserID:    ms.userID,
							EntityID:  id,
							Message:   fmt.Sprintf("dream_curated Core memory cites invalid or non-user SourceInputID %q", m.SourceInputID),
						})
					}
				}
			}
		}

		// INV-MEM-05: No duplicate Core memories for the same (Provenance, SourceInputID, Text).
		if m.SourceInputID != "" && m.SourceInputID != "ui" {
			dedupKey := string(m.EffectiveProvenance()) + "\x00" + m.SourceInputID + "\x00" + m.Text
			if firstID, dup := seenTurnMem[dedupKey]; dup {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-05.duplicate_memory",
					UserID:    ms.userID,
					EntityID:  id,
					Message:   fmt.Sprintf("duplicate Core memory %q mirrors %q for source_input_id %q", id, firstID, m.SourceInputID),
				})
			} else {
				seenTurnMem[dedupKey] = id
			}
			// INV-MEM-07: Cross-user reference check on Core SourceInputID.
			vs = append(vs, c.checkMemoryCrossUserRefLocked(c.inputOwners, "conversation_input", m.SourceInputID, ms.userID, "memory "+id)...)
		}

		chunk := expectedCoreChunk(ms.owner, m)
		expectedChunks[chunk.ChunkID] = chunk
	}

	// INV-MEM-03: Total Core memory byte budget <= 24 KiB.
	if totalCore := state.CoreMemorySize(coreSlice); totalCore > state.CoreMemoryBytes {
		vs = append(vs, Violation{
			Invariant: "INV-MEM-03.core_budget",
			UserID:    ms.userID,
			Message:   fmt.Sprintf("Core memory total bytes %d exceeds CoreMemoryBytes (%d)", totalCore, state.CoreMemoryBytes),
		})
	}

	// INV-MEM-03: Previously observed Pinned Core memories must never be auto-demoted
	// by background passes (vanish from Core and appear as demote_<id> without a manual receipt).
	for _, pinnedID := range sortedKeys(prev.pinnedCore) {
		if _, stillCore := ms.memories[pinnedID]; stillCore {
			continue
		}
		if ms.manualDemoted[pinnedID] {
			continue
		}
		if _, wasDemoted := allLogEntriesByID["demote_"+pinnedID]; wasDemoted {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-03.pinned_auto_demoted",
				UserID:    ms.userID,
				EntityID:  pinnedID,
				Message:   fmt.Sprintf("pinned Core memory %q was auto-demoted to Daily Logs without a manual demote receipt", pinnedID),
			})
		}
	}

	// --- INV-MEM-05: Non-completed/superseded/refused attempts leave zero memory mutations ---
	committedNewMemIDs := map[string]bool{}
	allAttempts := make([]state.ConversationAttempt, 0, len(ms.attempts)+len(ms.turns))
	for _, id := range sortedKeys(ms.attempts) {
		allAttempts = append(allAttempts, ms.attempts[id])
	}
	for _, id := range sortedKeys(ms.turns) {
		t := ms.turns[id]
		if t.Attempt.ID != "" {
			if _, exists := ms.attempts[t.Attempt.ID]; !exists {
				allAttempts = append(allAttempts, t.Attempt)
			}
		}
	}
	for _, att := range allAttempts {
		if att.Status != "completed" || hasRefusedMemoryChange(att.Memory) {
			continue
		}
		for _, ch := range att.Memory {
			if ch.Kind == state.MemoryRemember && !ch.Replaces && ch.ID != "" {
				committedNewMemIDs[ch.ID] = true
			}
		}
	}
	for _, att := range allAttempts {
		if len(att.Memory) == 0 {
			continue
		}
		refused := hasRefusedMemoryChange(att.Memory)
		if att.Status == "completed" && !refused {
			continue
		}
		invName := "INV-MEM-05.uncompleted_attempt_applied"
		reason := fmt.Sprintf("non-completed attempt %q (status=%q)", att.ID, att.Status)
		if att.Status == "completed" && refused {
			invName = "INV-MEM-05.refused_change_applied"
			reason = fmt.Sprintf("attempt %q with refused memory change", att.ID)
		}
		for _, ch := range att.Memory {
			if ch.Kind != state.MemoryRemember || ch.Replaces || ch.ID == "" || committedNewMemIDs[ch.ID] {
				continue
			}
			if _, inCore := ms.memories[ch.ID]; inCore {
				vs = append(vs, Violation{
					Invariant: invName,
					UserID:    ms.userID,
					EntityID:  ch.ID,
					Message:   fmt.Sprintf("%s left Core memory %q in store", reason, ch.ID),
				})
			}
			if _, inLog := allLogEntriesByID["demote_"+ch.ID]; inLog {
				vs = append(vs, Violation{
					Invariant: invName,
					UserID:    ms.userID,
					EntityID:  ch.ID,
					Message:   fmt.Sprintf("%s left demoted memory %q in memory_logs", reason, ch.ID),
				})
			}
			if _, inIdx := ms.chunks[docstore.MemoryCoreChunkID(ch.ID)]; inIdx {
				vs = append(vs, Violation{
					Invariant: invName,
					UserID:    ms.userID,
					EntityID:  ch.ID,
					Message:   fmt.Sprintf("%s left chunk %q in MemoryIndex", reason, docstore.MemoryCoreChunkID(ch.ID)),
				})
			}
		}
	}

	// --- INV-MEM-06: Exact 1-to-1 bijection between Store (memories U memory_logs) and MemoryIndex ---
	for _, chunkID := range sortedKeys(expectedChunks) {
		want := expectedChunks[chunkID]
		got, exists := ms.chunks[chunkID]
		if !exists {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-06.fts_missing_chunk",
				UserID:    ms.userID,
				EntityID:  chunkID,
				Message:   fmt.Sprintf("store memory record missing corresponding MemoryIndex chunk %q", chunkID),
			})
			continue
		}
		if !memoryChunksEqual(want, got) {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-06.fts_chunk_mismatch",
				UserID:    ms.userID,
				EntityID:  chunkID,
				Message:   fmt.Sprintf("MemoryIndex chunk %q disagrees with canonical store record", chunkID),
			})
		}
	}
	for _, chunkID := range sortedKeys(ms.chunks) {
		got := ms.chunks[chunkID]
		if _, expected := expectedChunks[chunkID]; !expected {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-06.fts_orphan_chunk",
				UserID:    ms.userID,
				EntityID:  chunkID,
				Message:   fmt.Sprintf("MemoryIndex contains orphan chunk %q with no backing store record", chunkID),
			})
		}
		// INV-MEM-07: Cross-user reference checks on indexed chunks.
		if got.SourceInput != "" && got.SourceInput != "ui" {
			vs = append(vs, c.checkMemoryCrossUserRefLocked(c.inputOwners, "conversation_input", got.SourceInput, ms.userID, "memory_chunk "+chunkID)...)
		}
		if got.RunID != "" {
			vs = append(vs, c.checkMemoryCrossUserRefLocked(c.runOwners, "run", got.RunID, ms.userID, "memory_chunk "+chunkID)...)
		}
	}

	// --- INV-MEM-01, INV-MEM-02, INV-MEM-07 (ConversationSummary & Watermarks) ---
	nextBase := memoryBaseline{
		hadSummary:                prev.hadSummary,
		epoch:                     prev.epoch,
		flushedThrough:            prev.flushedThrough,
		extractedThrough:          prev.extractedThrough,
		through:                   prev.through,
		flushFailures:             prev.flushFailures,
		lastFailureThrough:        prev.lastFailureThrough,
		extractFailures:           prev.extractFailures,
		lastExtractFailureThrough: prev.lastExtractFailureThrough,
		summaryItems:              prev.summaryItems,
		dismissedIDs:              prev.dismissedIDs,
		pinnedCore:                currPinned,
	}

	if ms.hasSummary {
		sum := ms.summary
		flushed := sum.EffectiveFlushedThrough()
		extracted := sum.EffectiveExtractedThrough()
		sameEpoch := prev.hadSummary && prev.epoch.Equal(sum.From)

		// Epoch regression check.
		if prev.hadSummary && !prev.epoch.IsZero() && sum.From.Before(prev.epoch) {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-01.watermark_monotonic",
				UserID:    ms.userID,
				Message:   fmt.Sprintf("summary.From regressed from %s to %s", prev.epoch.UTC().Format(time.RFC3339Nano), sum.From.UTC().Format(time.RFC3339Nano)),
			})
		}

		// Bounds:
		//   From <= EffectiveFlushedThrough <= EffectiveExtractedThrough.
		//   When !Through.IsZero(): EffectiveFlushedThrough <= Through (EffectiveExtractedThrough
		//   may exceed Through when newer verbatim turns were session-idle extracted without compacting).
		//   When Through.IsZero(): FlushedThrough and RefinementThrough must remain zero, while
		//   ExtractedThrough / ExtractFailures / LastDreamAt may be non-zero for an uncompacted
		//   session-idle summary.
		if sum.Through.IsZero() {
			if !sum.FlushedThrough.IsZero() || !sum.RefinementThrough.IsZero() {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-01.watermark_bounds",
					UserID:    ms.userID,
					Message:   fmt.Sprintf("summary.Through is zero while FlushedThrough=%s RefinementThrough=%s", sum.FlushedThrough.UTC().Format(time.RFC3339Nano), sum.RefinementThrough.UTC().Format(time.RFC3339Nano)),
				})
			}
		} else {
			if !sum.From.IsZero() && flushed.Before(sum.From) {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-01.watermark_bounds",
					UserID:    ms.userID,
					Message:   fmt.Sprintf("EffectiveFlushedThrough (%s) precedes From (%s)", flushed.UTC().Format(time.RFC3339Nano), sum.From.UTC().Format(time.RFC3339Nano)),
				})
			}
			if !sum.From.IsZero() && sum.Through.Before(sum.From) {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-01.watermark_bounds",
					UserID:    ms.userID,
					Message:   fmt.Sprintf("Through (%s) precedes From (%s)", sum.Through.UTC().Format(time.RFC3339Nano), sum.From.UTC().Format(time.RFC3339Nano)),
				})
			}
			if flushed.After(sum.Through) {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-01.watermark_bounds",
					UserID:    ms.userID,
					Message:   fmt.Sprintf("EffectiveFlushedThrough (%s) exceeds Through (%s)", flushed.UTC().Format(time.RFC3339Nano), sum.Through.UTC().Format(time.RFC3339Nano)),
				})
			}
			if !sum.FlushedThrough.IsZero() && sum.FlushedThrough.After(sum.Through) {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-01.watermark_bounds",
					UserID:    ms.userID,
					Message:   fmt.Sprintf("FlushedThrough (%s) exceeds Through (%s)", sum.FlushedThrough.UTC().Format(time.RFC3339Nano), sum.Through.UTC().Format(time.RFC3339Nano)),
				})
			}
			if !sum.RefinementThrough.IsZero() && sum.RefinementThrough.After(sum.Through) {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-01.watermark_bounds",
					UserID:    ms.userID,
					Message:   fmt.Sprintf("RefinementThrough (%s) exceeds Through (%s)", sum.RefinementThrough.UTC().Format(time.RFC3339Nano), sum.Through.UTC().Format(time.RFC3339Nano)),
				})
			}
		}

		// ExtractedThrough bounds (valid both when Through.IsZero() and when !Through.IsZero()).
		if !sum.From.IsZero() && extracted.Before(sum.From) {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-01.watermark_bounds",
				UserID:    ms.userID,
				Message:   fmt.Sprintf("EffectiveExtractedThrough (%s) precedes From (%s)", extracted.UTC().Format(time.RFC3339Nano), sum.From.UTC().Format(time.RFC3339Nano)),
			})
		}
		if extracted.Before(flushed) {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-01.watermark_bounds",
				UserID:    ms.userID,
				Message:   fmt.Sprintf("EffectiveExtractedThrough (%s) precedes EffectiveFlushedThrough (%s)", extracted.UTC().Format(time.RFC3339Nano), flushed.UTC().Format(time.RFC3339Nano)),
			})
		}
		if !sum.ExtractedThrough.IsZero() {
			if !sum.From.IsZero() && sum.ExtractedThrough.Before(sum.From) {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-01.watermark_bounds",
					UserID:    ms.userID,
					Message:   fmt.Sprintf("ExtractedThrough (%s) precedes From (%s)", sum.ExtractedThrough.UTC().Format(time.RFC3339Nano), sum.From.UTC().Format(time.RFC3339Nano)),
				})
			} else if sum.ExtractedThrough.Before(flushed) {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-01.watermark_bounds",
					UserID:    ms.userID,
					Message:   fmt.Sprintf("ExtractedThrough (%s) precedes EffectiveFlushedThrough (%s)", sum.ExtractedThrough.UTC().Format(time.RFC3339Nano), flushed.UTC().Format(time.RFC3339Nano)),
				})
			} else if !ms.conv.ContextFrom.IsZero() && sum.From.Equal(ms.conv.ContextFrom) && sum.ExtractedThrough.Before(ms.conv.ContextFrom) {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-01.watermark_bounds",
					UserID:    ms.userID,
					Message:   fmt.Sprintf("ExtractedThrough (%s) precedes ContextFrom (%s)", sum.ExtractedThrough.UTC().Format(time.RFC3339Nano), ms.conv.ContextFrom.UTC().Format(time.RFC3339Nano)),
				})
			}
		}

		// Monotonicity per ContextFrom epoch & no advance on flush or extraction failure.
		if sameEpoch {
			if flushed.Before(prev.flushedThrough) {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-01.watermark_monotonic",
					UserID:    ms.userID,
					Message:   fmt.Sprintf("EffectiveFlushedThrough regressed from %s to %s", prev.flushedThrough.UTC().Format(time.RFC3339Nano), flushed.UTC().Format(time.RFC3339Nano)),
				})
			}
			if extracted.Before(prev.extractedThrough) {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-01.watermark_monotonic",
					UserID:    ms.userID,
					Message:   fmt.Sprintf("EffectiveExtractedThrough regressed from %s to %s", prev.extractedThrough.UTC().Format(time.RFC3339Nano), extracted.UTC().Format(time.RFC3339Nano)),
				})
			}
			if !prev.through.IsZero() && sum.Through.Before(prev.through) {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-01.watermark_monotonic",
					UserID:    ms.userID,
					Message:   fmt.Sprintf("Through regressed from %s to %s", prev.through.UTC().Format(time.RFC3339Nano), sum.Through.UTC().Format(time.RFC3339Nano)),
				})
			}
			if sum.FlushFailures > prev.flushFailures && flushed.After(prev.flushedThrough) {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-01.failure_watermark_advance",
					UserID:    ms.userID,
					Message:   fmt.Sprintf("FlushedThrough advanced from %s to %s while FlushFailures increased from %d to %d", prev.flushedThrough.UTC().Format(time.RFC3339Nano), flushed.UTC().Format(time.RFC3339Nano), prev.flushFailures, sum.FlushFailures),
				})
			}
			if sum.ExtractFailures > prev.extractFailures &&
				sum.LastExtractFailureThrough.Equal(prev.lastExtractFailureThrough) &&
				extracted.After(prev.extractedThrough) &&
				(!flushed.After(prev.flushedThrough) || !extracted.Equal(flushed)) {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-01.failure_watermark_advance",
					UserID:    ms.userID,
					Message:   fmt.Sprintf("EffectiveExtractedThrough advanced from %s to %s while ExtractFailures increased from %d to %d", prev.extractedThrough.UTC().Format(time.RFC3339Nano), extracted.UTC().Format(time.RFC3339Nano), prev.extractFailures, sum.ExtractFailures),
				})
			}
		}
		if sum.FlushFailures > 0 && !sum.LastFailureThrough.IsZero() && !flushed.Before(sum.LastFailureThrough) {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-01.failure_watermark_advance",
				UserID:    ms.userID,
				Message:   fmt.Sprintf("FlushedThrough (%s) reached or passed LastFailureThrough (%s) while FlushFailures=%d > 0", flushed.UTC().Format(time.RFC3339Nano), sum.LastFailureThrough.UTC().Format(time.RFC3339Nano), sum.FlushFailures),
			})
		}
		if sum.ExtractFailures > 0 && !sum.LastExtractFailureThrough.IsZero() && !extracted.Before(sum.LastExtractFailureThrough) {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-01.failure_watermark_advance",
				UserID:    ms.userID,
				Message:   fmt.Sprintf("EffectiveExtractedThrough (%s) reached or passed LastExtractFailureThrough (%s) while ExtractFailures=%d > 0", extracted.UTC().Format(time.RFC3339Nano), sum.LastExtractFailureThrough.UTC().Format(time.RFC3339Nano), sum.ExtractFailures),
			})
		}

		// Excerpt budget & coverage for every completed turn in (EffectiveFlushedThrough, Through].
		excerptsBytes := 0
		if len(sum.Excerpts) > 0 {
			rawEx, _ := json.Marshal(sum.Excerpts)
			excerptsBytes = len(rawEx)
			if excerptsBytes > maxConversationExcerptBytes || excerptsBytes > state.OuterInputBytes {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-01.excerpt_budget",
					UserID:    ms.userID,
					Message:   fmt.Sprintf("summary.Excerpts size %d exceeds cap (%d)", excerptsBytes, maxConversationExcerptBytes),
				})
			}
		}
		if !sum.Through.IsZero() && flushed.Before(sum.Through) {
			vs = append(vs, checkUnflushedExcerptCoverage(ms.userID, sum, flushed, excerptsBytes, ms.turns)...)
		}

		// INV-MEM-02: Summary budget, dismissed items, and no silent eviction.
		if len(sum.Items) > state.MaxSummaryItems {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-02.summary_budget",
				UserID:    ms.userID,
				Message:   fmt.Sprintf("summary.Items count %d exceeds MaxSummaryItems (%d)", len(sum.Items), state.MaxSummaryItems),
			})
		}
		sumPayload, _ := json.Marshal(struct {
			Items []state.SummaryItem `json:"items"`
			Gist  string              `json:"gist"`
		}{sum.Items, sum.Gist})
		if len(sumPayload) > state.SummaryBytes {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-02.summary_budget",
				UserID:    ms.userID,
				Message:   fmt.Sprintf("summary {items,gist} size %d exceeds SummaryBytes (%d)", len(sumPayload), state.SummaryBytes),
			})
		}

		dismissedSet := make(map[string]bool, len(sum.DismissedItemIDs))
		for _, id := range sum.DismissedItemIDs {
			dismissedSet[id] = true
		}
		currItems := make(map[string]state.SummaryItem, len(sum.Items))
		for _, item := range sum.Items {
			itemID := state.SummaryItemID(item)
			currItems[itemID] = item
			if dismissedSet[itemID] {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-02.dismissed_reappeared",
					UserID:    ms.userID,
					EntityID:  itemID,
					Message:   fmt.Sprintf("dismissed summary item %q reappeared in summary.Items", itemID),
				})
			}
			// INV-MEM-07: Cross-user reference & pre-ContextFrom check on SummaryItem.
			if item.InputID != "" {
				vs = append(vs, c.checkMemoryCrossUserRefLocked(c.inputOwners, "conversation_input", item.InputID, ms.userID, "summary_item "+itemID)...)
				if !ms.conv.ContextFrom.IsZero() && sum.From.Equal(ms.conv.ContextFrom) {
					if in, ok := ms.inputs[item.InputID]; ok && !in.CreatedAt.IsZero() && in.CreatedAt.Before(ms.conv.ContextFrom) {
						vs = append(vs, Violation{
							Invariant: "INV-MEM-07.pre_context_from_leak",
							UserID:    ms.userID,
							EntityID:  itemID,
							Message:   fmt.Sprintf("SummaryItem %q references pre-ContextFrom input %q (%s < %s)", itemID, item.InputID, in.CreatedAt.UTC().Format(time.RFC3339Nano), ms.conv.ContextFrom.UTC().Format(time.RFC3339Nano)),
						})
					}
				}
			}
		}

		if !ms.conv.ContextFrom.IsZero() && sum.From.Equal(ms.conv.ContextFrom) {
			for _, ex := range sum.Excerpts {
				if !ex.TurnStartedAt.IsZero() && ex.TurnStartedAt.Before(ms.conv.ContextFrom) {
					vs = append(vs, Violation{
						Invariant: "INV-MEM-07.pre_context_from_leak",
						UserID:    ms.userID,
						EntityID:  ex.MessageID,
						Message:   fmt.Sprintf("summary excerpt %q has TurnStartedAt %s before ContextFrom %s", ex.MessageID, ex.TurnStartedAt.UTC().Format(time.RFC3339Nano), ms.conv.ContextFrom.UTC().Format(time.RFC3339Nano)),
					})
				}
			}
		}

		if sameEpoch {
			for _, prevDismissed := range sortedKeys(prev.dismissedIDs) {
				if !dismissedSet[prevDismissed] {
					vs = append(vs, Violation{
						Invariant: "INV-MEM-02.dismissed_reappeared",
						UserID:    ms.userID,
						EntityID:  prevDismissed,
						Message:   fmt.Sprintf("previously dismissed summary item ID %q was dropped from DismissedItemIDs", prevDismissed),
					})
				}
			}
			semanticallySuperseded := flushed.After(prev.flushedThrough) && sum.Model != "" && sum.Model != "extractive"
			if !semanticallySuperseded {
				for _, prevID := range sortedKeys(prev.summaryItems) {
					if _, stillPresent := currItems[prevID]; stillPresent {
						continue
					}
					if dismissedSet[prevID] {
						continue
					}
					prevItem := prev.summaryItems[prevID]
					if !isPreservedAsEvictedLog(prevID, prevItem, evictedEntries) {
						vs = append(vs, Violation{
							Invariant: "INV-MEM-02.silent_eviction",
							UserID:    ms.userID,
							EntityID:  prevID,
							Message:   fmt.Sprintf("summary item %q disappeared without dismissal, semantic batch supersession, or summary_evicted Daily Log entry", prevID),
						})
					}
				}
			}
		}

		if len(vs) == 0 {
			nextBase.hadSummary = true
			nextBase.epoch = sum.From
			nextBase.flushedThrough = flushed
			nextBase.extractedThrough = extracted
			nextBase.through = sum.Through
			nextBase.flushFailures = sum.FlushFailures
			nextBase.lastFailureThrough = sum.LastFailureThrough
			nextBase.extractFailures = sum.ExtractFailures
			nextBase.lastExtractFailureThrough = sum.LastExtractFailureThrough
			nextBase.summaryItems = currItems
			nextBase.dismissedIDs = dismissedSet
		}
	}

	// INV-MEM-07: Active ContextFrom read isolation check (fault-free, zero RNG draws).
	if !ms.conv.ContextFrom.IsZero() && ms.target.Store != nil {
		vs = append(vs, c.checkContextFromReadIsolationLocked(ctx, ms, preContextLogEntries, now)...)
	}

	c.memoryBaselines[ms.userID] = nextBase
	return vs
}

func (c *Checker) checkContextFromReadIsolationLocked(
	ctx context.Context,
	ms userMemorySnapshot,
	preContextLogEntries []state.MemoryLogEntry,
	now time.Time,
) []Violation {
	var vs []Violation
	_ = c.withoutFaults(func() error {
		// 1. Verify SearchMemory with CreatedAfter=ContextFrom returns zero pre-ContextFrom items or cross-owner chunks.
		if len(preContextLogEntries) > 0 {
			qToken := firstSearchableToken(preContextLogEntries[0].Text)
			if qToken != "" {
				hits, err := ms.target.Store.SearchMemory(ctx, docstore.MemorySearchQuery{
					Query:        qToken,
					Layer:        docstore.MemoryLayerLog,
					CreatedAfter: ms.conv.ContextFrom,
					Limit:        docstore.MaxMemorySearchLimit,
					Now:          now,
				})
				if err == nil {
					for _, hit := range hits {
						if hit.Chunk.Owner != ms.owner {
							vs = append(vs, Violation{
								Invariant: "INV-MEM-07.cross_user_reference",
								UserID:    ms.userID,
								EntityID:  hit.Chunk.ChunkID,
								Message:   fmt.Sprintf("SearchMemory returned chunk owned by %q for owner %q", hit.Chunk.Owner, ms.owner),
							})
						}
						if !hit.Chunk.CreatedAt.IsZero() && hit.Chunk.CreatedAt.Before(ms.conv.ContextFrom) {
							vs = append(vs, Violation{
								Invariant: "INV-MEM-07.pre_context_from_leak",
								UserID:    ms.userID,
								EntityID:  hit.Chunk.ChunkID,
								Message:   fmt.Sprintf("SearchMemory returned pre-ContextFrom log chunk %q (%s < %s)", hit.Chunk.ChunkID, hit.Chunk.CreatedAt.UTC().Format(time.RFC3339Nano), ms.conv.ContextFrom.UTC().Format(time.RFC3339Nano)),
							})
						}
					}
				}
			}
		}

		// 2. Verify ReadConversationMessage rejects pre-ContextFrom messages.
		for _, m := range ms.msgs {
			if m.Role == "user" && !m.CreatedAt.IsZero() && m.CreatedAt.Before(ms.conv.ContextFrom) {
				if _, err := ms.target.Store.ReadConversationMessage(ctx, m.ID); !errors.Is(err, state.ErrNotFound) {
					vs = append(vs, Violation{
						Invariant: "INV-MEM-07.pre_context_from_leak",
						UserID:    ms.userID,
						EntityID:  m.ID,
						Message:   fmt.Sprintf("ReadConversationMessage returned pre-ContextFrom message %q (err=%v)", m.ID, err),
					})
				}
				break
			}
		}
		return nil
	})
	return vs
}

func (c *Checker) checkMemoryCrossUserRefLocked(owners map[string]string, kind, entityID, userID, source string) []Violation {
	if entityID == "" {
		return nil
	}
	if owner, exists := owners[entityID]; exists && owner != userID {
		return []Violation{{
			Invariant: "INV-MEM-07.cross_user_reference",
			UserID:    userID,
			EntityID:  entityID,
			Message:   fmt.Sprintf("%s references %s ID %q owned by user %q", source, kind, entityID, owner),
		}}
	}
	return nil
}

// checkMemoryQuiescenceLocked implements INV-MEM-08 inside CheckQuiescence.
func (c *Checker) checkMemoryQuiescenceLocked(ctx context.Context, sorted []UserTarget, now time.Time) []Violation {
	var vs []Violation

	var snap memstore.BackendSnapshot
	hasSnap := false
	convWakes := map[string]docstore.UserWake{}
	if c.backend != nil {
		snap = c.backend.Snapshot()
		hasSnap = true
		for _, w := range snap.UserWakes {
			if w.Lane == docstore.LaneConversation {
				convWakes[w.UserID] = w
			}
		}
	}

	for _, t := range sorted {
		if t.Store == nil {
			continue
		}
		userID := t.User.ID
		conv, _, err := t.Store.ConversationStatus(ctx)
		if err != nil {
			continue
		}
		summary, err := t.Store.ConversationSummary(ctx)
		if err != nil {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-08.summary_read",
				UserID:    userID,
				Message:   fmt.Sprintf("ConversationSummary failed at quiescence: %v", err),
			})
			continue
		}

		// Wake lease hygiene check on LaneConversation when leases are tracked.
		w, hasWake := convWakes[userID]
		if hasSnap && c.checkLeases && hasWake {
			if w.Owner != "" || w.LeaseUntil != nil {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-08.wake_lease_hygiene",
					UserID:    userID,
					EntityID:  docstore.LaneConversation,
					Message:   fmt.Sprintf("conversation wake retains lease at quiescence (owner=%q lease_until_set=%v)", w.Owner, w.LeaseUntil != nil),
				})
			}
			if w.Runnable && !w.WakeAt.After(now) {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-08.wake_lease_hygiene",
					UserID:    userID,
					EntityID:  docstore.LaneConversation,
					Message:   fmt.Sprintf("conversation wake remains runnable with wake_at=%s <= now=%s at quiescence", w.WakeAt.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)),
				})
			}
		}

		if conv.Pending() || conv.Active != "" {
			continue
		}

		owner := t.User.DataOwner()
		summaryExists := false
		if hasSnap {
			_, summaryExists, _ = decodeSummaryFromSnapshot(snap, owner)
		} else {
			summaryExists = !summary.From.IsZero() ||
				!summary.Through.IsZero() ||
				!summary.FlushedThrough.IsZero() ||
				!summary.RefinementThrough.IsZero() ||
				!summary.ExtractedThrough.IsZero() ||
				summary.FlushFailures > 0 ||
				summary.ExtractFailures > 0 ||
				!summary.LastDreamAt.IsZero() ||
				!summary.UpdatedAt.IsZero()
		}

		hasFutureWake := hasWake && w.Runnable && w.WakeAt.After(now)
		summaryInEpoch := summaryExists && summary.From.Equal(conv.ContextFrom)
		inFlushBackoff := summaryInEpoch && summary.FlushFailures > 0 && !summary.UpdatedAt.IsZero() && now.Before(summary.UpdatedAt.Add(10*time.Minute))
		inExtractBackoff := summaryInEpoch && (summary.ExtractFailures > 0 || summary.FlushFailures > 0) && !summary.UpdatedAt.IsZero() && now.Before(summary.UpdatedAt.Add(10*time.Minute))

		if summary.Applies(conv.ContextFrom) && !summary.Through.IsZero() {
			flushed := summary.EffectiveFlushedThrough()
			if flushed.Before(summary.Through) {
				if !hasFutureWake && !inFlushBackoff {
					vs = append(vs, Violation{
						Invariant: "INV-MEM-08.unflushed_watermark",
						UserID:    userID,
						Message:   fmt.Sprintf("post-drain quiescence has FlushedThrough (%s) < Through (%s) with no scheduled future wake or active failure backoff", flushed.UTC().Format(time.RFC3339Nano), summary.Through.UTC().Format(time.RFC3339Nano)),
					})
				}
			} else if flushed.Equal(summary.Through) && len(summary.Excerpts) > 0 {
				vs = append(vs, Violation{
					Invariant: "INV-MEM-08.stale_excerpts",
					UserID:    userID,
					Message:   fmt.Sprintf("FlushedThrough == Through (%s) at quiescence but %d retired excerpt(s) remain", flushed.UTC().Format(time.RFC3339Nano), len(summary.Excerpts)),
				})
			}
		}

		// Session-idle extraction liveness: when the summary/memory pipeline is active
		// for this user and completed turns in the current ContextFrom epoch remain
		// unextracted at or after IdleExtractDelay (+20m) with no scheduled future
		// LaneConversation wake or active retry backoff, flag INV-MEM-08.unextracted_after_idle.
		if summaryInEpoch || (summaryExists && !conv.ContextFrom.IsZero() && summary.From.Before(conv.ContextFrom)) {
			effectiveExtracted := conv.ContextFrom
			if summaryInEpoch {
				if ex := summary.EffectiveExtractedThrough(); ex.After(effectiveExtracted) {
					effectiveExtracted = ex
				}
			}
			latestCompletedAt, hasUnextracted := latestUnextractedCompletedTurn(ctx, t.Store, snap, hasSnap, owner, conv.ContextFrom, effectiveExtracted)
			if hasUnextracted {
				idleAnchor := latestCompletedAt
				if conv.LatestAt.After(idleAnchor) {
					idleAnchor = conv.LatestAt
				}
				if !idleAnchor.IsZero() && !now.Before(idleAnchor.Add(state.IdleExtractDelay)) && !hasFutureWake && !inExtractBackoff {
					vs = append(vs, Violation{
						Invariant: "INV-MEM-08.unextracted_after_idle",
						UserID:    userID,
						Message:   fmt.Sprintf("post-drain quiescence has unextracted completed turn(s) up to %s > EffectiveExtractedThrough (%s) after IdleExtractDelay (%s) with no scheduled future wake or active backoff", latestCompletedAt.UTC().Format(time.RFC3339Nano), effectiveExtracted.UTC().Format(time.RFC3339Nano), state.IdleExtractDelay),
					})
				}
			}
		}
	}

	return vs
}

func latestUnextractedCompletedTurn(
	ctx context.Context,
	store state.ConversationStore,
	snap memstore.BackendSnapshot,
	hasSnap bool,
	owner string,
	contextFrom time.Time,
	effectiveExtracted time.Time,
) (latestCompletedAt time.Time, hasUnextracted bool) {
	if hasSnap {
		turns, _ := decodeConversationTurnsFromSnapshot(snap, owner)
		for _, id := range sortedKeys(turns) {
			turn := turns[id]
			if turn.Status != "completed" || turn.StartedAt.IsZero() {
				continue
			}
			if !contextFrom.IsZero() && turn.StartedAt.Before(contextFrom) {
				continue
			}
			if turn.StartedAt.After(latestCompletedAt) {
				latestCompletedAt = turn.StartedAt
			}
			if turn.StartedAt.After(effectiveExtracted) {
				hasUnextracted = true
			}
		}
		return latestCompletedAt, hasUnextracted
	}
	turns, err := store.ConversationTurnsAfter(ctx, effectiveExtracted)
	if err != nil {
		return time.Time{}, false
	}
	for _, turn := range turns {
		if turn.Status != "completed" || turn.StartedAt.IsZero() {
			continue
		}
		if !contextFrom.IsZero() && turn.StartedAt.Before(contextFrom) {
			continue
		}
		if turn.StartedAt.After(latestCompletedAt) {
			latestCompletedAt = turn.StartedAt
		}
		if turn.StartedAt.After(effectiveExtracted) {
			hasUnextracted = true
		}
	}
	return latestCompletedAt, hasUnextracted
}

func checkUnflushedExcerptCoverage(
	userID string,
	sum state.ConversationSummary,
	flushed time.Time,
	excerptsBytes int,
	turns map[string]state.ConversationTurn,
) []Violation {
	var unflushed []state.ConversationTurn
	for _, id := range sortedKeys(turns) {
		t := turns[id]
		if t.Status != "completed" {
			continue
		}
		if !sum.From.IsZero() && t.StartedAt.Before(sum.From) {
			continue
		}
		if !t.StartedAt.After(flushed) || t.StartedAt.After(sum.Through) {
			continue
		}
		if turnHasExcerptableContent(t) {
			unflushed = append(unflushed, t)
		}
	}
	if len(unflushed) == 0 {
		return nil
	}
	slices.SortFunc(unflushed, func(a, b state.ConversationTurn) int {
		if a.StartedAt.Before(b.StartedAt) {
			return -1
		}
		if a.StartedAt.After(b.StartedAt) {
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})

	if len(sum.Excerpts) == 0 {
		return []Violation{{
			Invariant: "INV-MEM-01.excerpt_coverage",
			UserID:    userID,
			EntityID:  unflushed[0].ID,
			Message:   fmt.Sprintf("%d completed turn(s) exist in (FlushedThrough=%s, Through=%s] but Summary.Excerpts is empty", len(unflushed), flushed.UTC().Format(time.RFC3339Nano), sum.Through.UTC().Format(time.RFC3339Nano)),
		}}
	}

	excerptMsgIDs := make(map[string]bool, len(sum.Excerpts))
	var earliestKept time.Time
	for _, ex := range sum.Excerpts {
		if ex.MessageID != "" {
			excerptMsgIDs[ex.MessageID] = true
		}
		if !ex.TurnStartedAt.IsZero() && (earliestKept.IsZero() || ex.TurnStartedAt.Before(earliestKept)) {
			earliestKept = ex.TurnStartedAt
		}
	}

	var vs []Violation
	for i := len(unflushed) - 1; i >= 0; i-- {
		t := unflushed[i]
		if turnRepresentedInExcerpts(t, sum.Excerpts, excerptMsgIDs) {
			continue
		}
		olderThanEarliest := !earliestKept.IsZero() && t.StartedAt.Before(earliestKept)
		estBytes := estimateTurnExcerptBytes(t)
		if !olderThanEarliest || excerptsBytes+estBytes <= maxConversationExcerptBytes {
			vs = append(vs, Violation{
				Invariant: "INV-MEM-01.excerpt_coverage",
				UserID:    userID,
				EntityID:  t.ID,
				Message:   fmt.Sprintf("completed turn %q (started_at=%s) in (FlushedThrough, Through] is missing from Summary.Excerpts (excerpts_bytes=%d)", t.ID, t.StartedAt.UTC().Format(time.RFC3339Nano), excerptsBytes),
			})
		}
	}
	return vs
}

func turnHasExcerptableContent(t state.ConversationTurn) bool {
	for _, in := range t.Inputs {
		if in.IsUser() && strings.TrimSpace(in.Text) != "" {
			return true
		}
	}
	return publishedTurnReply(t) != ""
}

func turnRepresentedInExcerpts(t state.ConversationTurn, excerpts []state.ConversationExcerpt, msgIDs map[string]bool) bool {
	if msgIDs[state.AssistantMessageID(t.ID)] {
		return true
	}
	for _, in := range t.Inputs {
		if in.IsUser() && msgIDs[state.UserMessageID(in.ID)] {
			return true
		}
	}
	for _, ex := range excerpts {
		if !ex.TurnStartedAt.IsZero() && ex.TurnStartedAt.Equal(t.StartedAt) {
			return true
		}
	}
	return false
}

func estimateTurnExcerptBytes(t state.ConversationTurn) int {
	var sample []state.ConversationExcerpt
	for _, in := range t.Inputs {
		if in.IsUser() && strings.TrimSpace(in.Text) != "" {
			txt := in.Text
			if len(txt) > state.MaxSummaryQuote {
				txt = docstore.ClipUTF8(txt, state.MaxSummaryQuote)
			}
			sample = append(sample, state.ConversationExcerpt{
				MessageID:     state.UserMessageID(in.ID),
				Role:          "user",
				Text:          txt,
				TurnStartedAt: t.StartedAt,
			})
		}
	}
	if reply := publishedTurnReply(t); reply != "" {
		if len(reply) > state.MaxSummaryQuote {
			reply = docstore.ClipUTF8(reply, state.MaxSummaryQuote)
		}
		sample = append(sample, state.ConversationExcerpt{
			MessageID:     state.AssistantMessageID(t.ID),
			Role:          "assistant",
			Text:          reply,
			TurnStartedAt: t.StartedAt,
		})
	}
	raw, _ := json.Marshal(sample)
	return len(raw)
}

func publishedTurnReply(t state.ConversationTurn) string {
	for i := len(t.Protocol) - 1; i >= 0; i-- {
		var m struct {
			Role    string  `json:"role"`
			Content *string `json:"content"`
		}
		if json.Unmarshal([]byte(t.Protocol[i]), &m) != nil || m.Role != "assistant" || m.Content == nil {
			continue
		}
		var structured struct {
			Text *string `json:"text"`
		}
		if json.Unmarshal([]byte(*m.Content), &structured) == nil && structured.Text != nil {
			return *structured.Text
		}
		return *m.Content
	}
	return ""
}

func isPreservedAsEvictedLog(itemID string, item state.SummaryItem, evicted []state.MemoryLogEntry) bool {
	wantID := "evict_" + itemID
	for _, e := range evicted {
		if e.ID == wantID {
			return true
		}
		if item.InputID != "" && e.SourceInputID == item.InputID && item.Quote != "" && strings.Contains(e.Text, item.Quote) {
			return true
		}
	}
	return false
}

func hasRefusedMemoryChange(changes []state.MemoryChange) bool {
	for _, ch := range changes {
		if ch.Error != "" {
			return true
		}
	}
	return false
}

func expectedCoreChunk(owner string, m state.Memory) docstore.MemoryChunk {
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

func expectedLogChunk(owner, date string, e state.MemoryLogEntry) docstore.MemoryChunk {
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

func memoryChunksEqual(a, b docstore.MemoryChunk) bool {
	return a.Owner == b.Owner &&
		a.ChunkID == b.ChunkID &&
		a.Layer == b.Layer &&
		a.LogDate == b.LogDate &&
		a.Section == b.Section &&
		a.About == b.About &&
		a.Body == b.Body &&
		a.Provenance == b.Provenance &&
		a.SourceInput == b.SourceInput &&
		a.RunID == b.RunID &&
		a.Pinned == b.Pinned &&
		a.CreatedAt.Equal(b.CreatedAt) &&
		a.UpdatedAt.Equal(b.UpdatedAt)
}

func firstSearchableToken(text string) string {
	for _, field := range strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-'
	}) {
		if len(field) >= 3 {
			return field
		}
	}
	return strings.TrimSpace(text)
}
