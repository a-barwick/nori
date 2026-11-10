package chat

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"nori/internal/contextview"
	"nori/internal/prompts"
	"nori/internal/state"
)

func messageGroup(m state.ChatMessage) []json.RawMessage {
	group := []json.RawMessage{}
	if len(m.Attachments) > 0 {
		b, _ := json.Marshal(m.Attachments)
		group = append(group, raw("user", prompts.Runtime("chat-attachments", nil)+string(b)))
	}
	if m.Text != "" {
		group = append(group, raw(m.Role, m.Text))
	}
	if len(m.Operations) > 0 {
		b, _ := json.Marshal(m.Operations)
		group = append(group, raw("user", prompts.Runtime("chat-history-evidence", nil)+string(b)))
	}
	return group
}
func rawSize(messages []json.RawMessage) int {
	n := 0
	for _, m := range messages {
		n += len(m)
	}
	return n
}
func excerpt(m state.ChatMessage) state.ChatExcerpt {
	text := m.Text
	if len(text) > 600 {
		text = strings.ToValidUTF8(text[:600], "")
	}
	return state.ChatExcerpt{HasAttachments: len(m.Attachments) > 0, HasOperations: len(m.Operations) > 0, Status: m.Status, ID: m.ID, Sequence: m.Sequence, Role: m.Role, Text: text, Truncated: len(text) < len(m.Text)}
}

// prepareContext runs before marking a model request pending. A crash can repeat
// these reads and fenced, idempotent index commits without repeating paid work.
func (s *Service) prepareContext(ctx context.Context, id, turn string) (state.Chat, []json.RawMessage, error) {
	c, err := s.update(ctx, id, turn, func(*state.Chat, *state.ChatMessage) (string, error) { return "", nil })
	if err != nil {
		return c, nil, err
	}
	limit := contextview.ReplyBytes
	if c.Checkpoint.ContextReduced {
		limit /= 2
	}
	current, err := s.Store.ChatMessage(ctx, id, state.UserMessageID(turn))
	if err != nil {
		return c, nil, err
	}
	reply, err := replyMessage(&c, turn)
	if err != nil {
		return c, nil, err
	}
	fixed := []json.RawMessage{raw("system", prompts.Chat), raw("system", "User details (data): "+s.facts(ctx))}
	// Keep the newest read whole; older results can be reread from their saved
	// arguments. Shedding evidence never changes the saved operations.
	evidence := readEvidence(reply.Operations, 0)
	base := rawSize(fixed) + rawSize(messageGroup(current)) + (20 << 10)
	for omitted := 1; base+rawSize(evidence) > limit && omitted < len(reply.Operations); omitted++ {
		evidence = readEvidence(reply.Operations, omitted)
	}
	used := base + rawSize(evidence)
	if used > limit {
		return c, nil, contextview.ErrOverflow
	}
	start := current.Sequence
	// Only whole user/reply pairs join the recent suffix.
	for i := len(c.Messages) - 1; i >= 0; i-- {
		m := c.Messages[i]
		if m.Sequence >= current.Sequence || m.Role != "user" {
			continue
		}
		groupSize := 0
		for _, n := range c.Messages {
			if n.Sequence >= m.Sequence && n.Sequence < start {
				groupSize += rawSize(messageGroup(n))
			}
		}
		if used+groupSize > limit {
			break
		}
		used += groupSize
		start = m.Sequence
	}
	if c.Context != nil {
		start = max(start, c.Context.Through+1)
	}
	through := start - 1
	// Catch up oldest first, including legacy chats larger than the snapshot page.
	// Each page commits its own boundary; it cannot skip an unread message.
	for through > 0 && (c.Context == nil || c.Context.Through < through) {
		after := int64(0)
		if c.Context != nil {
			after = c.Context.Through
		}
		page, e := s.Store.ChatMessagesAfter(ctx, id, after)
		if e != nil {
			return c, nil, e
		}
		next := state.ChatContext{}
		if c.Context != nil {
			next = *c.Context
			next.Excerpts = slices.Clone(next.Excerpts)
		}
		for _, m := range page {
			if m.Sequence > through {
				break
			}
			if m.Sequence != next.Through+1 {
				return c, nil, errors.New("chat context history has a gap")
			}
			next.Through = m.Sequence
			if m.Text != "" || len(m.Attachments) > 0 || len(m.Operations) > 0 {
				next.Excerpts = append(next.Excerpts, excerpt(m))
			}
		}
		if next.Through == after {
			return c, nil, errors.New("chat context source is missing")
		}
		// Keep the opening request and newest quotations. This is explicitly an index,
		// not a claim that every old instruction is represented in the model view.
		for len(next.Excerpts) > 1 {
			encoded, _ := json.Marshal(next.Excerpts)
			if len(next.Excerpts) <= 24 && len(encoded) <= 16<<10 {
				break
			}
			next.Excerpts = append(next.Excerpts[:1:1], next.Excerpts[2:]...)
		}
		c, e = s.update(ctx, id, turn, func(c *state.Chat, _ *state.ChatMessage) (string, error) {
			actual := int64(0)
			if c.Context != nil {
				actual = c.Context.Through
			}
			if actual != after {
				return "", state.ErrConversationStale
			}
			c.Context = &next
			return "context.updated", nil
		})
		if e != nil {
			return c, nil, e
		}
	}
	messages := []json.RawMessage{raw("system", prompts.Chat)}
	if c.Context != nil && c.Context.Through > 0 {
		b, _ := json.Marshal(map[string]any{"through": c.Context.Through, "excerpts": c.Context.Excerpts})
		messages = append(messages, raw("user", prompts.Runtime("chat-context-archive", nil)+string(b)))
	}
	for _, m := range c.Messages {
		if m.Sequence >= start && m.Sequence < current.Sequence {
			messages = append(messages, messageGroup(m)...)
		}
	}
	messages = append(messages, messageGroup(current)...)
	messages = append(messages, evidence...)
	if rawSize(messages)+rawSize(fixed[1:]) > limit {
		return c, nil, contextview.ErrOverflow
	}
	if c.Checkpoint.RejectedContextBytes > 0 && rawSize(messages) >= c.Checkpoint.RejectedContextBytes {
		return c, nil, contextview.ErrOverflow
	}
	return c, messages, nil
}

