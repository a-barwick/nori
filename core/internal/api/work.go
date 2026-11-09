package api

import (
	"errors"
	"strings"

	"context"
	"github.com/google/uuid"
	"net/http"
	"time"

	"nori/internal/connections"
	"nori/internal/permissions"
	"nori/internal/state"
)

type workOrigin struct {
	Kind           string `json:"kind"`
	ConversationID string `json:"conversation_id,omitempty"`
	MessageID      string `json:"message_id,omitempty"`
	AutomationID   string `json:"automation_id,omitempty"`
	OccurrenceID   string `json:"occurrence_id,omitempty"`
	GoalID         string `json:"goal_id,omitempty"`
	// ContinuesTaskID names the task this one explicitly continues (proposed for the contract in r2.6).
	ContinuesTaskID string `json:"continues_task_id,omitempty"`
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

// workItem is the contract WorkItem, a projection over state.Run.
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
	Error               *errorDetail   `json:"error,omitempty"`
}

type workEvent struct {
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

// operationProjection extends the existing operationView wire names with the registry, account and authority
// references. Raw arguments and authority references stay private.
type operationProjection struct {
	state.OperationRecord
	Owner           operationOwner `json:"owner"`
	AccountSummary  string         `json:"account_summary,omitempty"`
	ActionSummary   string         `json:"action_summary,omitempty"`
	AuthoritySource string         `json:"authority_source,omitempty"`
	BindingDigest   string         `json:"binding_digest,omitempty"`
	ReceiptURL      string         `json:"receipt_url,omitempty"`
	TaskTitle       string         `json:"task_title,omitempty"`
}

func originOf(run state.Run) workOrigin {
	if run.Origin == nil {
		return workOrigin{Kind: "conversation", ConversationID: "assistant"}
	}
	o := run.Origin
	switch {
	case o.AutomationID != "":
		return workOrigin{Kind: "automation", AutomationID: o.AutomationID, OccurrenceID: o.OccurrenceID, GoalID: o.GoalID}
	case o.GoalID != "":
		return workOrigin{Kind: "goal", GoalID: o.GoalID}
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

func (a *Assistant) item(ctx context.Context, run state.Run, ops []state.OperationRecord, pending []string, results []string) workItem {
	item := workItem{
		TaskID: run.ID, Title: clip(run.Request, 200), Status: string(run.Status), InstructionRevision: run.Revision,
		Origin: originOf(run), InteractionIDs: pending, Progress: run.Progress, ResultIDs: results,
		ParentTaskID: run.ParentID, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, CancelRequested: run.CancelRequested,
	}
	if item.Origin.Kind == "automation" && a.Scheduler != nil {
		// Show the routine's own title, not the scheduled instruction text the task was given.
		if automation, err := a.Scheduler.Get(ctx, item.Origin.AutomationID); err == nil {
			item.Title = automation.Title
		}
	}
	if item.Origin.ContinuesTaskID != "" {
		// Show what is being continued, not the continuation instruction the task was given.
		if previous, err := a.Store.GetRun(ctx, item.Origin.ContinuesTaskID); err == nil {
			title := clip(previous.Request, 190)
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
		if run.Question != nil && !contains(item.InteractionIDs, run.Question.ID) {
			item.InteractionIDs = append(item.InteractionIDs, run.Question.ID)
		}
	default:
		item.Bucket = "working"
	}
	if run.Error != "" {
		item.Error = &errorDetail{Code: "task_failed", Message: clip(run.Error, 1000)}
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
	if records, _, err := a.Connections.Records(ctx); err == nil {
		for _, record := range records {
			labels[record.ID] = record.Account.Label
		}
	}
	out := make([]operationProjection, 0, len(ops))
	for _, op := range ops {
		p := operationProjection{OperationRecord: op, Owner: operationOwner{Type: "task", ID: run.ID}, TaskTitle: clip(run.Request, 200)}
		if op.Action != nil {
			p.ActionSummary = op.Action.Text
		}
		if op.Tool != nil {
			p.BindingDigest = op.Tool.Digest
		}
		if op.ConnectionID != "" {
			p.AccountSummary = labels[op.ConnectionID]
		}
		p.AuthoritySource = a.Permissions.AuthoritySource(ctx, op.AuthorityRef)
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

// operations lists recent operations across tasks, newest task first: the action history.
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

// workAction accepts stop through the existing task command acceptance; the receipt names the same task.
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
	run, err := a.Stop(r.Context(), requestID, taskID)
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	ops, _ := a.Store.Operations(r.Context(), run.ID)
	item := a.item(r.Context(), run, ops, nil, nil)
	data, _ := jsonMarshal(item)
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

// summary drives navigation badges. Its revision changes whenever a count changes.
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
	s.Revision = int64(s.NeedsYou*1_000_000 + s.Working*10_000 + s.AppsAttention*100 + s.InboxUnread + 1)
	writeJSON(w, 200, s)
}

// continueWork starts an explicit continuation of a finished task that did not complete, such as one stopped at its
// spending limit. Its saved results remain, completed external actions are not repeated, and the new task records
// what it continues.
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
	run, err := a.Continue(r.Context(), taskID, previous.ID, clip(request, state.MaxMessageBytes-64), budget)
	if err != nil && !errors.Is(err, state.ErrDuplicate) {
		writeAPIError(w, err, requestID, nil)
		return
	}
	ops, _ := a.Store.Operations(r.Context(), run.ID)
	data, _ := jsonMarshal(a.item(r.Context(), run, ops, nil, nil))
	writeJSON(w, 202, state.MutationReceipt{RequestID: requestID, Accepted: true, Status: "queued", Resource: data, Work: &state.WorkRef{TaskID: taskID}})
}
