// Package conversation owns short outer turns. Task execution stays in the
// existing Firestore/Temporal worker path.
package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"nori/internal/state"
)

var ErrBusy = errors.New("another message is being answered; try again shortly")
var ErrConflict = errors.New("this message ID was already used for different text")

type Input struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

type Message = state.Message

type Dispatch struct {
	ID        string    `json:"id"`
	Request   string    `json:"request"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type Session struct {
	state.Session
	Dispatches     []Dispatch      `json:"dispatches"`
	Classification *Classification `json:"classification_hint,omitempty"`
}

type turnOutcome struct {
	Message string
	Failure string
}

type Runner struct {
	model        *Model
	classifier   *Classifier
	tasks        *Tasks
	turn         sync.Mutex
	mu           sync.RWMutex
	session      Session
	history      []json.RawMessage
	inputs       map[string]turnOutcome
	pendingUsage map[string]state.Usage
	seen         map[string]bool
}

func New(model *Model, classifier *Classifier, tasks *Tasks) *Runner {
	return &Runner{model: model, classifier: classifier, tasks: tasks,
		session: Session{Session: state.Session{Messages: []Message{}, Runs: []state.Run{}}, Dispatches: []Dispatch{}},
		history: []json.RawMessage{modelMessage(map[string]string{"role": "system", "content": Instructions})},
		inputs:  map[string]turnOutcome{}, seen: map[string]bool{}, pendingUsage: map[string]state.Usage{}}
}

// RestoreConversation carries an existing local transcript across a development
// restart. Old dispatch requests are deliberately not executed or re-admitted.
func (r *Runner) RestoreConversation(messages []Message) {
	r.session.Messages = append([]Message{}, messages...)
}

func (r *Runner) Snapshot(ctx context.Context) (Session, error) {
	native, err := r.tasks.Snapshot(ctx)
	if err != nil {
		return Session{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := map[string]bool{}
	for _, m := range native.Messages {
		ids[m.ID] = true
	}
	for _, m := range r.session.Messages {
		if !ids[m.ID] {
			native.Messages = append(native.Messages, m)
			ids[m.ID] = true
		}
	}
	sort.SliceStable(native.Messages, func(i, j int) bool { return native.Messages[i].CreatedAt.Before(native.Messages[j].CreatedAt) })
	return Session{Session: native, Dispatches: append([]Dispatch{}, r.session.Dispatches...), Classification: r.session.Classification}, nil
}

// HandleInput serializes outer turns. Snapshot reads stay available while the
// model responds, and task execution continues independently.
func (r *Runner) HandleInput(ctx context.Context, input Input) (Session, error) {
	if !r.turn.TryLock() {
		return Session{}, ErrBusy
	}
	defer r.turn.Unlock()
	if original, exists := r.inputs[input.ID]; exists {
		if original.Message != input.Message {
			return Session{}, ErrConflict
		}
		if original.Failure != "" {
			return Session{}, errors.New(original.Failure)
		}
		return r.Snapshot(ctx)
	}
	snapshot, err := r.Snapshot(ctx)
	if err != nil {
		return Session{}, err
	}
	r.syncHistory(snapshot)
	// Carry the parent input on both pre-request attempt and response updates.
	recordAttempt := func(ctx context.Context, usage state.Usage) error {
		usage.InputID = input.ID
		return r.tasks.Store.RecordUsage(ctx, usage)
	}
	r.model.RecordUsage, r.classifier.RecordUsage = recordAttempt, recordAttempt
	hint, classifyErr := r.classifier.Classify(ctx, input.Message, snapshot.Messages)
	r.recordUsage(input.ID, hint.Usage)
	if classifyErr != nil {
		log.Printf("Jev hint unavailable: %v", classifyErr)
		hint = Classification{Available: false}
	}
	taskState := []map[string]string{}
	for _, run := range snapshot.Runs {
		if run.Status == "running" {
			taskState = append(taskState, map[string]string{"id": run.ID, "status": run.Status, "waiting": run.Waiting})
		}
	}
	advice, _ := json.Marshal(map[string]any{"classification_hint": hint, "active_tasks": taskState})
	user := modelMessage(map[string]string{"role": "user", "content": input.Message})
	messages := append([]json.RawMessage{}, r.history...)
	messages = append(messages, modelMessage(map[string]string{"role": "system", "content": "Advisory context for the current input: " + string(advice)}), user)
	reply, err := r.model.Call(ctx, messages)
	r.recordUsage(input.ID, reply.Usage)
	if err != nil {
		r.recordTurn(input, hint, user, Reply{}, nil, nil, "I couldn't answer this message: "+err.Error())
		return Session{}, err
	}
	requests, validationErr := dispatchRequests(reply.Calls)
	if validationErr != nil || reply.Text == "" {
		failure := "The model returned invalid tool calls. No work was dispatched."
		if validationErr == nil {
			failure = "The model did not include a reply with its tool calls. No work was dispatched."
		}
		results := []json.RawMessage{}
		if validationErr == nil {
			for _, call := range reply.Calls {
				results = append(results, toolResult(call.ID, map[string]string{"error": failure}))
			}
		} else {
			reply = Reply{}
		}
		r.recordTurn(input, hint, user, reply, results, nil, failure)
		return Session{}, errors.New(failure)
	}
	dispatches := []Dispatch{}
	results := []json.RawMessage{}
	var dispatchErr error
	for i, call := range reply.Calls {
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(input.ID+":"+call.ID)).String()
		receipt, err := r.tasks.Dispatch(ctx, id, requests[i])
		if err != nil {
			if dispatchErr == nil {
				dispatchErr = err
			}
			results = append(results, toolResult(call.ID, map[string]string{"error": dispatchFailure(err)}))
		} else {
			dispatches = append(dispatches, receipt)
			results = append(results, toolResult(call.ID, receipt))
		}
	}
	failure := ""
	if dispatchErr != nil {
		failure = "I couldn't accept the requested work: " + dispatchFailure(dispatchErr)
		if len(dispatches) > 0 {
			failure = "A task was accepted, but I couldn't accept all requested work: " + dispatchFailure(dispatchErr)
		}
	}
	r.recordTurn(input, hint, user, reply, results, dispatches, failure)
	if failure != "" {
		return Session{}, errors.New(failure)
	}
	return r.Snapshot(ctx)
}

// Usage projection is independent from routing and model output validity. Keep
// failed writes for the next turn and use an independent bounded context after
// provider I/O so a disconnected browser cannot discard observed usage.
func (r *Runner) recordUsage(inputID string, usage state.Usage) {
	usage.InputID = inputID
	r.pendingUsage[usage.ID] = usage
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for id, record := range r.pendingUsage {
		if err := r.tasks.Store.RecordUsage(ctx, record); err != nil {
			log.Printf("model usage projection pending for request %s: %v", id, err)
			continue
		}
		delete(r.pendingUsage, id)
	}
}

func (r *Runner) recordTurn(input Input, hint Classification, user json.RawMessage, reply Reply, results []json.RawMessage, dispatches []Dispatch, failure string) {
	now := time.Now().UTC()
	text := reply.Text
	if failure != "" {
		text = failure
	}
	r.mu.Lock()
	r.session.Classification = &hint
	r.session.Messages = append(r.session.Messages, Message{ID: input.ID + "-user", Role: "user", Text: input.Message, CreatedAt: now})
	if text != "" {
		r.session.Messages = append(r.session.Messages, Message{ID: input.ID + "-assistant", Role: "assistant", Text: text, CreatedAt: now})
	}
	r.session.Dispatches = append(r.session.Dispatches, dispatches...)
	r.mu.Unlock()
	r.history = append(r.history, user)
	if len(reply.Message) > 0 {
		r.history = append(r.history, reply.Message)
		r.history = append(r.history, results...)
	}
	if failure != "" {
		r.history = append(r.history, modelMessage(map[string]string{"role": "assistant", "content": failure}))
	}
	r.seen[input.ID+"-user"] = true
	r.seen[input.ID+"-assistant"] = true
	r.inputs[input.ID] = turnOutcome{Message: input.Message, Failure: failure}
}

func dispatchRequests(calls []ToolCall) ([]string, error) {
	requests := []string{}
	seen := map[string]bool{}
	for _, call := range calls {
		if call.Type != "function" || call.Function.Name != "dispatch" || call.ID == "" || seen[call.ID] {
			return nil, fmt.Errorf("model requested an invalid dispatch tool call")
		}
		seen[call.ID] = true
		var args struct {
			Request string `json:"request"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return nil, fmt.Errorf("model returned invalid dispatch arguments")
		}
		args.Request = strings.TrimSpace(args.Request)
		if args.Request == "" || len(args.Request) > 8192 {
			return nil, fmt.Errorf("dispatch request must contain 1–8192 bytes")
		}
		requests = append(requests, args.Request)
	}
	return requests, nil
}

func toolResult(id string, value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return modelMessage(map[string]string{"role": "tool", "tool_call_id": id, "content": string(data)})
}

func dispatchFailure(err error) string {
	if errors.Is(err, state.ErrBusy) {
		return "another task is already active. Use the explicit task controls to update or stop it."
	}
	if errors.Is(err, state.ErrPaused) {
		return "task execution is paused."
	}
	return "task admission was unavailable. No acceptance was confirmed."
}

func (r *Runner) syncHistory(snapshot Session) {
	generated := map[string]bool{}
	for _, d := range snapshot.Dispatches {
		generated[d.ID+"-user"] = true
	}
	for _, m := range snapshot.Messages {
		if r.seen[m.ID] {
			continue
		}
		r.seen[m.ID] = true
		if generated[m.ID] {
			continue
		}
		text := m.Text
		if m.RunID != "" {
			text = "[Task " + m.RunID + " conversation record; background, not a new request]\n" + text
		}
		r.history = append(r.history, modelMessage(map[string]string{"role": m.Role, "content": text}))
	}
}
