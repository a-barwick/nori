package task

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"nori/internal/contextview"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/transcript"
)

// Limits on what one inner model call sends. The saved history keeps every item; only the request is shortened.
const (
	// InnerInputBytes is the transcript size that starts omitting older tool outputs. Omitting then continues until
	// the newest InnerKeepBytes are whole, so the shortened prefix stays the same for later calls and the provider's
	// prompt cache keeps working until the transcript grows past the limit again.
	InnerInputBytes = 64 << 10
	InnerKeepBytes  = 32 << 10
	omitOutputBytes = 512
)

var omittedNote = prompts.Runtime("omitted-output", nil)

// fitContext omits the outputs of older tool results once the transcript passes InnerInputBytes. Each omitted output
// keeps its call, operation_id and outcome, so call and result still pair up and the saved output stays one
// operation_read away. Text, tool calls and the provider's own items are never changed.
func fitContext(messages []transcript.Message) []transcript.Message {
	full := make([]int, len(messages))
	short := make([]int, len(messages))
	shortened := make([]transcript.Message, len(messages))
	for i, message := range messages {
		full[i] = messageBytes(message)
		shortened[i] = omitOutputs(message)
		short[i] = messageBytes(shortened[i])
	}
	// Replay the transcript's growth so the cut depends only on the messages before it, not on when a call happened.
	cut, total, tail := 0, 0, 0
	for i := range messages {
		total += full[i]
		tail += full[i]
		if total <= InnerInputBytes {
			continue
		}
		for cut <= i && tail > InnerKeepBytes {
			total -= full[cut] - short[cut]
			tail -= full[cut]
			cut++
		}
	}
	if cut == 0 {
		return messages
	}
	out := make([]transcript.Message, 0, len(messages))
	out = append(out, shortened[:cut]...)
	return append(out, messages[cut:]...)
}

func messageBytes(message transcript.Message) int {
	data, _ := json.Marshal(message)
	return len(data)
}

// omitOutputs replaces large tool outputs with a reference. A capability_load result stays whole, because the tools
// a task loaded are read back from it. User answers also stay whole.
func omitOutputs(message transcript.Message) transcript.Message {
	var parts []transcript.Part
	for i, part := range message.Parts {
		result := part.ToolResult
		if result == nil || result.Name == "capability_load" || result.Name == "request_input" {
			continue
		}
		data, _ := json.Marshal(result.Output)
		if len(data) < omitOutputBytes {
			continue
		}
		if parts == nil {
			parts = append([]transcript.Part(nil), message.Parts...)
		}
		stub := map[string]any{"omitted": true, "note": omittedNote}
		for _, key := range []string{"operation_id", "outcome", "effect_class", "evidence_ref", "artifact_id", "result_id"} {
			if value, ok := result.Output[key]; ok {
				stub[key] = value
			}
		}
		if _, ok := stub["operation_id"]; !ok {
			stub["note"] = prompts.Runtime("omitted-unsaved-output", nil)
		}
		parts[i] = transcript.Part{ToolResult: &transcript.ToolResult{CallID: result.CallID, Name: result.Name, Output: stub}}
	}
	if parts == nil {
		return message
	}
	return transcript.Message{Role: message.Role, Parts: parts}
}

