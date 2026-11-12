package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"nori/internal/clientauth"
	"nori/internal/permissions"
	"nori/internal/scheduler"
	"nori/internal/state"
	"nori/internal/users"
)

// The Nori MCP endpoint lets an authorized external MCP client use the user's Nori through the same application
// services as the app. Clients authenticate with a scoped token the user created; a token names its user, and its
// hash is checked in that user's records. A protocol session is never a Nori identity.

// noriMCP serves the endpoint for an authenticated client token.
func (a *Assistant) noriMCP(w http.ResponseWriter, r *http.Request) {
	token := clientToken(r)
	credential, ok := clientauth.Parse(token)
	if !ok || credential.Owner != users.ID(r.Context()) {
		clientUnauthorized(w)
		return
	}
	client, err := a.authenticate(r.Context(), credential)
	if err != nil {
		clientUnauthorized(w)
		return
	}
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return a.noriServer(client.Scopes) },
		&sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	handler.ServeHTTP(w, r)
}

func textResult(value any) *sdk.CallToolResult {
	data, _ := json.MarshalIndent(value, "", " ")
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(data)}}, StructuredContent: json.RawMessage(data)}
}

func errorResult(err error) *sdk.CallToolResult {
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: err.Error()}}}
}

type mcpArgs map[string]any

func (m mcpArgs) str(key string) string { v, _ := m[key].(string); return v }

// mcpServer builds the tool surface for one request. Read tools need the read scope; mutate tools need mutate and
// take a client request_id, so a retried call returns the same receipt.
func (a *Assistant) noriServer(scopes []string) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "nori", Version: AssistantContract}, nil)
	add := func(name, description string, schema map[string]any, scope string, run func(ctx context.Context, args mcpArgs) (any, error)) {
		if !slices.Contains(scopes, scope) {
			return
		}
		server.AddTool(&sdk.Tool{Name: name, Description: description, InputSchema: schema}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			args := mcpArgs{}
			if len(req.Params.Arguments) > 0 {
				if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
					return errorResult(errors.New("arguments must be a JSON object")), nil
				}
			}
			if scope == "mutate" && !validID(args.str("request_id")) {
				return errorResult(errors.New("mutations need a request_id UUID")), nil
			}
			out, err := run(ctx, args)
			if err != nil {
				return errorResult(err), nil
			}
			return textResult(out), nil
		})
	}
	object := func(props map[string]any, required ...string) map[string]any {
		if required == nil {
			required = []string{}
		}
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	str := map[string]any{"type": "string"}
	add("read_search", "Search the user's Library, conversation, tasks, goals and routines.", object(map[string]any{"query": str}, "query"), "read",
		func(ctx context.Context, args mcpArgs) (any, error) {
			return a.searchHits(ctx, args.str("query"))
		})
	add("read_library_item", "Read one Library item and its text.", object(map[string]any{"id": str}, "id"), "read",
		func(ctx context.Context, args mcpArgs) (any, error) {
			item, err := a.libraryEntry(ctx, args.str("id"))
			if err != nil {
				return nil, err
			}
			result, _ := a.Store.GetResult(ctx, item.ID)
			return map[string]any{"item": item, "text": clip(result.Text, 32<<10)}, nil
		})
	add("read_work", "List the user's tasks by bucket (needs_you, working, finished).", object(map[string]any{"bucket": str}), "read",
		func(ctx context.Context, args mcpArgs) (any, error) {
			items, err := a.workItems(ctx)
			if err != nil {
				return nil, err
			}
			out := []workItem{}
			for _, item := range items {
				if b := args.str("bucket"); b == "" || item.Bucket == b {
					out = append(out, item)
				}
			}
			return out, nil
		})
	add("read_task", "Read one task with its operations and pending questions.", object(map[string]any{"task_id": str}, "task_id"), "read",
		func(ctx context.Context, args mcpArgs) (any, error) { return a.detail(ctx, args.str("task_id")) })
	add("read_interactions", "List pending questions and approvals.", object(map[string]any{}), "read",
		func(ctx context.Context, args mcpArgs) (any, error) {
			items, _, err := a.Permissions.Interactions(ctx, "pending", "", "", 100)
			return items, err
		})
	if a.Scheduler != nil {
		add("read_goals", "List the user's goals.", object(map[string]any{}), "read",
			func(ctx context.Context, args mcpArgs) (any, error) { return a.Scheduler.Goals(ctx, "") })
		add("read_routines", "List the user's reminders and routines.", object(map[string]any{}), "read",
			func(ctx context.Context, args mcpArgs) (any, error) { return a.Scheduler.List(ctx, "") })
		add("mutate_routine", "Pause, resume, run now or cancel a routine.", object(map[string]any{"request_id": str, "automation_id": str, "action": map[string]any{"enum": []string{"pause", "resume", "run_now", "cancel"}}}, "request_id", "automation_id", "action"), "mutate",
			func(ctx context.Context, args mcpArgs) (any, error) {
				current, err := a.Scheduler.Get(ctx, args.str("automation_id"))
				if err != nil {
					return nil, err
				}
				return a.Scheduler.Act(ctx, current.ID, args.str("request_id"), current.Revision, scheduler.Action{Action: args.str("action")})
			})
	}
	add("mutate_submit_work", "Submit a request as a new Nori task. Returns the acceptance receipt with the task id.", object(map[string]any{"request_id": str, "request": str}, "request_id", "request"), "mutate",
		func(ctx context.Context, args mcpArgs) (any, error) {
			if a.Submit == nil || strings.TrimSpace(args.str("request")) == "" {
				return nil, errors.New("tasks are not available here")
			}
			taskID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("mcp-work:"+users.ID(ctx)+":"+args.str("request_id"))).String()
			run, err := a.Submit(ctx, taskID, args.str("request"))
			if err != nil && !errors.Is(err, state.ErrDuplicate) {
				return nil, err
			}
			return state.MutationReceipt{RequestID: args.str("request_id"), Accepted: true, Status: "queued", Resource: mustJSON(map[string]any{"task_id": taskID, "status": run.Status}), Work: &state.WorkRef{TaskID: taskID}}, nil
		})
	add("mutate_answer", "Answer a pending question, or approve or decline an approval by its action digest.", object(map[string]any{"request_id": str, "interaction_id": str, "text": str, "decision": map[string]any{"enum": []string{"approve", "decline"}}}, "request_id", "interaction_id"), "mutate",
		func(ctx context.Context, args mcpArgs) (any, error) {
			interaction, err := a.Permissions.Interaction(ctx, args.str("interaction_id"))
			if err != nil {
				return nil, err
			}
			response := permissions.Response{Kind: "answer", Text: args.str("text")}
			if d := args.str("decision"); d != "" {
				response = permissions.Response{Kind: d, ActionDigest: interaction.ActionDigest}
			}
			receipt, answer, err := a.Permissions.Respond(ctx, interaction.ID, args.str("request_id"), interaction.Revision, response)
			if err != nil {
				return nil, err
			}
			if answer != nil && a.Answer != nil {
				if err := a.Answer(ctx, answer.CommandID, answer.TaskID, answer.Question, answer.Text); err == nil || errors.Is(err, state.ErrDuplicate) {
					a.Permissions.MarkDelivered(ctx, interaction.ID)
				}
			}
			return receipt, nil
		})
	return server
}

func mustJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

var _ = fmt.Sprintf
