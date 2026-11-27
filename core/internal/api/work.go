package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"nori/internal/connections"
	"nori/internal/permissions"
	"nori/internal/state"
)

type workOrigin struct {
	Kind            string `json:"kind"`
	ConversationID  string `json:"conversation_id,omitempty"`
	MessageID       string `json:"message_id,omitempty"`
	AutomationID    string `json:"automation_id,omitempty"`
	OccurrenceID    string `json:"occurrence_id,omitempty"`
	ContinuesTaskID string `json:"continues_task_id,omitempty"`
}

type workStep struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

type workOperations struct {
	Total     int `json:"total"`
	Uncertain int `json:"uncertain"`
	Failed    int `json:"failed"`
	Submitted int `json:"submitted"`
}

type workCost struct {
	AmountUSD *float64 `json:"amount_usd,omitempty"`
	Status    string   `json:"status"`
	BudgetUSD float64  `json:"budget_usd,omitempty"`
}

type errorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type workItem struct {
	TaskID              string         `json:"task_id"`
	Title               string         `json:"title"`
	Status              string         `json:"status"`
	Bucket              string         `json:"bucket"`
	InstructionRevision int64          `json:"instruction_revision"`
	Origin              workOrigin     `json:"origin"`
	InteractionIDs      []string       `json:"interaction_ids"`
	Progress            string         `json:"progress,omitempty"`
	Operations          workOperations `json:"operations"`
	ResultIDs           []string       `json:"result_ids"`
	Cost                workCost       `json:"cost"`
	ParentTaskID        string         `json:"parent_task_id,omitempty"`
	StartedAt           time.Time      `json:"started_at"`
	FinishedAt          *time.Time     `json:"finished_at,omitempty"`
	CancelRequested     bool           `json:"cancel_requested"`
	Queued              bool           `json:"queued,omitempty"`
	Steps               []workStep     `json:"steps,omitempty"`
	Partial             bool           `json:"partial,omitempty"`
	Error               *errorDetail   `json:"error,omitempty"`
}

