// Package chat runs durable foreground turns alongside the existing assistant.
package chat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"nori/internal/agent"
	"nori/internal/conversation"
	"nori/internal/prompts"
	"nori/internal/state"
)

const (
	// persistEvery bounds how often streamed text is saved. Each save is one
	// database transaction and one event, so saving every token would not scale.
	persistEvery = 400 * time.Millisecond
	turnLease    = 10 * time.Second
	maxSteps     = 8
)

type run struct {
	turn   string
	cancel context.CancelFunc
}

type Service struct {
	Store   state.ConversationStore
	Model   conversation.ReplyModel
	Tools   *agent.Service
	owner   string
	mu      sync.Mutex
	running map[string]run
}

func New(store state.ConversationStore, model conversation.ReplyModel, tools *agent.Service) *Service {
	return &Service{Store: store, Model: model, Tools: tools, owner: uuid.NewString(), running: map[string]run{}}
}

func (s *Service) Create(ctx context.Context, title string) (state.Chat, error) {
	c := state.Chat{ID: uuid.NewString(), Title: title, Mode: "chat", Messages: []state.ChatMessage{}, UpdatedAt: time.Now().UTC()}
	if c.Title == "" {
		c.Title = "New chat"
	}
	return c, s.Store.CreateChat(ctx, c)
}

func (s *Service) List(ctx context.Context) ([]state.ChatSummary, error) {
	return s.Store.ChatSummaries(ctx)
}

// Delete removes an idle chat, its history and its files.
func (s *Service) Delete(ctx context.Context, id string) error {
	uploads, err := s.Store.DeleteChat(ctx, id)
	for _, upload := range uploads {
		if e := s.Store.DeleteFile(ctx, uploadKey(id, upload)); e != nil {
			log.Printf("chat file cleanup failed for an upload: %v", e)
		}
	}
	return err
}

func (s *Service) Accept(ctx context.Context, id, input, text string, attachments []string) (state.Chat, error) {
	assigned := []state.Attachment{}
	for _, upload := range attachments {
		u, err := s.Store.ChatUpload(ctx, id, upload)
		if err != nil {
			return state.Chat{}, state.ErrAttachmentMissing
		}
		assigned = append(assigned, u.Attachment)
	}
	return s.Store.UpdateChat(ctx, id, func(c *state.Chat) (string, error) {
		for _, m := range c.Messages {
			if m.ID == input+"-user" {
				if m.Text != text || !slices.Equal(m.Attachments, assigned) {
					return "", state.ErrMessageConflict
				}
				return "", nil
			}
		}
		if c.ActiveTurn != nil {
			return "", state.ErrChatBusy
		}
		if c.MessageCount == 0 && len(c.Messages) == 0 && c.Title == "New chat" {
			title := []rune(text)
			if len(title) > 48 {
				title = title[:48]
			}
			if len(title) > 0 {
				c.Title = string(title)
			}
		}
		c.ActiveTurn = &input
		c.Started = ""
		c.LeaseUntil = time.Time{}
		c.Messages = append(c.Messages, state.ChatMessage{ID: input + "-user", Role: "user", Text: text, Status: "completed", Attachments: assigned}, state.ChatMessage{ID: input + "-assistant", Role: "assistant", Status: "streaming"})
		return "input.accepted", nil
	}, input+"-user")
}

// Tick recovers durable accepted work even when the browser and request disappear.
// Its lease fences concurrent service processes.
func (s *Service) Tick(ctx context.Context) error {
	chats, err := s.Store.ActiveChats(ctx)
	if err != nil {
		return err
	}
	for _, c := range chats {
		if c.ActiveTurn != nil {
			s.launch(ctx, c.ID, *c.ActiveTurn)
		}
	}
	return nil
}

