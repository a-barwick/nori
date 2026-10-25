// Package conversation is the single-process outer agent: one model call and
// local dispatch acceptance per input. It has no task executor or cloud state.
package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var ErrBusy = errors.New("another message is being answered; try again shortly")
var ErrConflict = errors.New("this message ID was already used for different text")

type Input struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

type Message struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

type Dispatch struct {
	ID        string    `json:"id"`
	Request   string    `json:"request"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type Session struct {
	Messages   []Message  `json:"messages"`
	Dispatches []Dispatch `json:"dispatches"`
}

type Runner struct {
	model   *Model
	turn    sync.Mutex
	mu      sync.RWMutex
	session Session
	history []json.RawMessage
	inputs  map[string]string
}

func New(model *Model) *Runner {
	return &Runner{model: model, session: Session{Messages: []Message{}, Dispatches: []Dispatch{}},
		history: []json.RawMessage{modelMessage(map[string]string{"role": "system", "content": Instructions})},
		inputs:  map[string]string{}}
}

func (r *Runner) Snapshot() Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return Session{Messages: append([]Message{}, r.session.Messages...), Dispatches: append([]Dispatch{}, r.session.Dispatches...)}
}

// HandleInput serializes a single conversation. Snapshot reads stay available
// while a model request is in flight. No task runs during this call.
func (r *Runner) HandleInput(ctx context.Context, input Input) (Session, error) {
	if !r.turn.TryLock() {
		return Session{}, ErrBusy
	}
	defer r.turn.Unlock()
	if original, exists := r.inputs[input.ID]; exists {
		if original != input.Message {
			return Session{}, ErrConflict
		}
		return r.Snapshot(), nil
	}
	user := modelMessage(map[string]string{"role": "user", "content": input.Message})
	messages := append(append([]json.RawMessage{}, r.history...), user)
	reply, err := r.model.Call(ctx, messages)
	if err != nil {
		return Session{}, err
	}
	dispatches, results, err := dispatch(input.ID, reply.Calls)
	if err != nil {
		return Session{}, err
	}
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	now := time.Now().UTC()
	r.mu.Lock()
	r.session.Messages = append(r.session.Messages, Message{input.ID + "-user", "user", input.Message, now})
	if reply.Text != "" {
		r.session.Messages = append(r.session.Messages, Message{input.ID + "-assistant", "assistant", reply.Text, now})
	}
	r.session.Dispatches = append(r.session.Dispatches, dispatches...)
	r.mu.Unlock()
	// Retain the complete assistant message, including provider reasoning metadata,
	// and pair every tool call with a result before the next user turn.
	r.history = append(messages, reply.Message)
	r.history = append(r.history, results...)
	r.inputs[input.ID] = input.Message
	return r.Snapshot(), nil
}

// Dispatch accepts requests into the local snapshot. A worker can replace this
// boundary later; acceptance here never means execution has started.
func dispatch(turnID string, calls []ToolCall) ([]Dispatch, []json.RawMessage, error) {
	dispatches := []Dispatch{}
	results := []json.RawMessage{}
	seen := map[string]bool{}
	for _, call := range calls {
		if call.Type != "function" || call.Function.Name != "dispatch" || call.ID == "" || seen[call.ID] {
			return nil, nil, fmt.Errorf("model requested an invalid dispatch tool call")
		}
		seen[call.ID] = true
		var args struct {
			Request string `json:"request"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return nil, nil, fmt.Errorf("model returned invalid dispatch arguments")
		}
		args.Request = strings.TrimSpace(args.Request)
		if args.Request == "" || len(args.Request) > 8192 {
			return nil, nil, fmt.Errorf("dispatch request must contain 1–8192 bytes")
		}
		d := Dispatch{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(turnID+":"+call.ID)).String(), Request: args.Request,
			Status: "accepted", CreatedAt: time.Now().UTC()}
		dispatches = append(dispatches, d)
		result, _ := json.Marshal(d)
		results = append(results, modelMessage(map[string]string{"role": "tool", "tool_call_id": call.ID, "content": string(result)}))
	}
	return dispatches, results, nil
}
