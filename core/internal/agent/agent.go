// Package agent holds the inner task model contract and tool steps. Durable
// sequencing lives in the Temporal workflow that calls them.
package agent

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"nori/internal/computer"
	"nori/internal/state"
	"nori/internal/transcript"
)

type Service struct {
	Store    state.Store
	Model    Model
	Computer computer.Config
}

func New(store state.Store, model Model, c computer.Config) *Service {
	return &Service{Store: store, Model: model, Computer: c}
}

func params(name string) *transcript.Schema {
	return &transcript.Schema{
		Type:       transcript.TypeObject,
		Properties: map[string]*transcript.Schema{name: {Type: transcript.TypeString}},
		Required:   []string{name},
	}
}

// Tool executes a call after ValidateCalls has accepted its whole batch.
func (s *Service) Tool(ctx context.Context, run string, c *computer.Client, call transcript.ToolCall) (map[string]any, error) {
	switch call.Name {
	case "shell":
		command, _ := call.Args["command"].(string)
		return c.Exec(ctx, command)
	case "browser_navigate":
		address, _ := call.Args["url"].(string)
		return c.Navigate(ctx, address)
	case "browser_screenshot":
		data, e := c.Screenshot(ctx)
		if errors.Is(e, computer.ErrBrowserRestarted) {
			return map[string]any{
				"page_available": false,
				"reason":         "Browser restarted; navigate to the task's URL again before taking a screenshot.",
			}, nil
		}
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
		return map[string]any{
			"run_id":      run,
			"artifact_id": id,
			"description": "Private browser screenshot saved for Austin; image pixels are not available to the model.",
		}, nil
	default:
		return nil, errors.New("unknown tool")
	}
}
