// Package nori holds first-party tools over Nori's own application services: reminders, routines and monitors,
// goals, and the inbox. They are registry definitions executed through the same Tool executor, authority checks and
// operation records as provider tools, so a conversation creates an automation exactly as the UI does.
package nori

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/scheduler"
	"nori/internal/state"
)

const Version = "1"

type spec struct {
	name, title, capability, capabilityTitle, target string
	effect                                           extensions.Effect
	retry                                            string
	description, schema                              string
}

const triggerSchema = `"trigger": {"type": "object", "description": "One of: {kind:once, at:RFC3339}; {kind:recurring, local_start:YYYY-MM-DDTHH:MM, timezone:IANA, rrule:RRULE}; {kind:event_relative, event:{connection_id, kind:calendar_event, provider_id:'<calendar_id>/<event_id>'}, offset_seconds}; {kind:watch, source:{connection_id, kind, provider_id}, predicate_ref, min_interval_seconds>=60}",
	"properties": {"kind": {"enum": ["once", "recurring", "event_relative", "watch"]}, "at": {"type": "string"}, "local_start": {"type": "string"}, "timezone": {"type": "string"}, "rrule": {"type": "string"},
		"event": {"type": "object"}, "offset_seconds": {"type": "integer"}, "source": {"type": "object"}, "predicate_ref": {"type": "string"}, "min_interval_seconds": {"type": "integer"}},
	"required": ["kind"]}`

var specs = []spec{
	{"upload_read_text", "Read a text attachment", "nori_files", "Attached text files", "nori.uploads/read", "read", "safe",
		"Read the text of a CSV, ICS calendar, plain-text or Markdown file this task was given, without the computer. Use it to list a calendar file's events or summarize a document; it changes nothing.",
		`{"type": "object", "properties": {"upload_id": {"type": "string"}, "offset": {"type": "integer", "minimum": 0}}, "required": ["upload_id"], "additionalProperties": false}`},
	{"computer_request_takeover", "Ask the user to take over the computer", "nori_computer", "Computer", "nori.computer/handoff", "read", "safe",
		"Ask the user to take over the computer to sign in, solve a check or act where Nori is blocked. Never type passwords yourself. The task waits until the user resumes it, then re-check the browser.",
		`{"type": "object", "properties": {"reason": {"enum": ["sign_in", "captcha", "blocked"]}, "message": {"type": "string", "maxLength": 500}}, "required": ["reason", "message"], "additionalProperties": false}`},
	{"automation_create", "Create a reminder, routine or monitor", "nori_automations", "Reminders and routines", "nori.automations/create", "reversible_write", "provider_key",
		"Create a one-time reminder, a recurring routine, an event-relative reminder or a bounded monitor that runs the instruction at its schedule and delivers the result to the user's inbox. missed_run says what happens to runs Nori could not make: skip, latest or all. There is no default: ask the user or take it from what they said, and state the choice. Things to weigh: catching up with the latest run suits reading or summarizing routines; a reminder that missed its time may still be useful within a window (execution_window_seconds); a missed run that changes or sends something usually should not run late (skip). Requested standing authority for changes or sends while the user is away is shown to the user for approval.",
		`{"type": "object", "properties": {"title": {"type": "string", "minLength": 1, "maxLength": 200}, "instruction": {"type": "string", "minLength": 1, "maxLength": 6000}, ` + triggerSchema + `,
			"missed_run": {"enum": ["skip", "latest", "all"]}, "execution_window_seconds": {"type": "integer", "minimum": 0}, "max_active_runs": {"type": "integer", "minimum": 1, "maximum": 10},
			"budget_usd": {"type": "number", "minimum": 0, "maximum": 20}, "connection_ids": {"type": "array", "items": {"type": "string"}}, "destination_ids": {"type": "array", "items": {"type": "string"}},
			"end_at": {"type": "string"}, "max_occurrences": {"type": "integer", "minimum": 1}, "goal_id": {"type": "string"},
			"requested_authority": {"type": "object", "properties": {"action_classes": {"type": "array", "items": {"enum": ["read", "reversible_write", "sensitive_send"]}}, "tool_ids": {"type": "array", "items": {"type": "string"}}, "recipients": {"type": "array", "items": {"type": "string"}}, "max_operations": {"type": "integer", "minimum": 1}}, "required": ["action_classes"], "additionalProperties": false}
		}, "required": ["title", "instruction", "trigger", "missed_run"], "additionalProperties": false}`},
	{"automations_list", "List reminders and routines", "nori_automations", "Reminders and routines", "nori.automations/list", "read", "safe",
		"List the user's reminders, routines and monitors with their next run, last result and status.",
		`{"type": "object", "properties": {"status": {"enum": ["enabled", "paused", "cancelled"]}}, "additionalProperties": false}`},
	{"automation_change", "Pause, resume, snooze, run or cancel a routine", "nori_automations", "Reminders and routines", "nori.automations/act", "reversible_write", "provider_key",
		"Pause, resume, snooze until a time, run now or cancel one of the user's reminders or routines.",
		`{"type": "object", "properties": {"automation_id": {"type": "string"}, "action": {"enum": ["pause", "resume", "snooze", "run_now", "cancel"]}, "until": {"type": "string", "description": "RFC 3339, for snooze"}}, "required": ["automation_id", "action"], "additionalProperties": false}`},
	{"goal_create", "Adopt a goal", "nori_goals", "Goals", "nori.goals/create", "reversible_write", "provider_key",
		"Record a goal the user explicitly adopted, with its outcome and completion criteria. Never create a goal from an inferred wish.",
		`{"type": "object", "properties": {"title": {"type": "string", "minLength": 1}, "outcome": {"type": "string", "minLength": 1}, "completion_criteria": {"type": "array", "items": {"type": "string"}, "minItems": 1}, "deadline": {"type": "string"}, "follow_up": {"enum": ["none", "remind", "monitor"]}}, "required": ["title", "outcome", "completion_criteria"], "additionalProperties": false}`},
	{"goals_list", "List goals", "nori_goals", "Goals", "nori.goals/list", "read", "safe",
		"List the user's goals with progress, next actions, blockers and criteria.",
		`{"type": "object", "properties": {"status": {"enum": ["active", "paused", "completed", "abandoned"]}}, "additionalProperties": false}`},
	{"goal_progress", "Record goal progress", "nori_goals", "Goals", "nori.goals/progress", "reversible_write", "provider_key",
		"Record progress, next actions and blockers on an active goal, and mark criteria met with the saved results that prove them. A failed attempt is a blocker, not a reason to drop the goal.",
		`{"type": "object", "properties": {"goal_id": {"type": "string"}, "progress": {"type": "string", "maxLength": 2000}, "next_actions": {"type": "array", "items": {"type": "string"}},
			"blockers": {"type": "array", "items": {"type": "string"}}, "met_criteria": {"type": "array", "items": {"type": "object", "properties": {"id": {"type": "string"}, "result_ids": {"type": "array", "items": {"type": "string"}, "minItems": 1}}, "required": ["id", "result_ids"], "additionalProperties": false}}
		}, "required": ["goal_id"], "additionalProperties": false}`},
}

