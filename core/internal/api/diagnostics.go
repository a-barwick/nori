package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/connections"
	"nori/internal/scheduler"
	"nori/internal/state"
)

// Diagnostics lists conditions that need repair, each with the repair that runs through the normal authority and
// operation paths. Nothing here repeats an external action: an uncertain write is resolved by the user's answer or
// by provider reconciliation, never by an operator shortcut.
type diagnostic struct {
	Kind     string         `json:"kind"`
	Severity string         `json:"severity"`
	Subject  map[string]any `json:"subject"`
	Message  string         `json:"message"`
	Repair   string         `json:"repair,omitempty"`
	Since    *time.Time     `json:"since,omitempty"`
}

func (a *Assistant) diagnosticsList(ctx context.Context) ([]diagnostic, error) {
	out := []diagnostic{}
	now := time.Now().UTC()
	runs, err := a.Store.RecentRuns(ctx, 200)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		started := run.StartedAt
		if run.Status == state.RunRunning && run.CancelRequested && now.Sub(started) > 10*time.Minute {
			out = append(out, diagnostic{Kind: "stuck_stop", Severity: "warning", Subject: map[string]any{"task_id": run.ID}, Since: &started,
				Message: "Stop was accepted but the task has not finished. A worker may not be polling.", Repair: "resume_delivery"})
		} else if run.Status == state.RunRunning && run.Incorporated < run.Position && now.Sub(started) > 15*time.Minute {
			out = append(out, diagnostic{Kind: "stuck_task", Severity: "warning", Subject: map[string]any{"task_id": run.ID}, Since: &started,
				Message: "Accepted commands have not reached the task. Delivery or the worker may be stalled.", Repair: "resume_delivery"})
		}
		if run.CleanupPending {
			out = append(out, diagnostic{Kind: "cleanup_pending", Severity: "info", Subject: map[string]any{"task_id": run.ID},
				Message: "The task's computer cleanup has not been confirmed: " + run.CleanupError})
		}
		ops, err := a.Store.Operations(ctx, run.ID)
		if err != nil {
			continue
		}
		for _, op := range ops {
			if (op.Outcome == "uncertain" || op.Outcome == "submitted") && op.Cleared == "" && op.EffectClass != "read" {
				out = append(out, diagnostic{Kind: "unresolved_write", Severity: "error", Subject: map[string]any{"task_id": run.ID, "operation_id": op.ID},
					Message: "An external change may or may not have happened (" + op.Name + "). It is not repeated automatically; the task's question or the provider's state settles it."})
			}
		}
	}
	if a.Scheduler != nil {
		automations, err := a.Scheduler.List(ctx, "enabled")
		if err == nil {
			for _, auto := range automations {
				if auto.NextAt != nil && now.Sub(*auto.NextAt) > 5*time.Minute {
					next := *auto.NextAt
					out = append(out, diagnostic{Kind: "missed_wake", Severity: "error", Subject: map[string]any{"automation_id": auto.ID}, Since: &next,
						Message: "A scheduled run is overdue; its wake was lost or the scheduler has not run.", Repair: "run_scheduler"})
				}
			}
		}
		page, _, err := a.Store.Resources(ctx, scheduler.CollectionDeliveries, map[string]string{"state": "failed"}, "", 100)
		if err == nil {
			for _, r := range page {
				var d scheduler.Delivery
				if r.Decode(&d) == nil {
					message := "A delivery failed."
					if d.Error != nil {
						message = d.Error.Message
					}
					out = append(out, diagnostic{Kind: "delivery_failed", Severity: "warning", Subject: map[string]any{"delivery_id": d.ID, "destination_id": d.DestinationID}, Message: message})
				}
			}
		}
	}
	if records, _, err := a.Connections.Records(ctx); err == nil {
		for _, record := range records {
			if record.Status == connections.StatusAttention || record.Health.Code != "" && record.Health.Code != connections.HealthReady && record.Status == connections.StatusConnected {
				out = append(out, diagnostic{Kind: "connection_attention", Severity: "warning", Subject: map[string]any{"connection_id": record.ID},
					Message: record.Health.Message, Repair: record.Health.Recovery})
			}
		}
	}
	if a.MCP != nil {
		if servers, err := a.MCP.List(ctx); err == nil {
			for _, server := range servers {
				if server.Enabled && server.Observed.Status == "failed" {
					message := "The MCP server could not be reached."
					if server.Observed.Error != nil {
						message = server.Observed.Error.Message
					}
					out = append(out, diagnostic{Kind: "mcp_failed", Severity: "warning", Subject: map[string]any{"mcp_server_id": server.ID}, Message: message, Repair: "reconnect"})
				}
			}
		}
	}
	if a.Plugins != nil {
		if installs, err := a.Plugins.Installations(ctx); err == nil {
			for _, inst := range installs {
				for _, d := range inst.Diagnostics {
					out = append(out, diagnostic{Kind: "plugin_problem", Severity: "warning", Subject: map[string]any{"installation_id": inst.ID}, Message: d})
				}
			}
		}
	}
	return out, nil
}

