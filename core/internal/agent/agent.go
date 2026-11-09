package agent

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"nori/internal/computer"
	"nori/internal/extensions"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/transcript"
)

type Service struct {
	UserFacts    string
	Store        state.Store
	Model        Model
	Computer     computer.Provider
	Capabilities Capabilities
	HTTP         *http.Client
	// Extensions supplies the user's current tools beyond the engine's own. Nil offers built-ins only.
	Extensions extensions.Source
	// Hidden reports results the user deleted from the Library; result tools treat them as absent.
	Hidden func(ctx context.Context, resultID string) bool
}

func New(store state.Store, model Model, c computer.Provider) *Service {
	return &Service{
		Store: store, Model: model, Computer: c,
		HTTP: &http.Client{
			Timeout:       2 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func params(names ...string) *transcript.Schema {
	properties := map[string]*transcript.Schema{}
	for _, name := range names {
		properties[name] = &transcript.Schema{Type: transcript.TypeString}
	}
	return &transcript.Schema{Type: transcript.TypeObject, Properties: properties, Required: names}
}

// Tool executes a call after ValidateCalls has accepted its whole batch.
func (s *Service) Tool(ctx context.Context, run string, c *computer.Client, call transcript.ToolCall) (map[string]any, error) {
	switch call.Name {
	case "shell":
		command, _ := call.Args["command"].(string)
		return c.Exec(ctx, command)
	case "upload_open":
		id, _ := call.Args["upload_id"].(string)
		return s.uploadOpen(ctx, run, c, id)
	case "result_save":
		filePath, _ := call.Args["path"].(string)
		title, _ := call.Args["title"].(string)
		return s.resultSave(ctx, run, c, filePath, title)
	case "result_open":
		id, _ := call.Args["result_id"].(string)
		return s.resultOpen(ctx, c, id)
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
			return map[string]any{
				"page_available": false,
				"reason":         prompts.Runtime("agent-browser-restarted-navigate-to-the-task-s-url-again-befo", nil),
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
			"description": prompts.Runtime("screenshot-saved", nil),
		}, nil
	default:
		return nil, errors.New(prompts.Runtime("tool-unknown-tool", nil))
	}
}