// Definitions are the first-party tools.
var Definitions = func() []extensions.Definition {
	out := []extensions.Definition{}
	for _, s := range specs {
		var compact any
		if err := json.Unmarshal([]byte(s.schema), &compact); err != nil {
			panic("nori tool schema " + s.name + ": " + err.Error())
		}
		schema, _ := json.Marshal(compact)
		d, err := extensions.Seal(extensions.Definition{
			Ref:       extensions.DefinitionRef{ID: "nori/assistant/" + strings.ReplaceAll(s.name, "_", "-"), Version: Version},
			Extension: extensions.DefinitionRef{ID: "nori/assistant", Version: Version},
			ModelName: s.name, Title: s.title, Description: s.description, Capability: s.capability, CapabilityTitle: s.capabilityTitle,
			InputSchema: schema, Binding: extensions.Binding{Adapter: extensions.AdapterNative, Target: s.target, Version: Version},
			Policy:   extensions.Policy{Effect: s.effect, Retry: s.retry, TimeoutMS: 20_000, Location: extensions.LocationRuntime},
			Exposure: extensions.ExposureDirect,
		})
		if err != nil {
			panic(err)
		}
		out = append(out, d)
	}
	return out
}()

// Adapter executes the first-party tools against one user's Scheduler.
type Adapter struct {
	Scheduler  *scheduler.Service
	Unattended string
}

func requestID(call invoke.Call) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("nori-tool:"+call.OperationID)).String()
}

func decode(args map[string]any, value any) error {
	data, _ := json.Marshal(args)
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("%w: %v", invoke.ErrInvalidArguments, err)
	}
	return nil
}

