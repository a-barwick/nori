package conversation

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

func verifyFlushCoreUpserts(
	candidates []flushCoreUpsertCandidate,
	batchUsers map[string]string,
	batchUserTimes map[string]time.Time,
	existingCore []state.Memory,
	nowUTC time.Time,
) []state.Memory {
	existingByID := make(map[string]state.Memory, len(existingCore))
	for _, m := range existingCore {
		existingByID[m.ID] = m
	}
	byID := map[string]int{}
	out := make([]state.Memory, 0, len(candidates))
	for _, c := range candidates {
		text := strings.TrimSpace(c.Text)
		about := strings.TrimSpace(c.About)
		section := strings.TrimSpace(c.Section)
		inputID := strings.TrimSpace(c.InputID)
		quote := strings.TrimSpace(c.Quote)
		replaces := strings.TrimSpace(c.Replaces)

		sourceText, ok := batchUsers[inputID]
		if !ok || strings.TrimSpace(sourceText) == "" {
			continue
		}
		if quote != "" && !strings.Contains(sourceText, quote) {
			continue
		}
		if text == "" || len(text) > state.MaxMemoryText || !utf8.ValidString(text) {
			continue
		}
		if !utf8.ValidString(about) || !utf8.ValidString(section) {
			continue
		}
		if about == "" {
			about = "the user"
		}
		about = docstore.ClipUTF8(about, state.MaxMemoryAbout)
		section = docstore.ClipUTF8(section, state.MaxMemorySection)

		m := state.Memory{
			Text:          text,
			About:         about,
			Section:       section,
			Provenance:    state.ProvenancePreCompactFlush,
			SourceInputID: inputID,
			SourceText:    state.BoundedSource(sourceText),
			CreatedAt:     nowUTC,
			UpdatedAt:     nowUTC,
		}
		if replaces != "" {
			if existing, exists := existingByID[replaces]; exists {
				if existing.Pinned && !batchUserTimes[inputID].After(existing.UpdatedAt) {
					continue
				}
				m.ID = existing.ID
				m.CreatedAt = existing.CreatedAt
				if m.CreatedAt.After(m.UpdatedAt) {
					m.UpdatedAt = m.CreatedAt
				}
				m.Pinned = existing.Pinned
				m.SupersedesID = existing.ID
				if m.Section == "" {
					m.Section = existing.Section
				}
			}
		}
		if m.ID == "" {
			m.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("flush-core:"+inputID+":"+text)).String()
		}
		if idx, dup := byID[m.ID]; dup {
			out[idx] = m
		} else {
			byID[m.ID] = len(out)
			out = append(out, m)
		}
	}
	return out
}

func verifyFlushLogEntries(
	candidates []flushLogEntryCandidate,
	batchUsers map[string]string,
	batchRuns map[string]bool,
	nowUTC time.Time,
) []state.MemoryLogEntry {
	seen := map[string]bool{}
	out := make([]state.MemoryLogEntry, 0, len(candidates))
	for _, c := range candidates {
		text := strings.TrimSpace(c.Text)
		about := strings.TrimSpace(c.About)
		section := strings.TrimSpace(c.Section)
		inputID := strings.TrimSpace(c.InputID)
		runID := strings.TrimSpace(c.RunID)
		quote := strings.TrimSpace(c.Quote)

		if text == "" || len(text) > state.MaxMemoryText || !utf8.ValidString(text) {
			continue
		}
		if !utf8.ValidString(about) || !utf8.ValidString(section) {
			continue
		}
		if about == "" {
			about = "the user"
		}
		about = docstore.ClipUTF8(about, state.MaxMemoryAbout)
		section = docstore.ClipUTF8(section, state.MaxMemorySection)

		entry := state.MemoryLogEntry{
			Text:      text,
			About:     about,
			Section:   section,
			CreatedAt: nowUTC,
			UpdatedAt: nowUTC,
		}
		if sourceText, ok := batchUsers[inputID]; ok && strings.TrimSpace(sourceText) != "" {
			if quote != "" && !strings.Contains(sourceText, quote) {
				continue
			}
			entry.Provenance = state.ProvenancePreCompactFlush
			entry.SourceInputID = inputID
			entry.SourceText = state.BoundedSource(sourceText)
			if entry.Section == "" {
				entry.Section = "Context"
			}
		} else if runID != "" && batchRuns[runID] {
			entry.Provenance = state.ProvenanceTaskOutcome
			entry.RunID = runID
			if entry.Section == "" || entry.Section == "Context" {
				entry.Section = "Completed Work"
			}
		} else {
			continue
		}

		entry.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("flush-log:"+entry.SourceInputID+":"+entry.RunID+":"+entry.Section+":"+entry.Text)).String()
		if seen[entry.ID] {
			continue
		}
		seen[entry.ID] = true
		out = append(out, entry)
	}
	return out
}

func extractiveFallbackEntries(batch []state.ConversationTurn, nowUTC time.Time) []state.MemoryLogEntry {
	out := []state.MemoryLogEntry{}
	seen := map[string]bool{}
	for _, t := range batch {
		for _, in := range t.Inputs {
			if !in.IsUser() {
				continue
			}
			text := strings.TrimSpace(in.Text)
			if text == "" || !utf8.ValidString(text) {
				continue
			}
			chunks := splitUTF8Chunks(text, state.MaxMemoryText)
			for idx, chunk := range chunks {
				id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("fallback-log:%s:%d", in.ID, idx))).String()
				if seen[id] {
					continue
				}
				seen[id] = true
				out = append(out, state.MemoryLogEntry{
					ID:            id,
					Section:       "Context",
					Text:          chunk,
					About:         "the user",
					Provenance:    state.ProvenancePreCompactFlush,
					SourceInputID: in.ID,
					SourceText:    state.BoundedSource(text),
					CreatedAt:     nowUTC,
					UpdatedAt:     nowUTC,
				})
			}
		}
	}
	return out
}

func splitUTF8Chunks(s string, maxBytes int) []string {
	if maxBytes <= 0 || len(s) == 0 {
		return nil
	}
	var chunks []string
	for len(s) > 0 {
		part := docstore.ClipUTF8(s, maxBytes)
		if len(part) == 0 {
			break
		}
		chunks = append(chunks, part)
		s = s[len(part):]
	}
	return chunks
}

func compactionTurn(t state.ConversationTurn) (map[string]any, map[string]string, map[string]bool) {
	users := map[string]string{}
	runs := map[string]bool{}
	messages := []map[string]string{}
	for _, in := range t.Inputs {
		switch {
		case in.IsUser():
			users[in.ID] = in.Text
			messages = append(messages, map[string]string{"from": "user", "input_id": in.ID, "text": in.Text})
		case in.Event != nil:
			if in.Event.RunID != "" {
				runs[in.Event.RunID] = true
			}
			text := in.Event.Text
			if len(text) > 600 {
				text = docstore.ClipUTF8(text, 600)
			}
			messages = append(messages, map[string]string{"from": "task " + in.Event.RunID, "run_id": in.Event.RunID, "event": in.Event.Kind, "text": strings.ToValidUTF8(text, "")})
		}
	}
	if reply := publishedReply(t); reply != "" {
		messages = append(messages, map[string]string{"from": "Nori", "text": reply})
	}
	return map[string]any{"started_at": t.StartedAt, "messages": messages}, users, runs
}

func publishedReply(t state.ConversationTurn) string {
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
