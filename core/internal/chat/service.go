package chat

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"nori/internal/agent"
	"nori/internal/contextview"
	"nori/internal/conversation"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/usage"
)

const (
	// persistEvery bounds how often streamed text is saved. Each save is one
	// database transaction and one event, so saving every token would not scale.
	persistEvery    = 400 * time.Millisecond
	turnLease       = 10 * time.Second
	maxSteps        = 8
	maxReadAttempts = 2
)

type chatTurn struct{ chatID, turnID string }

type Store interface {
	state.ChatDatabase
	state.Artifacts
	usage.Meter
}

type Service struct {
	// StartTask accepts durable task work for a chat through the assistant's task engine, with the chat as origin.
	// Nil leaves the chat without background work.
	StartTask func(ctx context.Context, commandID, chatID, messageID, request string) (state.Run, error)
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
		c.Checkpoint = &state.ChatTurnCheckpoint{}
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
	if status == "failed" && reply.Text == "" {
		reply.Text = prompts.Runtime("chat-interrupted", nil)
	}
	if op := pendingRead(c, reply); op != nil {
		op.Outcome = status
	}
	reply.Status = status
	c.ActiveTurn = nil
	c.Started = ""
	c.LeaseUntil = time.Time{}
	c.Checkpoint = nil
	return "turn." + status
}