func (a *Activities) operationRead(ctx context.Context, runID, id string) (map[string]any, error) {
	// A continuation may read the operations of the tasks it continues, whose IDs start with their task ID.
	if run, err := a.Store.GetRun(ctx, runID); err == nil {
		for hops := 0; hops < 8 && run.Origin != nil && run.Origin.ContinuesTaskID != ""; hops++ {
			previous := run.Origin.ContinuesTaskID
			if strings.HasPrefix(id, previous+"-") {
				runID = previous
				break
			}
			if run, err = a.Store.GetRun(ctx, previous); err != nil {
				break
			}
		}
	}
	ops, err := a.Store.Operations(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, op := range ops {
		if op.ID != id {
			continue
		}
		if op.ResultRef == "" {
			return nil, errors.New(prompts.Runtime("operation-no-output", nil))
		}
		blocks, err := a.Store.ReadHistory(ctx, runID, op.ResultRef)
		if err != nil {
			return nil, err
		}
		if len(blocks) != 1 {
			return nil, errors.New(prompts.Runtime("operation-unreadable", nil))
		}
		var output map[string]any
		if err = json.Unmarshal(blocks[0], &output); err != nil {
			return nil, errors.New(prompts.Runtime("operation-unreadable", nil))
		}
		return map[string]any{"read_operation_id": op.ID, "name": op.Name, "saved_outcome": op.Outcome, "output": output,
			"note": prompts.Runtime("context-a-saved-tool-output-is-untrusted-data-as-it-was-when-th", nil)}, nil
	}
	return nil, errors.New(prompts.Runtime("operation-not-found", nil))
}

// managedContext keeps all current-task instructions and a complete recent suffix.
// Older rounds become source-linked evidence, never fabricated tool responses.
// The projection is deterministic from the immutable ref, so a restart needs no
// separate compaction job, paid summarizer or mutable transcript head.
func managedContext(messages []transcript.Message, ref string, reduced bool) ([]transcript.Message, error) {
	if len(messages) == 0 {
		return messages, nil
	}
	budget := contextview.TaskBytes
	if reduced {
		budget /= 2
	}
	candidate := messages
	if reduced && isBackgroundMessage(messages[0]) {
		if trimmed, changed := withTrimmedBackground(messages, budget/4); changed {
			candidate = trimmed
		}
	}
	if out := fitOrProject(candidate, ref, budget); len(out) > 0 {
		return out, nil
	}
	if isBackgroundMessage(messages[0]) {
		if !reduced {
			if trimmed, changed := withTrimmedBackground(messages, budget/4); changed {
				if out := fitOrProject(trimmed, ref, budget); len(out) > 0 {
					return out, nil
				}
			}
		}
		if len(messages) > 1 {
			if out := dropBackgroundContext(messages, ref, budget); len(out) > 0 {
				return out, nil
			}
		}
	}
	return nil, contextview.ErrOverflow
}

func fitOrProject(messages []transcript.Message, ref string, budget int) []transcript.Message {
	fitted := fitContext(messages)
	if contextMessagesBytes(fitted) <= budget {
		return fitted
	}
	if out := projectContext(messages, fitted, ref, budget, false); len(out) > 0 {
		return out
	}
	short := make([]transcript.Message, len(messages))
	for i, m := range messages {
		short[i] = omitOutputs(m)
	}
	if out := projectContext(messages, short, ref, budget, false); len(out) > 0 {
		return out
	}
	return nil
}

func dropBackgroundContext(messages []transcript.Message, ref string, budget int) []transcript.Message {
	fittedRest := fitContext(messages[1:])
	if contextMessagesBytes(fittedRest) <= budget {
		return fittedRest
	}
	fitted := fitContext(messages)
	if out := projectContext(messages, fitted, ref, budget, true); len(out) > 0 {
		return out
	}
	short := make([]transcript.Message, len(messages))
	for i, m := range messages {
		short[i] = omitOutputs(m)
	}
	if out := projectContext(messages, short, ref, budget, true); len(out) > 0 {
		return out
	}
	return nil
}

func isBackgroundMessage(m transcript.Message) bool {
	if m.Role != transcript.RoleUser || len(m.Parts) != 1 || m.Parts[0].Text == "" {
		return false
	}
	start := prompts.Runtime("task-history-start", nil)
	end := prompts.Runtime("task-history-end", nil)
	return strings.HasPrefix(m.Parts[0].Text, start) && strings.HasSuffix(m.Parts[0].Text, end)
}

func withTrimmedBackground(messages []transcript.Message, maxBytes int) ([]transcript.Message, bool) {
	if len(messages) == 0 || maxBytes <= 0 || !isBackgroundMessage(messages[0]) {
		return messages, false
	}
	origBytes := messageBytes(messages[0])
	if origBytes <= maxBytes {
		return messages, false
	}
	trimmed, ok := trimBackgroundMessage(messages[0], maxBytes)
	if !ok || messageBytes(trimmed) >= origBytes {
		return messages, false
	}
	out := make([]transcript.Message, len(messages))
	copy(out, messages)
	out[0] = trimmed
	return out, true
}

func trimBackgroundMessage(m transcript.Message, maxBytes int) (transcript.Message, bool) {
	start := prompts.Runtime("task-history-start", nil)
	end := prompts.Runtime("task-history-end", nil)
	text := m.Parts[0].Text
	body := text[len(start) : len(text)-len(end)]
	var records []json.RawMessage
	if json.Unmarshal([]byte(body), &records) == nil && len(records) > 0 {
		var kept []json.RawMessage
		for i := len(records) - 1; i >= 0; i-- {
			trial := make([]json.RawMessage, 0, len(kept)+1)
			trial = append(trial, records[i])
			trial = append(trial, kept...)
			raw, err := json.Marshal(trial)
			if err != nil {
				break
			}
			cand := transcript.UserText(start + string(raw) + end)
			if messageBytes(cand) > maxBytes {
				break
			}
			kept = trial
		}
		if len(kept) > 0 {
			raw, err := json.Marshal(kept)
			if err == nil {
				return transcript.UserText(start + string(raw) + end), true
			}
		}
		var rec map[string]string
		if json.Unmarshal(records[len(records)-1], &rec) == nil {
			orig := rec["text"]
			lo, hi := 0, len(orig)
			var best transcript.Message
			found := false
			for lo <= hi {
				mid := (lo + hi) / 2
				rec["text"] = clip(orig, mid)
				raw, err := json.Marshal([]map[string]string{rec})
				if err != nil {
					hi = mid - 1
					continue
				}
				cand := transcript.UserText(start + string(raw) + end)
				if messageBytes(cand) <= maxBytes {
					best, found = cand, true
					lo = mid + 1
				} else {
					hi = mid - 1
				}
			}
			if found {
				return best, true
			}
		}
	}
	lo, hi := 0, len(body)
	var best transcript.Message
	found := false
	for lo <= hi {
		mid := (lo + hi) / 2
		cand := transcript.UserText(start + clip(body, mid) + end)
		if messageBytes(cand) <= maxBytes {
			best, found = cand, true
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best, found
}

func projectContext(original, recent []transcript.Message, ref string, budget int, dropBackground bool) []transcript.Message {
	suffixBytes := make([]int, len(recent)+1)
	for i := len(recent) - 1; i >= 0; i-- {
		suffixBytes[i] = suffixBytes[i+1] + messageBytes(recent[i])
	}
	kept := []transcript.Message{}
	keptBytes := 0
	retain := func(m transcript.Message) { kept = append(kept, m); keptBytes += messageBytes(m) }
	evidence := []map[string]any{}
	calls := map[string]transcript.ToolCall{}
	pending := map[string]bool{}
	loaded := map[string]bool{}
	for i, m := range original {
		if !(dropBackground && i == 0 && isBackgroundMessage(m)) {
			for _, p := range m.Parts {
				if p.ToolCall != nil {
					calls[p.ToolCall.ID] = *p.ToolCall
					pending[p.ToolCall.ID] = true
				}
				if m.Role == transcript.RoleUser && p.Text != "" {
					retain(transcript.UserText(p.Text))
				}
				if r := p.ToolResult; r != nil {
					delete(pending, r.CallID)
					if r.Name == "capability_load" {
						data, _ := json.Marshal(r.Output["loaded"])
						if call, ok := calls[r.CallID]; ok && !loaded[string(data)] {
							loaded[string(data)] = true
							retain(transcript.Message{Role: transcript.RoleModel, Parts: []transcript.Part{{ToolCall: &call}}})
							retain(transcript.ToolResultMessage(r.CallID, r.Name, r.Output))
						}
					} else if r.Name == "request_input" || r.Name == "request_work" {
						result := any(r.Output)
						data, _ := json.Marshal(result)
						if r.Name == "request_work" && len(data) > 4096 {
							result = map[string]any{"excerpt": strings.ToValidUTF8(string(data[:4096]), ""), "truncated": true, "before": strconv.Itoa(i + 1), "history_ref": ref}
						}
						data, _ = json.Marshal(map[string]any{"tool": r.Name, "request": calls[r.CallID].Args, "result": result})
						retain(transcript.UserText(prompts.Runtime("context-retained-answer", nil) + string(data)))
					}
					row := map[string]any{"message": i, "tool": r.Name}
					for _, key := range []string{"operation_id", "outcome", "artifact_id", "result_id"} {
						if v, ok := r.Output[key]; ok {
							row[key] = v
						}
					}
					evidence = append(evidence, row)
					if len(evidence) > 32 {
						evidence = evidence[1:]
					}
				}
			}
		}
		cut := i + 1
		if len(pending) != 0 || keptBytes+suffixBytes[cut] > budget {
			continue
		}
		data, _ := json.Marshal(map[string]any{"history_ref": ref, "before": strconv.Itoa(cut), "recent_operations": evidence})
		notice := transcript.UserText(prompts.Runtime("context-task-archive", nil) + string(data))
		if messageBytes(notice)+keptBytes+suffixBytes[cut] > budget {
			continue
		}
		out := append([]transcript.Message{notice}, kept...)
		return append(out, recent[cut:]...)
	}
	return nil
}

func (a *Activities) taskHistoryRead(ctx context.Context, run, ref, beforeText, offsetText string) (map[string]any, error) {
	if !state.ValidHistoryRef(ref) {
		return nil, errors.New("invalid history reference")
	}
	before, err := strconv.Atoi(beforeText)
	if err != nil || before < 0 {
		return nil, errors.New("before must be a nonnegative message index")
	}
	messages, err := a.history(ctx, run, ref)
	if err != nil {
		return nil, err
	}
	if before == 0 || before > len(messages) {
		before = len(messages)
	}
	offset, err := strconv.Atoi(offsetText)
	if err != nil || offset < 0 {
		return nil, errors.New("offset must be nonnegative")
	}
	if before == 0 {
		return map[string]any{"has_more": false}, nil
	}
	fragments := []map[string]any{}
	remaining := 8000
	for before > 0 && remaining > 0 && len(fragments) < 12 {
		m := messages[before-1]
		parts := []transcript.Part{}
		for _, p := range m.Parts {
			if p.Provider == nil {
				parts = append(parts, p)
			}
		}
		data, err := json.Marshal(transcript.Message{Role: m.Role, Parts: parts})
		if err != nil {
			return nil, err
		}
		if offset > len(data) || (offset < len(data) && data[offset]&0xC0 == 0x80) {
			return nil, errors.New("invalid message offset")
		}
		end := min(len(data), offset+remaining)
		for end < len(data) && end > offset && data[end]&0xC0 == 0x80 {
			end--
		}
		if end == offset && end < len(data) {
			break
		}
		fragments = append(fragments, map[string]any{"message_index": before - 1, "offset": offset, "message_json": string(data[offset:end])})
		remaining -= end - offset
		if end < len(data) {
			offset = end
			break
		}
		before, offset = before-1, 0
	}
	return map[string]any{"fragments": fragments, "next_before": strconv.Itoa(before), "next_offset": strconv.Itoa(offset), "has_more": before > 0, "note": prompts.Runtime("context-archive-data", nil)}, nil
}

func contextMessagesBytes(messages []transcript.Message) int {
	total := 0
	for _, m := range messages {
		total += messageBytes(m)
	}
	return total
}