func (s *Service) launch(parent context.Context, id, turn string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.running[id]; ok && r.turn == turn {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.running[id] = run{turn: turn, cancel: cancel}
	go func() {
		defer func() {
			cancel()
			s.mu.Lock()
			if s.running[id].turn == turn {
				delete(s.running, id)
			}
			s.mu.Unlock()
		}()
		s.run(ctx, id, turn)
	}()
}

// stop cancels the local run of one turn.
func (s *Service) stop(id, turn string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.running[id]; ok && r.turn == turn {
		r.cancel()
	}
}

// Process runs the hosted wake. It returns ErrConversationBusy while any turn
// still needs work, so the durable queue retries the wake.
func (s *Service) Process(ctx context.Context) error {
	if err := s.Tick(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	scan := time.NewTicker(time.Second)
	defer scan.Stop()
	for {
		s.mu.Lock()
		n := len(s.running)
		s.mu.Unlock()
		if n == 0 {
			chats, err := s.Store.ActiveChats(ctx)
			if err != nil {
				return err
			}
			for _, c := range chats {
				if c.ActiveTurn != nil {
					return state.ErrConversationBusy
				}
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-scan.C:
			if err := s.Tick(ctx); err != nil {
				return err
			}
		case <-ticker.C:
		}
	}
}

func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.running {
		r.cancel()
	}
}

// owns reports whether this process still holds the turn's lease.
func (s *Service) owns(c *state.Chat, turn string) bool {
	return c.ActiveTurn != nil && *c.ActiveTurn == turn && c.Started == s.owner && c.LeaseUntil.After(time.Now())
}

func (s *Service) update(ctx context.Context, id, turn, kind string, f func(*state.ChatMessage)) (state.Chat, error) {
	return s.Store.UpdateChat(ctx, id, func(c *state.Chat) (string, error) {
		if !s.owns(c, turn) {
			return "", state.ErrCancelled
		}
		f(&c.Messages[len(c.Messages)-1])
		c.LeaseUntil = time.Now().Add(turnLease)
		return kind, nil
	})
}

func (s *Service) finish(id, turn, status string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.Store.UpdateChat(ctx, id, func(c *state.Chat) (string, error) {
		if !s.owns(c, turn) {
			return "", nil
		}
		m := &c.Messages[len(c.Messages)-1]
		m.Status = status
		if status == "failed" && m.Text == "" {
			m.Text = prompts.Runtime("chat-interrupted", nil)
		}
		c.ActiveTurn = nil
		c.Started = ""
		c.LeaseUntil = time.Time{}
		return "turn." + status, nil
	})
	if err != nil {
		log.Printf("chat %s: saving the %s status of turn %s failed: %v", id, status, turn, err)
	}
}

func (s *Service) Cancel(ctx context.Context, id, turn string) (state.Chat, error) {
	c, err := s.Store.UpdateChat(ctx, id, func(c *state.Chat) (string, error) {
		if c.ActiveTurn == nil {
			for _, m := range c.Messages {
				if m.ID == turn+"-assistant" {
					return "", nil
				}
			}
			return "", state.ErrNotFound
		}
		if *c.ActiveTurn != turn {
			return "", state.ErrQuestionStale
		}
		m := &c.Messages[len(c.Messages)-1]
		m.Status = "cancelled"
		c.ActiveTurn = nil
		c.Started = ""
		c.LeaseUntil = time.Time{}
		return "turn.cancelled", nil
	}, turn+"-assistant")
	if err == nil {
		s.stop(id, turn)
	}
	return c, err
}

// claim takes the turn's lease.
func (s *Service) claim(ctx context.Context, id, turn string) (state.Chat, error) {
	return s.Store.UpdateChat(ctx, id, func(c *state.Chat) (string, error) {
		if c.ActiveTurn == nil || *c.ActiveTurn != turn {
			return "", state.ErrCancelled
		}
		m := &c.Messages[len(c.Messages)-1]
		if c.Started != "" {
			if c.LeaseUntil.After(time.Now()) {
				return "", state.ErrConversationBusy
			}
			// Another process started this reply and stopped. Its provider call is never repeated.
			m.Status = "failed"
			c.ActiveTurn = nil
			c.Started = ""
			return "turn.failed", nil
		}
		c.Started = s.owner
		c.LeaseUntil = time.Now().Add(turnLease)
		return "reply.updated", nil
	})
}

func (s *Service) run(ctx context.Context, id, turn string) {
	c, err := s.claim(ctx, id, turn)
	if err != nil || c.ActiveTurn == nil {
		return
	}
	defer s.heartbeat(ctx, id, turn)()
	messages := history(c, turn)
	for step := 0; step < maxSteps; step++ {
		reply, err := s.respond(ctx, id, turn, messages)
		if err != nil {
			if ctx.Err() == nil {
				s.finish(id, turn, "failed")
			}
			return
		}
		if len(reply.Calls) == 0 {
			if _, err = s.update(ctx, id, turn, "reply.updated", func(m *state.ChatMessage) { m.Text = reply.Text }); err == nil {
				s.finish(id, turn, "completed")
			}
			return
		}
		if len(reply.Calls) != 1 {
			s.finish(id, turn, "failed")
			return
		}
		evidence, next := s.call(ctx, id, turn, reply.Calls[0])
		if !next {
			return
		}
		messages = append(messages, raw("user", evidence))
	}
	s.finish(id, turn, "failed")
}

// heartbeat renews the turn's lease and stops the local run when another process
// cancelled or took the turn. The returned function ends it.
func (s *Service) heartbeat(ctx context.Context, id, turn string) func() {
	stopped := make(chan struct{})
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopped:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := s.update(ctx, id, turn, "reply.updated", func(*state.ChatMessage) {}); err != nil {
					s.stop(id, turn)
					return
				}
			}
		}
	}()
	return func() { close(stopped) }
}

