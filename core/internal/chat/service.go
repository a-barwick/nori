package chat

import (
	"context"
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
	"nori/internal/usage"
)

const (
	// persistEvery bounds how often streamed text is saved. Each save is one
	// database transaction and one event, so saving every token would not scale.
	persistEvery = 400 * time.Millisecond
	turnLease    = 10 * time.Second
	maxSteps     = 8
)

type chatTurn struct{ chatID, turnID string }

type Store interface {
	state.ChatDatabase
	state.Artifacts
	usage.Meter
}

type Service struct {
	Store     Store
	model     conversation.ReplyModel
	userFacts string
	owner     string
	mu        sync.Mutex
	running   map[chatTurn]context.CancelFunc
	runEnded  chan struct{}
	closed    bool
	wg        sync.WaitGroup
}

func New(store Store, model conversation.ReplyModel, userFacts string) *Service {
	return &Service{
		Store: store, model: model, userFacts: userFacts, owner: uuid.NewString(),
		running: map[chatTurn]context.CancelFunc{}, runEnded: make(chan struct{}),
	}
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
		userMessageID := state.UserMessageID(input)
		for _, m := range c.Messages {
			if m.ID == userMessageID {
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
		c.Messages = append(c.Messages, state.ChatMessage{ID: userMessageID, Role: "user", Text: text, Status: "completed", Attachments: assigned}, state.ChatMessage{ID: state.AssistantMessageID(input), Role: "assistant", Status: "streaming"})
		return "input.accepted", nil
	}, state.UserMessageID(input))
}

func (s *Service) scan(ctx context.Context) error {
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
	if s.closed {
		return
	}
	key := chatTurn{id, turn}
	if _, exists := s.running[key]; exists {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.running[key] = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			cancel()
			s.mu.Lock()
			delete(s.running, key)
			close(s.runEnded)
			s.runEnded = make(chan struct{})
			s.mu.Unlock()
		}()
		s.run(ctx, id, turn)
	}()
}

func (s *Service) stop(id, turn string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel := s.running[chatTurn{id, turn}]; cancel != nil {
		cancel()
	}
}

// Process runs the hosted wake. It returns ErrConversationBusy while any turn
// still needs work, so the durable queue retries the wake.
func (s *Service) Process(ctx context.Context) error {
	if err := s.scan(ctx); err != nil {
		return err
	}
	scan := time.NewTicker(time.Second)
	defer scan.Stop()
	for {
		s.mu.Lock()
		n := len(s.running)
		runEnded := s.runEnded
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
			if err := s.scan(ctx); err != nil {
				return err
			}
		case <-runEnded:
		}
	}
}

func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	for _, cancel := range s.running {
		cancel()
	}
	s.mu.Unlock()
	// Detached usage recording can still be using the store.
	s.wg.Wait()
}

func (s *Service) owns(c *state.Chat, turn string) bool {
	return c.ActiveTurn != nil && *c.ActiveTurn == turn && c.Started == s.owner && c.LeaseUntil.After(time.Now())
}

func replyMessage(c *state.Chat, turn string) (*state.ChatMessage, error) {
	id := state.AssistantMessageID(turn)
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i].ID == id {
			return &c.Messages[i], nil
		}
	}
	return nil, state.ErrNotFound
}

func endTurn(c *state.Chat, reply *state.ChatMessage, status string) string {
	reply.Status = status
	c.ActiveTurn = nil
	c.Started = ""
	c.LeaseUntil = time.Time{}
	return "turn." + status
}

func (s *Service) update(ctx context.Context, id, turn, kind string, f func(*state.ChatMessage)) (state.Chat, error) {
	return s.Store.UpdateChat(ctx, id, func(c *state.Chat) (string, error) {
		if !s.owns(c, turn) {
			return "", state.ErrCancelled
		}
		reply, err := replyMessage(c, turn)
		if err != nil {
			return "", err
		}
		f(reply)
		c.LeaseUntil = time.Now().Add(turnLease)
		return kind, nil
	}, state.AssistantMessageID(turn))
}

func (s *Service) finish(id, turn, status string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.Store.UpdateChat(ctx, id, func(c *state.Chat) (string, error) {
		if !s.owns(c, turn) {
			return "", nil
		}
		m, err := replyMessage(c, turn)
		if err != nil {
			return "", err
		}
		if status == "failed" && m.Text == "" {
			m.Text = prompts.Runtime("chat-interrupted", nil)
		}
		return endTurn(c, m, status), nil
	}, state.AssistantMessageID(turn))
	if err != nil {
		log.Printf("chat %s: saving the %s status of turn %s failed: %v", id, status, turn, err)
	}
}

func (s *Service) Cancel(ctx context.Context, id, turn string) (state.Chat, error) {
	c, err := s.Store.UpdateChat(ctx, id, func(c *state.Chat) (string, error) {
		if c.ActiveTurn == nil {
			_, err := replyMessage(c, turn)
			return "", err
		}
		if *c.ActiveTurn != turn {
			return "", state.ErrQuestionStale
		}
		m, err := replyMessage(c, turn)
		if err != nil {
			return "", err
		}
		return endTurn(c, m, "cancelled"), nil
	}, state.AssistantMessageID(turn))
	if err == nil {
		s.stop(id, turn)
	}
	return c, err
}

func (s *Service) claim(ctx context.Context, id, turn string) (state.Chat, error) {
	return s.Store.UpdateChat(ctx, id, func(c *state.Chat) (string, error) {
		if c.ActiveTurn == nil || *c.ActiveTurn != turn {
			return "", state.ErrCancelled
		}
		m, err := replyMessage(c, turn)
		if err != nil {
			return "", err
		}
		if c.Started != "" {
			if c.LeaseUntil.After(time.Now()) {
				return "", state.ErrConversationBusy
			}
			// Another process started this reply and stopped. Its provider call is never repeated.
			return endTurn(c, m, "failed"), nil
		}
		c.Started = s.owner
		c.LeaseUntil = time.Now().Add(turnLease)
		return "reply.updated", nil
	}, state.AssistantMessageID(turn))
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

func (s *Service) heartbeat(ctx context.Context, id, turn string) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
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
	return func() { cancel(); <-done }
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
		if m.ID == state.AssistantMessageID(turn) {
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

func (s *Service) respond(ctx context.Context, id, turn string, messages []json.RawMessage) (conversation.Reply, error) {
	saved := time.Time{}
	messages = append(append([]json.RawMessage{}, messages...), raw("system", "User details (data): "+s.userFacts))
	request := conversation.ReplyRequest{Messages: messages, NoDispatch: true, Foreground: true, Tools: uploadToolSchema, Stream: func(text string) error {
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
	reply, err := s.model.Respond(ctx, request)
	if reply.Usage.ID != "" && reply.Usage.RequestStatus != "not_sent" {
		// The final usage must outlive a cancellation, including unknown costs.
		save, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		reply.Usage.InputID = turn
		err = errors.Join(err, s.Store.RecordUsage(save, reply.Usage))
		cancel()
	}
	return reply, err
}

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
	u, err := state.SaveUploadBytes(ctx, s.Store, uploadKey(id, upload), upload, name, declared, data)
	if err != nil {
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

var uploadToolSchema = json.RawMessage(`[{"type":"function","function":{"name":"upload_read","parameters":{"type":"object","properties":{"upload_id":{"type":"string"}},"required":["upload_id"],"additionalProperties":false}}}]`)