func (s *Service) readHistory(ctx context.Context, id, turn string, request state.ChatReadCheckpoint) (map[string]any, error) {
	if request.MessageID != "" {
		if strings.ContainsAny(request.MessageID, "/\\") || request.MessageID == "." || request.MessageID == ".." {
			return nil, state.ErrNotFound
		}
		m, err := s.Store.ChatMessage(ctx, id, request.MessageID)
		if err != nil {
			return nil, err
		}
		if m.ID == state.AssistantMessageID(turn) {
			return nil, errors.New("the current reply is not historical evidence")
		}
		// Page the entire visible record so attachments and saved tool evidence
		// remain reachable even after the text-only index has moved on.
		data, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		if request.Offset < 0 || request.Offset > len(data) || (request.Offset < len(data) && data[request.Offset]&0xC0 == 0x80) {
			return nil, errors.New("invalid message offset")
		}
		end := min(len(data), request.Offset+8000)
		for end < len(data) && end > request.Offset && data[end]&0xC0 == 0x80 {
			end--
		}
		return map[string]any{"message_id": m.ID, "message_json": string(data[request.Offset:end]), "next_offset": end, "has_more": end < len(data), "note": prompts.Runtime("context-archive-data", nil)}, nil
	}
	page, err := s.Store.ChatMessages(ctx, id, request.Before)
	if err != nil {
		return nil, err
	}
	messages := page.Messages
	if len(messages) > 12 {
		messages = messages[len(messages)-12:]
	}
	excerpts := []state.ChatExcerpt{}
	for _, m := range messages {
		if m.ID != state.AssistantMessageID(turn) {
			excerpts = append(excerpts, excerpt(m))
		}
	}
	before := int64(0)
	if len(messages) > 0 {
		before = messages[0].Sequence
	}
	return map[string]any{"messages": excerpts, "next_before": before, "has_more": before > 1, "note": prompts.Runtime("context-archive-data", nil)}, nil
}

func readEvidence(ops []state.ChatOperation, omitBefore int) []json.RawMessage {
	out := []json.RawMessage{}
	for i, original := range ops {
		op := original
		if i < omitBefore {
			op.Result = map[string]any{"omitted": true, "note": "Earlier read result omitted to fit context. Repeat the saved read arguments only if those details are still needed."}
		}
		if json.Valid([]byte(op.Arguments)) {
			action, _ := json.Marshal(map[string]any{"actions": []any{map[string]any{"name": op.Name, "arguments": json.RawMessage(op.Arguments)}}, "text": ""})
			out = append(out, raw("assistant", string(action)))
		}
		b, _ := json.Marshal(op)
		out = append(out, raw("user", prompts.Runtime("chat-tool-evidence", nil)+string(b)))
	}
	return out
}

func (s *Service) contextFailure(id, turn string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.update(ctx, id, turn, func(c *state.Chat, m *state.ChatMessage) (string, error) {
		m.Text = prompts.Runtime("context-too-large", nil)
		return endTurn(c, m, "failed"), nil
	})
}
