// Package agent holds the inner task model and tool steps. Durable
// sequencing lives in the Temporal workflow that calls them.
package agent

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"google.golang.org/genai"
	"nori/internal/computer"
	"nori/internal/state"
)

type Service struct {
	Store     state.Store
	Key       string
	Client    *http.Client
	ModelName string
	Computer  computer.Config
}

func New(store state.Store, key string, name string, c computer.Config) *Service {
	return &Service{Store: store, Key: key, Client: &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, ModelName: name, Computer: c}
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
		if errors.Is(e, computer.ErrBrowserRestarted) {
			return map[string]any{"page_available": false, "reason": "Browser restarted; navigate to the task's URL again before taking a screenshot."}, nil
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
		return map[string]any{"run_id": run, "artifact_id": id, "description": "Private browser screenshot saved for Austin; image pixels are not available to the model."}, nil
	default:
		return nil, errors.New("unknown tool")
	}
}
