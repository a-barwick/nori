// Package agent implements the bounded agent loop independently of HTTP hosting.
package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/genai"
	"nori/internal/computer"
	"nori/internal/state"
)

type Service struct {
	Store     state.Store
	Model     *genai.Client
	ModelName string
	Computer  computer.Config
	mu        sync.Mutex
	active    map[string]context.CancelFunc
}

func New(store state.Store, model *genai.Client, name string, c computer.Config) *Service {
	return &Service{Store: store, Model: model, ModelName: name, Computer: c, active: map[string]context.CancelFunc{}}
}
func (s *Service) Cancel(ctx context.Context, id string) (state.Run, error) {
	r, e := s.Store.RequestCancel(ctx, id)
	if e != nil {
		return r, e
	}
	s.mu.Lock()
	cancel := s.active[id]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return r, nil
}
func (s *Service) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cancel := range s.active {
		cancel()
	}
}

// Run executes inline. It returns only after model, computer work and cleanup stop.
func (s *Service) Run(parent context.Context, id, message string) (state.Run, error) {
	run, e := s.Store.Begin(parent, id, message)
	if e != nil {
		return run, e
	}
	ctx, cancel := context.WithDeadline(parent, run.Deadline)
	defer cancel()
	s.mu.Lock()
	s.active[id] = cancel
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.active, id); s.mu.Unlock() }()
	watchDone := make(chan struct{})
	watchExited := make(chan struct{})
	go func() {
		defer close(watchExited)
		ticker := time.NewTicker(750 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, err := s.Store.GetRun(ctx, id)
				if err != nil || current.CancelRequested {
					cancel()
					return
				}
				control, err := s.Store.Control(ctx)
				if err != nil {
					cancel()
					return
				}
				if _, err = control.Deadline(); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	text, computerClient, executeErr := s.execute(ctx, run)
	close(watchDone)
	<-watchExited
	// A separate, bounded cleanup context keeps IAP open long enough to prove the
	// guest stopped; execution itself never escapes the request's deadline.
	stopErr := error(nil)
	if computerClient != nil {
		// A tool may have returned a recoverable error to the model after its
		// own cleanup failed. Require the same stop acknowledgment on success.
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		stopErr = computerClient.Cancel(cleanup)
		stop()
		computerClient.Close()
	}
	finalCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	current, readErr := s.Store.GetRun(finalCtx, id)
	runStatus, detail := "completed", ""
	switch {
	case stopErr != nil:
		runStatus = "failed"
		detail = "Interrupted: computer stop could not be confirmed."
	case current.CancelRequested && readErr == nil:
		runStatus = "cancelled"
		detail = "Cancelled by Austin."
	case executeErr != nil:
		runStatus = "failed"
		detail = executeErr.Error()
	case ctx.Err() != nil:
		runStatus = "failed"
		detail = "Interrupted: request or execution deadline ended."
	case readErr != nil:
		runStatus = "failed"
		detail = "Interrupted: durable run state could not be checked."
	}
	if runStatus != "completed" {
		text = ""
	}
	finished, finishErr := s.Store.Finish(finalCtx, id, runStatus, detail, text)
	if finishErr != nil {
		return run, fmt.Errorf("could not persist terminal state: %w", finishErr)
	}
	return finished, nil
}
func (s *Service) execute(ctx context.Context, run state.Run) (string, *computer.Client, error) {
	session, e := s.Store.Session(ctx)
	if e != nil {
		return "", nil, e
	}
	contents := Context(session)
	var client *computer.Client
	toolCalls := 0
	for modelCalls := 0; modelCalls < 6; modelCalls++ {
		if e = ctx.Err(); e != nil {
			return "", client, e
		}
		if e = s.Store.Event(ctx, run.ID, state.Event{Kind: "model", Text: "Thinking"}); e != nil {
			return "", client, e
		}
		decision, e := s.Decide(ctx, contents, false)
		if e != nil {
			return "", client, e
		}
		contents = append(contents, decision.Content)
		calls := decision.Calls
		if len(calls) == 0 {
			return decision.Answer, client, nil
		}
		if toolCalls+len(calls) > 12 {
			return "", client, errors.New("tool call limit reached")
		}
		parts := []*genai.Part{}
		for _, call := range calls {
			toolCalls++
			if e = s.Store.Event(ctx, run.ID, state.Event{Kind: "tool", Text: call.Name}); e != nil {
				return "", client, e
			}
			if client == nil {
				client, e = computer.Open(ctx, s.Computer, run.ID)
				if e != nil {
					return "", client, fmt.Errorf("computer unavailable: %w", e)
				}
			}
			toolCtx, stop := context.WithTimeout(ctx, 45*time.Second)
			result, e := s.tool(toolCtx, run.ID, client, call)
			toolContextErr := toolCtx.Err()
			stop()
			if toolContextErr != nil {
				return "", client, fmt.Errorf("tool interrupted: %w", toolContextErr)
			}
			if e != nil {
				if ctx.Err() != nil {
					return "", client, ctx.Err()
				}
				return "", client, fmt.Errorf("%s failed: %w", call.Name, e)
			}
			parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: result}})
		}
		contents = append(contents, &genai.Content{Role: "user", Parts: parts})
	}
	return "", client, errors.New("model call limit reached before a final answer")
}
func params(name string) *genai.Schema {
	return &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{name: {Type: genai.TypeString}}, Required: []string{name}}
}
func (s *Service) tool(ctx context.Context, run string, c *computer.Client, call *genai.FunctionCall) (map[string]any, error) {
	switch call.Name {
	case "shell":
		command, _ := call.Args["command"].(string)
		if command == "" || len(command) > 8192 {
			return nil, errors.New("invalid command")
		}
		return c.Exec(ctx, command)
	case "browser_navigate":
		address, _ := call.Args["url"].(string)
		if len(address) > 8192 {
			return nil, errors.New("invalid URL")
		}
		return c.Navigate(ctx, address)
	case "browser_screenshot":
		data, e := c.Screenshot(ctx)
		if e != nil {
			return nil, e
		}
		id := uuid.NewString()
		if c.OperationID() != "" {
			id = uuid.NewSHA1(uuid.NameSpaceOID, []byte(c.OperationID())).String()
		}
		if e = s.Store.PutPNG(ctx, run, id, data); e != nil {
			return nil, e
		}
		if e = s.Store.Artifact(ctx, run, state.Artifact{ID: id, Kind: "screenshot"}); e != nil {
			return nil, e
		}
		return map[string]any{"run_id": run, "artifact_id": id, "description": "Private browser screenshot saved for Austin; image pixels are not available to the model."}, nil
	default:
		return nil, errors.New("unknown tool")
	}
}