func (a Adapter) Prepare(ctx context.Context, call invoke.Call) (invoke.Prepared, error) {
	_, operation, _ := strings.Cut(call.Exec.Binding.Target, "/")
	data, _ := json.Marshal(call.Args)
	sum := sha256.Sum256(data)
	spec := map[string]string{"arguments_sha256": hex.EncodeToString(sum[:])}
	switch call.Exec.Binding.Target {
	case "nori.automations/create":
		var value scheduler.Editable
		if err := decode(call.Args, &value); err != nil {
			return invoke.Prepared{}, err
		}
		if value.MaxActiveRuns == 0 {
			value.MaxActiveRuns = 1
		}
		preview, err := a.Scheduler.Preview(ctx, value, a.Unattended)
		if err != nil {
			return invoke.Prepared{}, fmt.Errorf("%w: %v", invoke.ErrInvalidArguments, err)
		}
		effect := extensions.EffectReversibleWrite
		if value.RequestedAuthority != nil {
			for _, class := range value.RequestedAuthority.ActionClasses {
				if class != "read" {
					// Granting standing authority to act while the user is away is itself consequential.
					effect = extensions.EffectSensitiveSend
				}
			}
		}
		summary := fmt.Sprintf("Create %q. Next runs: %v. %s %s", value.Title, preview.NextRuns, preview.AuthoritySummary, preview.MissedRunExplanation)
		return invoke.Prepared{Summary: summary, Effect: effect, Spec: spec}, nil
	case "nori.automations/act":
		return invoke.Prepared{Summary: fmt.Sprintf("%s automation %s", invoke.String(call.Args, "action"), invoke.String(call.Args, "automation_id")), Effect: extensions.EffectReversibleWrite, Spec: spec}, nil
	case "nori.goals/create":
		return invoke.Prepared{Summary: "Adopt the goal " + invoke.String(call.Args, "title"), Effect: extensions.EffectReversibleWrite, Spec: spec}, nil
	case "nori.uploads/read":
		return invoke.Prepared{Summary: "Read an attached file", Effect: extensions.EffectRead, Spec: spec}, nil
	case "nori.goals/progress":
		return invoke.Prepared{Summary: "Record progress on goal " + invoke.String(call.Args, "goal_id"), Effect: extensions.EffectReversibleWrite, Spec: spec}, nil
	}
	_ = operation
	return invoke.Prepared{Summary: call.Exec.Binding.Target, Effect: extensions.EffectRead, Spec: spec}, nil
}

// Reconcile settles an interrupted reminder or routine create from Nori's own store: the saved automation under the
// call's request ID, or proof that nothing was saved. Other first-party writes stay uncertain until the user answers.
func (a Adapter) Reconcile(ctx context.Context, call invoke.Call, _ invoke.Prepared) (invoke.Result, bool) {
	if call.Exec.Binding.Target != "nori.automations/create" {
		return invoke.Result{}, false
	}
	created, err := a.Scheduler.Created(ctx, requestID(call))
	if errors.Is(err, state.ErrNotFound) {
		return invoke.Result{Outcome: "failed", Err: errors.New("the interrupted attempt did not save it")}, true
	}
	if err != nil {
		return invoke.Result{}, false
	}
	return savedResult(created), true
}

func savedResult(created scheduler.Automation) invoke.Result {
	return invoke.Result{Outcome: "confirmed", Content: map[string]any{"automation": created,
		"summary": fmt.Sprintf("Saved %q; next run %v. The user can see and change it in Routines.", created.Title, created.NextAt)}}
}