func (a *Assistant) diagnostics(w http.ResponseWriter, r *http.Request) {
	items, err := a.diagnosticsList(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, list[diagnostic]{Items: items})
}

// diagnosticsAction runs a repair: resume delivery repairs accepted commands and worker wakes; run_scheduler
// re-reads durable schedules and re-arms due-time wakes.
func (a *Assistant) diagnosticsAction(w http.ResponseWriter, r *http.Request) {
	var value struct {
		Action string `json:"action"`
	}
	requestID, _, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	var err error
	switch value.Action {
	case "resume_delivery":
		if a.Resume == nil {
			err = state.ErrInvalidResource
		} else {
			err = a.Resume(r.Context())
		}
	case "run_scheduler":
		if a.Scheduler == nil {
			err = state.ErrInvalidResource
		} else {
			_, err = a.Scheduler.Tick(r.Context())
		}
	default:
		err = state.ErrInvalidResource
	}
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	items, _ := a.diagnosticsList(r.Context())
	data, _ := json.Marshal(map[string]any{"action": value.Action, "remaining": items})
	writeJSON(w, 200, state.MutationReceipt{RequestID: requestID, Accepted: true, Status: "applied", Resource: data})
}

const collectionFeedback = "feedback"

// feedback records what the user reports, linked to the task's evidence (operations, results, trace) so nobody has
// to find trace IDs.
func (a *Assistant) feedback(w http.ResponseWriter, r *http.Request) {
	var value struct {
		Kind         string `json:"kind"`
		TaskID       string `json:"task_id,omitempty"`
		OperationID  string `json:"operation_id,omitempty"`
		ConnectionID string `json:"connection_id,omitempty"`
		Text         string `json:"text,omitempty"`
	}
	requestID, _, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	switch value.Kind {
	case "wrong_result", "wrong_action", "too_slow", "missing_capability", "broken_connection":
	default:
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	evidence := map[string]any{}
	if value.TaskID != "" {
		run, err := a.Store.GetRun(r.Context(), value.TaskID)
		if err != nil {
			writeAPIError(w, err, requestID, nil)
			return
		}
		ops, _ := a.Store.Operations(r.Context(), run.ID)
		ids := []string{}
		for _, op := range ops {
			ids = append(ids, op.ID)
		}
		evidence["operation_ids"], evidence["status"], evidence["spent_usd"] = ids, run.Status, run.Spent
		if trace := run.OriginTraceContext["traceparent"]; trace != "" {
			evidence["trace"] = strings.Split(trace, "-")[1]
		}
	}
	id := "fb_" + strings.ReplaceAll(uuid.NewSHA1(uuid.NameSpaceOID, []byte("feedback:"+requestID)).String(), "-", "")[:20]
	record := map[string]any{"id": id, "kind": value.Kind, "task_id": value.TaskID, "operation_id": value.OperationID,
		"connection_id": value.ConnectionID, "text": clip(value.Text, 4000), "evidence": evidence, "created_at": time.Now().UTC()}
	receipt, err := a.Store.Mutate(r.Context(), state.Mutation{RequestID: requestID, Method: "POST", Target: "/v1/feedback", Payload: value,
		Collection: collectionFeedback, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			res, err := state.NewResource(collectionFeedback, id, 1, record, map[string]string{"kind": value.Kind, "task_id": value.TaskID}, time.Now())
			if err != nil {
				return state.MutationResult{}, err
			}
			return state.MutationResult{Writes: []state.Resource{res}, Receipt: state.MutationReceipt{Status: "applied", Resource: mustJSON(record), HTTPStatus: 201}}, nil
		}})
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	writeReceipt(w, receipt)
}
