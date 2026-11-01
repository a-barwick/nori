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
	Store     ScreenshotStore
	Key       string
	Client    *http.Client
	ModelName string
	Computer  computer.Config
	// Capabilities is set once by the worker before it starts polling.
	Capabilities Capabilities
}

// ScreenshotStore adds reading a task's saved screenshots to the task's records.
type ScreenshotStore interface {
	state.Store
	GetPNG(ctx context.Context, run, id string) ([]byte, error)
}

func New(store ScreenshotStore, key string, name string, c computer.Config) *Service {
	return &Service{Store: store, Key: key, Client: &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, ModelName: name, Computer: c}
}

func params(names ...string) *genai.Schema {
	properties := map[string]*genai.Schema{}
	for _, name := range names {
		properties[name] = &genai.Schema{Type: genai.TypeString}
	}
	return &genai.Schema{Type: genai.TypeObject, Properties: properties, Required: names}
}

// Tool executes a call after ValidateCalls has accepted its whole batch.
func (s *Service) Tool(ctx context.Context, run string, c *computer.Client, call *genai.FunctionCall) (map[string]any, error) {
	switch call.Name {
	case "shell":
		command, _ := call.Args["command"].(string)
		return c.Exec(ctx, command)
	case "browser_navigate":
		address, _ := call.Args["url"].(string)
		return c.Navigate(ctx, address)
	case "job_start":
		command, _ := call.Args["command"].(string)
		return c.JobStart(ctx, command)
	case "job_status", "job_wait":
		id, _ := call.Args["job_id"].(string)
		return c.JobStatus(ctx, id)
	case "job_stop":
		id, _ := call.Args["job_id"].(string)
		return c.JobStop(ctx, id)
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
		return map[string]any{"run_id": run, "artifact_id": id, "description": "Private browser screenshot saved for Austin. Call screenshot_inspect with this artifact_id to read it."}, nil
	default:
		return nil, errors.New("unknown tool")
	}
}