func raw(role string, content any) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"role": role, "content": content})
	return b
}

// history builds the prompt. Tool output and attachment details travel as user-role
// data, never as system messages, so they carry no more authority than the chat itself.
func history(c state.Chat, turn string) []json.RawMessage {
	const historyBytes = 96 << 10
	const historyMessages = 32
	groups := [][]json.RawMessage{}
	total := 0
	for i := len(c.Messages) - 1; i >= 0 && len(groups) < historyMessages; i-- {
		m := c.Messages[i]
		if m.ID == turn+"-assistant" {
			continue
		}
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
		size := 0
		for _, entry := range group {
			size += len(entry)
		}
		if total+size > historyBytes {
			break
		}
		total += size
		groups = append(groups, group)
	}
	messages := []json.RawMessage{raw("system", prompts.Chat)}
	for i := len(groups) - 1; i >= 0; i-- {
		messages = append(messages, groups[i]...)
	}
	return messages
}

// respond makes one model call. Streamed text is saved at most every persistEvery;
// the final text is saved by the caller.
func (s *Service) respond(ctx context.Context, id, turn string, messages []json.RawMessage) (conversation.Reply, error) {
	saved := time.Time{}
	messages = append(append([]json.RawMessage{}, messages...), raw("system", "User details (data): "+s.Tools.UserFacts))
	request := conversation.ReplyRequest{Messages: messages, NoDispatch: true, Foreground: true, Tools: toolSchemas(), Stream: func(text string) error {
		if time.Since(saved) < persistEvery {
			return ctx.Err()
		}
		saved = time.Now()
		_, err := s.update(ctx, id, turn, "reply.updated", func(m *state.ChatMessage) { m.Text = text })
		return err
	}}
	request.RecordUsage = func(callctx context.Context, u state.Usage) error {
		// A cancelled or taken turn sends nothing. The check is a read inside the chat's transaction.
		if _, err := s.Store.UpdateChat(callctx, id, func(c *state.Chat) (string, error) {
			if !s.owns(c, turn) {
				return "", state.ErrCancelled
			}
			return "", nil
		}); err != nil {
			return err
		}
		u.InputID = turn
		return s.Store.RecordUsage(callctx, u)
	}
	reply, err := s.Model.Respond(ctx, request)
	if reply.Usage.ID != "" && reply.Usage.RequestStatus != "not_sent" {
		// The final usage must outlive a cancellation, including unknown costs.
		save, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		reply.Usage.InputID = turn
		err = errors.Join(err, s.Store.RecordUsage(save, reply.Usage))
		cancel()
	}
	return reply, err
}