func (s *Service) update(ctx context.Context, id, turn string, f func(*state.Chat, *state.ChatMessage) (string, error)) (state.Chat, error) {
	return s.Store.UpdateChat(ctx, id, func(c *state.Chat) (string, error) {
		if !s.owns(c, turn) {
			return "", state.ErrCancelled
		}
		reply, err := replyMessage(c, turn)
		if err != nil {
			return "", err
		}
		c.LeaseUntil = time.Now().Add(turnLease)
		return f(c, reply)
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
		if c.Started != "" && c.LeaseUntil.After(time.Now()) {
			return "", state.ErrConversationBusy
		}
		// Legacy claimed turns have no checkpoint. A pending model request in
		// either format may have been billed, so settle it without a resend.
		if (c.Checkpoint == nil && c.Started != "") || (c.Checkpoint != nil && c.Checkpoint.ModelPending) {
			return endTurn(c, m, "failed"), nil
		}
		if c.Checkpoint == nil {
			c.Checkpoint = &state.ChatTurnCheckpoint{}
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
	for c.ActiveTurn != nil && ctx.Err() == nil {
		switch {
		case c.Checkpoint.Read != nil:
			c, err = s.runRead(ctx, id, turn)
		case c.Checkpoint.Models >= maxSteps:
			s.finish(id, turn, "failed")
			return
		default:
			c, err = s.generate(ctx, id, turn)
		}
		if err != nil {
			if errors.Is(err, state.ErrPaused) || errors.Is(err, state.ErrChatMessageTooLarge) {
				s.finish(id, turn, "failed")
			}
			return
		}
	}
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
				if _, err := s.update(ctx, id, turn, func(*state.Chat, *state.ChatMessage) (string, error) {
					return "reply.updated", nil
				}); err != nil {
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

func (s *Service) respond(ctx context.Context, id, turn string, messages []json.RawMessage) (conversation.Reply, error) {
	saved := time.Time{}
	messages = append(append([]json.RawMessage{}, messages...), raw("system", "User details (data): "+s.userFacts))
	request := conversation.ReplyRequest{Messages: messages, NoDispatch: true, Foreground: true, Tools: chatToolSchema, Stream: func(text string) error {
		if time.Since(saved) < persistEvery {
			return ctx.Err()
		}
		saved = time.Now()
		_, err := s.update(ctx, id, turn, func(_ *state.Chat, m *state.ChatMessage) (string, error) {
			m.Text = text
			return "reply.updated", nil
		})
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

func (s *Service) generate(ctx context.Context, id, turn string) (state.Chat, error) {
	c, messages, err := s.prepareContext(ctx, id, turn)
	if err != nil {
		if errors.Is(err, contextview.ErrOverflow) {
			s.contextFailure(id, turn)
		} else if ctx.Err() == nil {
			s.finish(id, turn, "failed")
		}
		return c, err
	}
	c, err = s.update(ctx, id, turn, func(c *state.Chat, _ *state.ChatMessage) (string, error) {
		c.Checkpoint.Models++
		c.Checkpoint.ModelPending = true
		return "reply.updated", nil
	})
	if err != nil {
		return c, err
	}
	reply, err := s.respond(ctx, id, turn, messages)
	if errors.Is(err, contextview.ErrOverflow) && !c.Checkpoint.ContextReduced && ctx.Err() == nil {
		return s.update(ctx, id, turn, func(c *state.Chat, m *state.ChatMessage) (string, error) {
			c.Checkpoint.ModelPending = false
			c.Checkpoint.ContextReduced = true
			c.Checkpoint.RejectedContextBytes = rawSize(messages)
			m.Text = ""
			return "reply.updated", nil
		})
	}
	if err != nil {
		if ctx.Err() == nil {
			if errors.Is(err, contextview.ErrOverflow) {
				s.contextFailure(id, turn)
			} else {
				s.finish(id, turn, "failed")
			}
		}
		return c, err
	}
	operationID := uuid.NewString()
	if len(reply.Calls) == 1 && reply.Calls[0].Function.Name == "start_task" {
		return s.startTask(ctx, id, turn, c, reply.Calls[0].ID, reply.Calls[0].Function.Arguments)
	}
	return s.update(ctx, id, turn, func(c *state.Chat, m *state.ChatMessage) (string, error) {
		if len(reply.Calls) == 0 {
			m.Text = reply.Text
			return endTurn(c, m, "completed"), nil
		}
		if len(reply.Calls) != 1 {
			return endTurn(c, m, "failed"), nil
		}
		call := reply.Calls[0]
		var args struct {
			UploadID  string `json:"upload_id"`
			Before    int64  `json:"before"`
			MessageID string `json:"message_id"`
			Offset    int    `json:"offset"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return endTurn(c, m, "failed"), nil
		}
		if !state.ValidUploadID(args.UploadID) {
			args.UploadID = ""
		}
		c.Checkpoint.ModelPending = false
		c.Checkpoint.RejectedContextBytes = 0
		if call.Function.Name != "upload_read" && call.Function.Name != "history_read" {
			return endTurn(c, m, "failed"), nil
		}
		if args.Before < 0 || len(args.MessageID) > 128 {
			return endTurn(c, m, "failed"), nil
		}
		c.Checkpoint.Read = &state.ChatReadCheckpoint{OperationID: operationID, UploadID: args.UploadID, Name: call.Function.Name, Before: args.Before, MessageID: args.MessageID, Offset: args.Offset}
		m.Operations = append(m.Operations, state.ChatOperation{Arguments: call.Function.Arguments, OperationRecord: state.OperationRecord{
			ID: operationID, CallID: call.ID, Name: call.Function.Name, EffectClass: "read", Outcome: "pending",
		}})
		return "tool.updated", nil
	})
}

func pendingRead(c *state.Chat, m *state.ChatMessage) *state.ChatOperation {
	if c.Checkpoint == nil || c.Checkpoint.Read == nil || len(m.Operations) == 0 {
		return nil
	}
	op := &m.Operations[len(m.Operations)-1]
	if op.ID != c.Checkpoint.Read.OperationID || (op.Name != "upload_read" && op.Name != "history_read") || op.EffectClass != "read" ||
		(op.Outcome != "pending" && op.Outcome != "attempted") {
		return nil
	}
	return op
}

func (s *Service) runRead(ctx context.Context, id, turn string) (state.Chat, error) {
	c, err := s.update(ctx, id, turn, func(c *state.Chat, m *state.ChatMessage) (string, error) {
		op := pendingRead(c, m)
		if op == nil {
			return endTurn(c, m, "failed"), nil
		}
		if op.Attempt >= maxReadAttempts {
			op.Outcome = "failed"
			op.Result = map[string]any{"error": prompts.Runtime("chat-read-interrupted", nil)}
			c.Checkpoint.Read = nil
		} else {
			op.Attempt++
			op.Outcome = "attempted"
		}
		return "tool.updated", nil
	})
	if err != nil || c.Checkpoint == nil || c.Checkpoint.Read == nil {
		return c, err
	}
	readCtx, stop := context.WithTimeout(ctx, 30*time.Second)
	var result map[string]any
	var readErr error
	if c.Checkpoint.Read.Name == "history_read" {
		result, readErr = s.readHistory(readCtx, id, turn, *c.Checkpoint.Read)
	} else {
		result, readErr = s.readUpload(readCtx, id, c.Checkpoint.Read.UploadID)
	}
	stop()
	if ctx.Err() != nil {
		return c, ctx.Err()
	}
	return s.update(ctx, id, turn, func(c *state.Chat, m *state.ChatMessage) (string, error) {
		op := pendingRead(c, m)
		if op == nil {
			return endTurn(c, m, "failed"), nil
		}
		op.Outcome = "completed"
		op.Result = boundedResult(result)
		if readErr != nil {
			op.Outcome = "failed"
			op.Result = boundedResult(map[string]any{"error": readErr.Error()})
		}
		c.Checkpoint.Read = nil
		return "tool.updated", nil
	})
}

func (s *Service) readUpload(ctx context.Context, id, upload string) (map[string]any, error) {
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

var chatToolSchema = json.RawMessage(prompts.ChatTools)

// startTask hands a chat request to the durable task engine. The same tools, authority and operation records as the
// main assistant apply; the chat records the task as a submitted operation and its result is appended when it ends.
func (s *Service) startTask(ctx context.Context, id, turn string, c state.Chat, callID, arguments string) (state.Chat, error) {
	var args struct {
		Request string `json:"request"`
	}
	if s.StartTask == nil || json.Unmarshal([]byte(arguments), &args) != nil || strings.TrimSpace(args.Request) == "" || len(args.Request) > state.MaxMessageBytes-16 {
		return s.update(ctx, id, turn, func(c *state.Chat, m *state.ChatMessage) (string, error) {
			m.Text = "Background work is not available here right now."
			return endTurn(c, m, "failed"), nil
		})
	}
	messageID := ""
	if len(c.Messages) > 0 {
		messageID = c.Messages[len(c.Messages)-1].ID
	}
	commandID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("chat-task:"+id+":"+turn+":"+callID)).String()
	run, err := s.StartTask(ctx, commandID, id, messageID, args.Request)
	if err != nil && !errors.Is(err, state.ErrDuplicate) {
		return s.update(ctx, id, turn, func(c *state.Chat, m *state.ChatMessage) (string, error) {
			m.Text = "The task could not be started: " + err.Error()
			return endTurn(c, m, "failed"), nil
		})
	}
	taskID := run.ID
	if taskID == "" {
		taskID = commandID
	}
	return s.update(ctx, id, turn, func(c *state.Chat, m *state.ChatMessage) (string, error) {
		c.Checkpoint.ModelPending = false
		m.Operations = append(m.Operations, state.ChatOperation{Arguments: arguments, Result: map[string]any{"task_id": taskID},
			OperationRecord: state.OperationRecord{ID: "chat-task-" + commandID, CallID: callID, Name: "start_task", EffectClass: "control", Outcome: "submitted", Attempt: 1}})
		m.Text = "I started this as a task. Its progress is in Work, and its result will appear here."
		return endTurn(c, m, "completed"), nil
	})
}

// DeliverTasks appends the results of finished chat tasks to the chats that started them, once each.
func (s *Service) DeliverTasks(ctx context.Context, run func(context.Context, string) (state.Run, error)) error {
	summaries, err := s.Store.ChatSummaries(ctx)
	if err != nil {
		return err
	}
	for _, summary := range summaries {
		c, err := s.Store.Chat(ctx, summary.ID)
		if err != nil {
			continue
		}
		for _, m := range c.Messages {
			for _, op := range m.Operations {
				if op.Name != "start_task" || op.Outcome != "submitted" {
					continue
				}
				taskID, _ := op.Result["task_id"].(string)
				task, err := run(ctx, taskID)
				if err != nil || task.Status == state.RunRunning {
					continue
				}
				text := task.Result
				switch task.Status {
				case state.RunFailed:
					text = "The task did not finish: " + task.Error
				case state.RunCancelled:
					text = "The task was stopped."
				}
				messageID, opID := m.ID, op.ID
				_, _ = s.Store.UpdateChat(ctx, summary.ID, func(c *state.Chat) (string, error) {
					for i := range c.Messages {
						if c.Messages[i].ID != messageID {
							continue
						}
						for j := range c.Messages[i].Operations {
							o := &c.Messages[i].Operations[j]
							if o.ID == opID && o.Outcome == "submitted" {
								o.Outcome = "confirmed"
								c.Messages = append(c.Messages, state.ChatMessage{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("chat-task-result:"+opID)).String(),
									Role: "assistant", Text: text, Status: "completed"})
								return "reply.updated", nil
							}
						}
					}
					return "", nil
				}, messageID)
			}
		}
	}
	return nil
}