func (a Adapter) Execute(ctx context.Context, call invoke.Call, _ invoke.Prepared) invoke.Result {
	failed := func(err error) invoke.Result {
		return invoke.Result{Outcome: "failed", Err: err}
	}
	switch call.Exec.Binding.Target {
	case "nori.automations/create":
		var value scheduler.Editable
		if err := decode(call.Args, &value); err != nil {
			return failed(err)
		}
		if value.MaxActiveRuns == 0 {
			value.MaxActiveRuns = 1
		}
		if value.BudgetUSD == 0 {
			value.BudgetUSD = 1
		}
		receipt, err := a.Scheduler.Save(ctx, "", requestID(call), 0, value)
		if err != nil {
			return failed(err)
		}
		var created scheduler.Automation
		_ = json.Unmarshal(receipt.Resource, &created)
		return savedResult(created)
	case "nori.uploads/read":
		if call.Upload == nil {
			return failed(errors.New("attachments are not available here"))
		}
		name, mediaType, data, err := call.Upload(ctx, invoke.String(call.Args, "upload_id"))
		if err != nil {
			return failed(err)
		}
		if !strings.HasPrefix(mediaType, "text/") {
			return failed(fmt.Errorf("%s is %s, not a text file; images use upload_inspect and PDFs need the computer", name, mediaType))
		}
		offset := min(invoke.Int(call.Args, "offset", 0), len(data))
		const page = 32 << 10
		end := min(offset+page, len(data))
		content := map[string]any{"name": name, "media_type": mediaType, "text": strings.ToValidUTF8(string(data[offset:end]), ""), "bytes": len(data)}
		if end < len(data) {
			content["next_offset"] = end
		}
		if mediaType == "text/calendar" {
			content["note"] = "This is an uploaded calendar file, not a live calendar. Importing it is a separate action."
		}
		return invoke.Result{Outcome: "confirmed", Content: content}
	case "nori.automations/list":
		list, err := a.Scheduler.List(ctx, invoke.String(call.Args, "status"))
		if err != nil {
			return failed(err)
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"automations": list}}
	case "nori.automations/act":
		current, err := a.Scheduler.Get(ctx, invoke.String(call.Args, "automation_id"))
		if err != nil {
			return failed(err)
		}
		action := scheduler.Action{Action: invoke.String(call.Args, "action")}
		if until := invoke.String(call.Args, "until"); until != "" {
			t, err := parseInstant(until)
			if err != nil {
				return failed(fmt.Errorf("%w: until must be RFC 3339", invoke.ErrInvalidArguments))
			}
			action.Until = &t
		}
		receipt, err := a.Scheduler.Act(ctx, current.ID, requestID(call), current.Revision, action)
		if err != nil {
			return failed(err)
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"automation": json.RawMessage(receipt.Resource)}}
	case "nori.goals/create":
		value := scheduler.GoalEditable{Title: invoke.String(call.Args, "title"), Outcome: invoke.String(call.Args, "outcome"), FollowUp: invoke.String(call.Args, "follow_up")}
		for _, text := range invoke.Strings(call.Args, "completion_criteria") {
			value.CompletionCriteria = append(value.CompletionCriteria, struct {
				ID   string `json:"id,omitempty"`
				Text string `json:"text"`
			}{Text: text})
		}
		if deadline := invoke.String(call.Args, "deadline"); deadline != "" {
			t, err := parseInstant(deadline)
			if err != nil {
				return failed(fmt.Errorf("%w: deadline must be RFC 3339", invoke.ErrInvalidArguments))
			}
			value.Deadline = &t
		}
		receipt, err := a.Scheduler.SaveGoal(ctx, "", requestID(call), 0, value)
		if err != nil {
			return failed(err)
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"goal": json.RawMessage(receipt.Resource)}}
	case "nori.goals/list":
		goals, err := a.Scheduler.Goals(ctx, invoke.String(call.Args, "status"))
		if err != nil {
			return failed(err)
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"goals": goals}}
	case "nori.goals/progress":
		update := scheduler.GoalProgress{Progress: invoke.String(call.Args, "progress"), TaskID: call.RunID}
		if _, ok := call.Args["next_actions"]; ok {
			update.NextActions = invoke.Strings(call.Args, "next_actions")
		}
		if _, ok := call.Args["blockers"]; ok {
			update.Blockers = []scheduler.Blocker{}
			for _, text := range invoke.Strings(call.Args, "blockers") {
				update.Blockers = append(update.Blockers, scheduler.Blocker{Text: text})
			}
		}
		if raw, ok := call.Args["met_criteria"]; ok {
			data, _ := json.Marshal(raw)
			_ = json.Unmarshal(data, &update.MetCriteria)
		}
		goal, err := a.Scheduler.RecordProgress(ctx, invoke.String(call.Args, "goal_id"), requestID(call), update)
		if err != nil {
			return failed(err)
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"goal": goal}}
	}
	return failed(errors.New("unknown Nori tool"))
}

// Register adds the first-party tools' adapter to a user's registry.
func Register(registry *invoke.Registry, adapter Adapter) {
	registry.Register(extensions.AdapterNative, "nori.automations", adapter)
	registry.Register(extensions.AdapterNative, "nori.goals", adapter)
	registry.Register(extensions.AdapterNative, "nori.uploads", adapter)
}

// Source offers the first-party tools as a registry inventory.
type Source struct{}

func (Source) Inventory(context.Context) (extensions.Inventory, error) {
	return extensions.Inventory{Definitions: Definitions, Capabilities: []extensions.CapabilityState{
		{Capability: "nori_automations", Title: "Reminders and routines", Account: "Nori", Availability: "Available", Ready: true},
		{Capability: "nori_goals", Title: "Goals", Account: "Nori", Availability: "Available", Ready: true},
		{Capability: "nori_files", Title: "Attached text files", Account: "Nori", Availability: "Available", Ready: true},
		{Capability: "nori_computer", Title: "Computer", Account: "Nori", Availability: "Available when the computer is enabled", Ready: true},
	}}, nil
}

func parseInstant(value string) (time.Time, error) { return time.Parse(time.RFC3339, value) }
