package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

func memoryMessage(memories []state.Memory) json.RawMessage {
	content := prompts.Runtime("memories-none", nil)
	if len(memories) > 0 {
		ordered := slices.Clone(memories)
		state.SortCoreMemories(ordered)
		type shown struct {
			ID         string `json:"id"`
			Text       string `json:"text"`
			About      string `json:"about,omitempty"`
			Section    string `json:"section,omitempty"`
			Pinned     bool   `json:"pinned,omitempty"`
			Provenance string `json:"provenance,omitempty"`
			Saved      string `json:"saved"`
		}
		list := make([]shown, 0, len(ordered))
		for _, m := range ordered {
			about := m.About
			if about == "the user" {
				about = ""
			}
			prov := string(m.EffectiveProvenance())
			if prov == string(state.ProvenanceUserExplicit) {
				prov = ""
			}
			list = append(list, shown{
				ID:         m.ID,
				Text:       m.Text,
				About:      about,
				Section:    m.Section,
				Pinned:     m.Pinned,
				Provenance: prov,
				Saved:      m.UpdatedAt.Format("2006-01-02"),
			})
		}
		data, _ := json.Marshal(list)
		content = prompts.Runtime("memories", map[string]any{"Memories": string(data)})
	}
	return modelMessage(map[string]string{"role": "system", "content": content})
}

func summaryMessage(s state.ConversationSummary) json.RawMessage {
	type shown struct {
		Kind      string `json:"kind"`
		UserWords string `json:"user_words"`
		InputID   string `json:"input_id"`
		ModelNote string `json:"model_note,omitempty"`
	}
	items := make([]shown, 0, len(s.Items))
	for _, item := range s.Items {
		items = append(items, shown{item.Kind, item.Quote, item.InputID, item.Note})
	}
	payload := map[string]any{
		"items":         items,
		"model_gist":    s.Gist,
		"turns_indexed": s.Turns,
	}
	if len(s.Excerpts) > 0 {
		payload["source_excerpts"] = s.Excerpts
	}
	data, _ := json.Marshal(payload)
	return modelMessage(map[string]string{"role": "system", "content": prompts.Runtime("summary", map[string]any{"Summary": string(data)})})
}

type recalledMemoryLog struct {
	Date       string `json:"date"`
	Section    string `json:"section,omitempty"`
	About      string `json:"about,omitempty"`
	Provenance string `json:"provenance"`
	Text       string `json:"text"`
}

func (r *Runner) recallMemoryLogs(ctx context.Context, query string, contextFrom, now time.Time) []recalledMemoryLog {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	hits, err := r.tasks.Store.SearchMemory(ctx, docstore.MemorySearchQuery{
		Query:        query,
		Layer:        docstore.MemoryLayerLog,
		CreatedAfter: contextFrom,
		Limit:        state.TailRecallTopK,
		Now:          now,
	})
	if err != nil {
		if ctx.Err() == nil {
			slog.WarnContext(ctx, "tail memory auto-recall unavailable", "err", err)
		}
		return nil
	}
	return formatRecalledLogs(hits, contextFrom)
}

func formatRecalledLogs(hits []docstore.MemorySearchHit, contextFrom time.Time) []recalledMemoryLog {
	if len(hits) == 0 {
		return nil
	}
	recalled := make([]recalledMemoryLog, 0, len(hits))
	for _, hit := range hits {
		if !contextFrom.IsZero() && hit.Chunk.CreatedAt.Before(contextFrom) {
			continue
		}
		text := strings.TrimSpace(hit.Snippet)
		if text == "" {
			text = strings.TrimSpace(hit.Chunk.Body)
		}
		if text == "" {
			continue
		}
		text = docstore.ClipUTF8(text, docstore.MaxMemorySnippetBytes)
		item := recalledMemoryLog{
			Date:       hit.Chunk.LogDate,
			Section:    hit.Chunk.Section,
			About:      hit.Chunk.About,
			Provenance: hit.Chunk.Provenance,
			Text:       text,
		}
		if item.Date == "" && !hit.Chunk.CreatedAt.IsZero() {
			item.Date = hit.Chunk.CreatedAt.UTC().Format("2006-01-02")
		}
		candidate := append(recalled, item)
		data, err := json.Marshal(candidate)
		if err != nil || len(data) > state.TailRecallBytes {
			break
		}
		recalled = candidate
	}
	if len(recalled) == 0 {
		return nil
	}
	return recalled
}

func splitMemory(calls []ToolCall) ([]ToolCall, []memoryCall, error) {
	commands := make([]ToolCall, 0, len(calls))
	changes := []memoryCall{}
	for _, call := range calls {
		switch call.Function.Name {
		case state.MemoryRemember:
			var args struct {
				Text     string  `json:"text"`
				About    string  `json:"about"`
				Section  *string `json:"section"`
				Replaces *string `json:"replaces"`
			}
			if err := decodeCommandArguments(call.Function.Arguments, &args); err != nil {
				return nil, nil, err
			}
			change := state.MemoryChange{
				Kind:  state.MemoryRemember,
				Text:  strings.TrimSpace(args.Text),
				About: strings.TrimSpace(args.About),
			}
			if args.Section != nil {
				change.Section = strings.TrimSpace(*args.Section)
			}
			if args.Replaces != nil && *args.Replaces != "" {
				if !canonicalID(*args.Replaces) {
					return nil, nil, fmt.Errorf("remember names an invalid memory ID")
				}
				change.ID, change.Replaces = *args.Replaces, true
			}
			changes = append(changes, memoryCall{call.ID, change})
		case state.MemoryForget:
			var args struct {
				MemoryID string `json:"memory_id"`
			}
			if err := decodeCommandArguments(call.Function.Arguments, &args); err != nil {
				return nil, nil, err
			}
			if !canonicalID(args.MemoryID) {
				return nil, nil, fmt.Errorf("forget names an invalid memory ID")
			}
			changes = append(changes, memoryCall{call.ID, state.MemoryChange{Kind: state.MemoryForget, ID: args.MemoryID}})
		default:
			commands = append(commands, call)
		}
	}
	return commands, changes, nil
}

func canonicalID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}

type memoryCall struct {
	callID string
	change state.MemoryChange
}

func planMemory(attemptID string, calls []memoryCall, saved []state.Memory) ([]state.MemoryChange, bool) {
	changes := make([]state.MemoryChange, 0, len(calls))
	for _, call := range calls {
		change := call.change
		if change.Kind == state.MemoryRemember && !change.Replaces {
			change.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(attemptID+":memory:"+call.callID)).String()
		}
		changes = append(changes, change)
	}
	return changes, state.PlanMemoryChanges(saved, changes)
}

func memoryFailureText(changes []state.MemoryChange) string {
	for _, c := range changes {
		if c.Error != "" {
			return prompts.Runtime("memory-change-failed", map[string]any{"Error": c.Error})
		}
	}
	return ""
}

func memoryText(changes []state.MemoryChange) string {
	if len(changes) == 0 {
		return ""
	}
	return prompts.Runtime("memory-changed", nil)
}