type workEvent struct {
	ID        string    `json:"id,omitempty"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

type workDetail struct {
	Item            workItem                  `json:"item"`
	Operations      []operationProjection     `json:"operations"`
	Interactions    []permissions.Interaction `json:"interactions"`
	Result          string                    `json:"result,omitempty"`
	Events          []workEvent               `json:"events,omitempty"`
	EventsTruncated bool                      `json:"events_truncated,omitempty"`
}

type operationProjection struct {
	state.OperationRecord
	Owner           operationOwner  `json:"owner"`
	AccountSummary  string          `json:"account_summary,omitempty"`
	ActionSummary   string          `json:"action_summary,omitempty"`
	AuthoritySource string          `json:"authority_source,omitempty"`
	BindingDigest   string          `json:"binding_digest,omitempty"`
	ReceiptURL      string          `json:"receipt_url,omitempty"`
	TaskTitle       string          `json:"task_title,omitempty"`
	ResultIDs       []string        `json:"result_ids,omitempty"`
	Cost            json.RawMessage `json:"cost,omitempty"`
	UpdatedAt       *time.Time      `json:"updated_at,omitempty"`
}

func originOf(run state.Run) workOrigin {
	if run.Origin == nil {
		return workOrigin{Kind: "conversation", ConversationID: "assistant"}
	}
	o := run.Origin
	if o.AutomationID != "" {
		return workOrigin{Kind: "automation", AutomationID: o.AutomationID, OccurrenceID: o.OccurrenceID}
	}
	conversation := o.ConversationID
	if conversation == "" {
		conversation = "assistant"
	}
	kind := "conversation"
	if conversation == "external" {
		kind = "external"
	}
	return workOrigin{Kind: kind, ConversationID: conversation, MessageID: o.MessageID, ContinuesTaskID: o.ContinuesTaskID}
}

func humanStepLabel(name string) string {
	switch name {
	case "web_search":
		return "Searched the web"
	case "compose":
		return "Ran multi-step workflow"
	case "results_find":
		return "Checked saved results"
	case "result_read":
		return "Read saved result"
	case "upload_inspect", "upload_read_text":
		return "Read attached file"
	case "conversation_find", "conversation_read":
		return "Checked earlier chat"
	case "automation_create":
		return "Scheduled automation"
	case "automations_list":
		return "Checked automations"
	case "automation_change":
		return "Updated automation"
	}
	cleaned := strings.TrimSpace(strings.ReplaceAll(name, "_", " "))
	if cleaned == "" {
		return "Working"
	}
	return strings.ToUpper(cleaned[:1]) + cleaned[1:]
}

func isHiddenStepTool(name string) bool {
	switch name {
	case "capability_load", "complete", "request_input", "request_work",
		"operation_read", "task_history_read", "memory_read", "memory_search", "memory_get":
		return true
	}
	return false
}

func stepStatusOf(op state.OperationRecord) string {
	switch op.Outcome {
	case "attempted", "pending", "submitted":
		return "active"
	default:
		return "done"
	}
}

func projectWorkSteps(run state.Run, ops []state.OperationRecord) []workStep {
	if run.Status != state.RunRunning {
		return nil
	}
	steps := make([]workStep, 0, len(ops)+2)
	for i := 0; i < len(ops); i++ {
		op := ops[i]
		if isHiddenStepTool(op.Name) {
			continue
		}
		if op.Name == "web_search" {
			count := 1
			active := stepStatusOf(op) == "active"
			for i+1 < len(ops) && ops[i+1].Name == "web_search" {
				i++
				count++
				if stepStatusOf(ops[i]) == "active" {
					active = true
				}
			}
			label := "Searched the web"
			if count > 1 {
				label = fmt.Sprintf("Searched the web (%d queries)", count)
			}
			status := "done"
			if active {
				status = "active"
			}
			steps = append(steps, workStep{Text: label, Status: status})
			continue
		}
		label := ""
		if op.Action != nil {
			label = strings.TrimSpace(op.Action.Text)
		}
		if label == "" {
			label = humanStepLabel(op.Name)
		}
		steps = append(steps, workStep{Text: clip(label, 140), Status: stepStatusOf(op)})
	}
	if len(steps) > 0 {
		hasActive := false
		for _, s := range steps {
			if s.Status == "active" {
				hasActive = true
				break
			}
		}
		if !hasActive {
			if p := strings.TrimSpace(run.Progress); p != "" {
				steps = append(steps, workStep{Text: clip(p, 140), Status: "active"})
			} else if w := strings.TrimSpace(run.Waiting); w != "" {
				steps = append(steps, workStep{Text: clip(w, 140), Status: "active"})
			} else {
				steps = append(steps, workStep{Text: "Finishing up", Status: "active"})
			}
		}
	} else if p := strings.TrimSpace(run.Progress); p != "" {
		steps = append(steps, workStep{Text: clip(p, 140), Status: "active"})
	}
	if len(steps) == 0 {
		return nil
	}
	return steps
}

func (a *Assistant) item(ctx context.Context, run state.Run, ops []state.OperationRecord, pending []string, results []string) workItem {
	item := workItem{
		TaskID: run.ID, Title: state.TaskTitle(run), Status: string(run.Status), InstructionRevision: run.Revision,
		Origin: originOf(run), InteractionIDs: pending, Progress: run.Progress, ResultIDs: results,
		ParentTaskID: run.ParentID, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, CancelRequested: run.CancelRequested,
	}
	if item.Origin.Kind == "automation" && a.Scheduler != nil {
		if automation, err := a.Scheduler.Get(ctx, item.Origin.AutomationID); err == nil {
			item.Title = automation.Title
		}
	}
	if item.Origin.ContinuesTaskID != "" {
		previous, err := a.Store.GetRun(ctx, item.Origin.ContinuesTaskID)
		for hops := 0; err == nil && hops < 8 && previous.Origin != nil && previous.Origin.ContinuesTaskID != ""; hops++ {
			earlier, lookup := a.Store.GetRun(ctx, previous.Origin.ContinuesTaskID)
			if lookup != nil {
				break
			}
			previous = earlier
		}
		if err == nil {
			title := state.TaskTitle(previous)
			if previous.Origin != nil && previous.Origin.AutomationID != "" && a.Scheduler != nil {
				if automation, err := a.Scheduler.Get(ctx, previous.Origin.AutomationID); err == nil {
					title = automation.Title
				}
			}
			item.Title = "Continue: " + strings.TrimPrefix(title, "Continue: ")
		}
	}
	if item.InteractionIDs == nil {
		item.InteractionIDs = []string{}
	}
	if item.ResultIDs == nil {
		item.ResultIDs = []string{}
	}
	for _, op := range ops {
		item.Operations.Total++
		switch op.Outcome {
		case "uncertain":
			if op.Cleared == "" {
				item.Operations.Uncertain++
			}
		case "failed":
			item.Operations.Failed++
		case "submitted":
			item.Operations.Submitted++
		}
	}
	spent := run.Spent + run.SubtaskSpent
	item.Cost = workCost{AmountUSD: &spent, Status: "known", BudgetUSD: run.Budget}
	if run.CostUnknown {
		item.Cost.Status = "unknown"
	}
	switch {
	case run.Status != state.RunRunning:
		item.Bucket = "finished"
	case len(item.InteractionIDs) > 0 || (run.Question != nil && run.Question.ResolvedBy == "" && run.Question.Revision == run.Revision):
		item.Bucket = "needs_you"
		if run.Question != nil && !slices.Contains(item.InteractionIDs, run.Question.ID) {
			item.InteractionIDs = append(item.InteractionIDs, run.Question.ID)
		}
	default:
		item.Bucket = "working"
	}
	item.Queued = run.Status == state.RunRunning && item.Bucket == "working" && run.Incorporated == 0 && len(run.Events) == 0 && len(ops) == 0
	if !item.Queued {
		item.Steps = projectWorkSteps(run, ops)
	}
	if run.Error != "" {
		code := "task_failed"
		msg := strings.TrimSpace(run.Error)
		lower := strings.ToLower(msg)
		if run.Status == state.RunFailed && (strings.HasPrefix(lower, "incomplete") || strings.Contains(lower, "spending limit") || strings.Contains(lower, "budget")) {
			item.Partial = true
			code = "task_incomplete"
			if strings.HasPrefix(lower, "incomplete") {
				rest := strings.TrimSpace(msg[len("incomplete"):])
				rest = strings.TrimSpace(strings.TrimLeft(rest, ".:- "))
				if rest != "" {
					msg = rest
				}
			}
		}
		item.Error = &errorDetail{Code: code, Message: clip(msg, 1000)}
	}
	return item
}

func (a *Assistant) pendingInteractions(ctx context.Context, taskID string) ([]permissions.Interaction, error) {
	items, _, err := a.Permissions.Interactions(ctx, "pending", taskID, "", 100)
	return items, err
}

func (a *Assistant) resultIDs(ctx context.Context) (map[string][]string, error) {
	results, err := a.Store.RecentResults(ctx, 500)
	if err != nil {
		return nil, err
	}
	byRun := map[string][]string{}
	for _, result := range results {
		byRun[result.RunID] = append(byRun[result.RunID], result.ID)
	}
	return byRun, nil
}

func (a *Assistant) workItems(ctx context.Context) ([]workItem, error) {
	runs, err := a.Store.RecentRuns(ctx, 200)
	if err != nil {
		return nil, err
	}
	pending, err := a.pendingInteractions(ctx, "")
	if err != nil {
		return nil, err
	}
	byTask := map[string][]string{}
	for _, i := range pending {
		byTask[i.TaskID] = append(byTask[i.TaskID], i.ID)
	}
	results, err := a.resultIDs(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]workItem, 0, len(runs))
	for _, run := range runs {
		ops, err := a.Store.Operations(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, a.item(ctx, run, ops, byTask[run.ID], results[run.ID]))
	}
	return items, nil
}

func (a *Assistant) work(w http.ResponseWriter, r *http.Request) {
	items, err := a.workItems(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	bucket := r.URL.Query().Get("bucket")
	filtered := []workItem{}
	for _, item := range items {
		if bucket == "" || item.Bucket == bucket {
			filtered = append(filtered, item)
		}
	}
	writeJSON(w, 200, list[workItem]{Items: filtered})
}

func (a *Assistant) workDetail(w http.ResponseWriter, r *http.Request) {
	detail, err := a.detail(r.Context(), r.PathValue("task_id"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, detail)
}

func (a *Assistant) detail(ctx context.Context, taskID string) (workDetail, error) {
	run, err := a.Store.GetRun(ctx, taskID)
	if err != nil {
		return workDetail{}, err
	}
	ops, err := a.Store.Operations(ctx, run.ID)
	if err != nil {
		return workDetail{}, err
	}
	interactions, _, err := a.Permissions.Interactions(ctx, "", run.ID, "", 100)
	if err != nil {
		return workDetail{}, err
	}
	legacy, err := a.legacyQuestions(ctx, run.ID, interactions)
	if err != nil {
		return workDetail{}, err
	}
	interactions = append(interactions, legacy...)
	var pending []string
	for _, i := range interactions {
		if i.Status == "pending" {
			pending = append(pending, i.ID)
		}
	}
	results, err := a.resultIDs(ctx)
	if err != nil {
		return workDetail{}, err
	}
	detail := workDetail{Item: a.item(ctx, run, ops, pending, results[run.ID]), Operations: a.projectOperations(ctx, run, ops),
		Interactions: interactions, Result: run.Result}
	events := run.Events
	if len(events) > 50 {
		events, detail.EventsTruncated = events[len(events)-50:], true
	}
	for _, e := range events {
		detail.Events = append(detail.Events, workEvent{Kind: e.Kind, Text: e.Text, CreatedAt: e.CreatedAt})
	}
	return detail, nil
}

func (a *Assistant) projectOperations(ctx context.Context, run state.Run, ops []state.OperationRecord) []operationProjection {
	labels := map[string]string{}
	if a.Connections != nil {
		if records, _, err := a.Connections.Records(ctx); err == nil {
			for _, record := range records {
				labels[record.ID] = record.Account.Label
			}
		}
	}
	out := make([]operationProjection, 0, len(ops))
	for _, op := range ops {
		p := operationProjection{OperationRecord: op, Owner: operationOwner{Type: "task", ID: run.ID}, TaskTitle: state.TaskTitle(run)}
		if op.Action != nil && op.Action.Text != "" {
			p.ActionSummary = op.Action.Text
		} else if op.Name == "web_search" {
			p.ActionSummary = "Searched the web"
		}
		if op.Tool != nil {
			p.BindingDigest = op.Tool.Digest
		}
		if op.ConnectionID != "" {
			p.AccountSummary = labels[op.ConnectionID]
		}
		if op.AuthorityRef != "" && a.Permissions != nil {
			p.AuthoritySource = a.Permissions.AuthoritySource(ctx, op.AuthorityRef)
		}
		for _, ref := range op.ProviderRefs {
			if ref.URL != "" {
				p.ReceiptURL = ref.URL
			}
		}
		out = append(out, p)
	}
	return out
}

func (a *Assistant) workOperations(w http.ResponseWriter, r *http.Request) {
	run, err := a.Store.GetRun(r.Context(), r.PathValue("task_id"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	ops, err := a.Store.Operations(r.Context(), run.ID)
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, list[operationProjection]{Items: a.projectOperations(r.Context(), run, ops)})
}

func (a *Assistant) operations(w http.ResponseWriter, r *http.Request) {
	runs, err := a.Store.RecentRuns(r.Context(), 100)
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	connection := r.URL.Query().Get("connection_id")
	out := []operationProjection{}
	for _, run := range runs {
		ops, err := a.Store.Operations(r.Context(), run.ID)
		if err != nil {
			writeAPIError(w, err, "", nil)
			return
		}
		for _, p := range a.projectOperations(r.Context(), run, ops) {
			if connection == "" || p.ConnectionID == connection {
				out = append(out, p)
			}
		}
		if len(out) >= 500 {
			break
		}
	}
	writeJSON(w, 200, list[operationProjection]{Items: out})
}

func (a *Assistant) workAction(w http.ResponseWriter, r *http.Request) {
	var value struct {
		Action    string  `json:"action"`
		BudgetUSD float64 `json:"budget_usd,omitempty"`
	}
	requestID, _, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if value.Action == "continue" {
		a.continueWork(w, r, requestID, value.BudgetUSD)
		return
	}
	if value.Action != "stop" {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	taskID := r.PathValue("task_id")
	if a.Stop == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Error: "Background tasks are disabled.", Code: "tasks_disabled", RequestID: requestID})
		return
	}
	run, err := a.Stop(r.Context(), requestID, taskID)
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	ops, _ := a.Store.Operations(r.Context(), run.ID)
	item := a.item(r.Context(), run, ops, nil, nil)
	data, _ := json.Marshal(item)
	writeJSON(w, 202, state.MutationReceipt{RequestID: requestID, Accepted: true, Status: "queued", Resource: data, Work: &state.WorkRef{TaskID: run.ID}})
}

type summary struct {
	Revision        int64      `json:"revision"`
	NeedsYou        int        `json:"needs_you"`
	Working         int        `json:"working"`
	AppsAttention   int        `json:"apps_attention"`
	InboxUnread     int        `json:"inbox_unread"`
	NextScheduledAt *time.Time `json:"next_scheduled_at,omitempty"`
}

var lastSummaryRevision atomic.Int64

func nextSummaryRevision(nowMS int64) int64 {
	for {
		prev := lastSummaryRevision.Load()
		next := nowMS
		if next <= prev {
			next = prev + 1
		}
		if lastSummaryRevision.CompareAndSwap(prev, next) {
			return next
		}
	}
}

func (a *Assistant) summary(w http.ResponseWriter, r *http.Request) {
	items, err := a.workItems(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	var s summary
	for _, item := range items {
		switch item.Bucket {
		case "needs_you":
			s.NeedsYou++
		case "working":
			s.Working++
		}
	}
	records, _, err := a.Connections.Records(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	for _, record := range records {
		if record.Status == connections.StatusAttention || record.Health.Code != "" && record.Health.Code != connections.HealthReady && record.Status == connections.StatusConnected {
			s.AppsAttention++
		}
	}
	if a.NextScheduled != nil {
		s.NextScheduledAt = a.NextScheduled(r.Context())
	}
	if a.InboxUnread != nil {
		s.InboxUnread = a.InboxUnread(r.Context())
	}
	s.Revision = nextSummaryRevision(time.Now().UnixMilli())
	writeJSON(w, 200, s)
}

func (a *Assistant) continueWork(w http.ResponseWriter, r *http.Request, requestID string, budget float64) {
	previous, err := a.Store.GetRun(r.Context(), r.PathValue("task_id"))
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	if previous.Status == state.RunRunning || previous.Status == state.RunCompleted || a.Continue == nil {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	taskID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("continue:"+requestID)).String()
	request := "Continue this earlier task, which stopped before it finished (" + clip(previous.Error, 300) + "). Original request: " + previous.Request +
		"\nIts saved results are available through results_find. Do not repeat external actions it already completed; check the previous task's operations first."
	if earlier, err := a.Store.Operations(r.Context(), previous.ID); err == nil && len(earlier) > 0 {
		request += "\nIts operations (read one with operation_read; cite these IDs as evidence instead of redoing them):"
		for i, op := range earlier {
			if i == 40 {
				request += "\n- …"
				break
			}
			request += "\n- " + op.ID + " " + op.Name + " (" + op.Outcome + ")"
		}
	}
	run, err := a.Continue(r.Context(), taskID, previous.ID, clip(request, state.MaxMessageBytes-64), budget)
	if errors.Is(err, state.ErrDuplicate) {
		runID := run.ID
		if runID == "" {
			runID = taskID
		}
		existing, getErr := a.Store.GetRun(r.Context(), runID)
		if getErr != nil {
			writeAPIError(w, getErr, requestID, nil)
			return
		}
		run = existing
	} else if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	ops, _ := a.Store.Operations(r.Context(), run.ID)
	data, _ := json.Marshal(a.item(r.Context(), run, ops, nil, nil))
	writeJSON(w, 202, state.MutationReceipt{RequestID: requestID, Accepted: true, Status: "queued", Resource: data, Work: &state.WorkRef{TaskID: run.ID}})
}