// call runs one tool request. It returns the evidence for the next model step, or
// false when the turn ended.
func (s *Service) call(ctx context.Context, id, turn string, call conversation.ToolCall) (string, bool) {
	var args map[string]any
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || call.Function.Name != "upload_read" {
		s.finish(id, turn, "failed")
		return "", false
	}
	op := state.ChatOperation{OperationRecord: state.OperationRecord{ID: uuid.NewString(), CallID: call.ID, Name: call.Function.Name, EffectClass: "read", Outcome: "attempted"}}
	if _, err := s.update(ctx, id, turn, "tool.updated", func(m *state.ChatMessage) { m.Operations = append(m.Operations, op) }); err != nil {
		s.finish(id, turn, "failed")
		return "", false
	}
	var err error
	op.Result, err = s.readUpload(ctx, id, args)
	op.Outcome = "completed"
	if err != nil {
		op.Outcome = "failed"
		op.Result = map[string]any{"error": err.Error()}
	}
	op.Result = boundedResult(op.Result)
	if _, err = s.update(ctx, id, turn, "tool.updated", func(m *state.ChatMessage) { m.Operations[len(m.Operations)-1] = op }); err != nil {
		return "", false
	}
	result, _ := json.Marshal(op)
	return prompts.Runtime("chat-tool-evidence", nil) + string(result), true
}

// readUpload reads only files assigned in this chat.
func (s *Service) readUpload(ctx context.Context, id string, args map[string]any) (map[string]any, error) {
	upload, _ := args["upload_id"].(string)
	u, err := s.Store.ChatUpload(ctx, id, upload)
	if err != nil {
		return nil, state.ErrAttachmentMissing
	}
	data, err := s.Store.GetFile(ctx, uploadKey(id, upload))
	if err != nil {
		return nil, err
	}
	return agent.ReadAttachedCSV(u.Attachment, data)
}

func boundedResult(result map[string]any) map[string]any {
	b, err := json.Marshal(result)
	if err != nil || len(b) > 16<<10 {
		return map[string]any{"error": prompts.Runtime("chat-result-too-large", nil)}
	}
	return result
}

func uploadKey(chat, id string) string {
	return state.UploadKey(uuid.NewSHA1(uuid.NameSpaceOID, []byte("chat/"+chat+"/"+id)).String())
}

func (s *Service) SaveUpload(ctx context.Context, id, upload, name, declared string, data []byte) (state.Upload, error) {
	if _, err := s.Store.Chat(ctx, id); err != nil {
		return state.Upload{}, err
	}
	name = state.CleanUploadName(name)
	kind, err := state.SniffUpload(name, declared, data)
	if err != nil {
		return state.Upload{}, err
	}
	sum := sha256.Sum256(data)
	u := state.Upload{Attachment: state.Attachment{ID: upload, Name: name, MediaType: kind, Size: int64(len(data))}, SHA256: hex.EncodeToString(sum[:]), CreatedAt: time.Now().UTC()}
	err = s.Store.PutFile(ctx, uploadKey(id, upload), kind, data)
	if errors.Is(err, state.ErrFileExists) {
		saved, e := s.Store.GetFile(ctx, uploadKey(id, upload))
		if e != nil {
			return u, e
		}
		if !bytes.Equal(saved, data) {
			return u, state.ErrDuplicate
		}
	} else if err != nil {
		return u, err
	}
	return s.Store.CreateChatUpload(ctx, id, u)
}

func (s *Service) ReadUpload(ctx context.Context, id, upload string) (state.Upload, []byte, error) {
	u, err := s.Store.ChatUpload(ctx, id, upload)
	if err != nil {
		return u, nil, err
	}
	data, err := s.Store.GetFile(ctx, uploadKey(id, upload))
	return u, data, err
}

func toolSchemas() json.RawMessage {
	// Same argument names and validation as the shared worker tool implementations.
	schemas := []any{}
	for _, t := range []struct {
		Name string
		Keys []string
	}{{"upload_read", []string{"upload_id"}}} {
		props := map[string]any{}
		for _, key := range t.Keys {
			props[key] = map[string]any{"type": "string"}
		}
		schemas = append(schemas, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "parameters": map[string]any{"type": "object", "properties": props, "required": t.Keys, "additionalProperties": false}}})
	}
	b, _ := json.Marshal(schemas)
	return b
}
