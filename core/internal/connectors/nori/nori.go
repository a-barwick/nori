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

const triggerSchema = `"trigger": {"type": "object", "description": "One of: {kind:once, at:RFC3339}; {kind:recurring, local_start:YYYY-MM-DDTHH:MM, timezone:IANA, rrule:RRULE}; {kind:event_relative, event:{connection_id, kind:calendar_event, provider_id:'<calendar_id>/<event_id>'}, offset_seconds}; {kind:watch, source:{connection_id, kind, provider_id}, predicate_ref, min_interval_seconds>=300}",
	"properties": {"kind": {"enum": ["once", "recurring", "event_relative", "watch"]}, "at": {"type": "string"}, "local_start": {"type": "string"}, "timezone": {"type": "string"}, "rrule": {"type": "string"},
		"event": {"type": "object"}, "offset_seconds": {"type": "integer"}, "source": {"type": "object"}, "predicate_ref": {"type": "string"}, "min_interval_seconds": {"type": "integer"}},
	"required": ["kind"]}`

var specs = []spec{
	{"upload_read_text", "Read a text attachment", "nori_files", "Attached text files", "nori.uploads/read", "read", "safe",
		"Read the text of a CSV, ICS calendar, plain-text or Markdown file this task was given. Use it to list a calendar file's events or summarize a document; it changes nothing.",
		`{"type": "object", "properties": {"upload_id": {"type": "string"}, "offset": {"type": "integer", "minimum": 0}}, "required": ["upload_id"], "additionalProperties": false}`},
	{"automation_create", "Create a reminder, routine or monitor", "nori_automations", "Reminders and routines", "nori.automations/create", "reversible_write", "provider_key",
		"Create a one-time reminder, a recurring routine, an event-relative reminder or a bounded monitor that runs the instruction at its schedule and delivers the result to the user's inbox. Do not ask the user about missed_run or execution_window_seconds unless they bring it up; Nori uses reliable defaults automatically (delivering one-time reminders within a grace window and running the latest missed instance for recurring routines). Only set missed_run (skip, latest, or all) when the user explicitly requests custom missed-run behavior. Requested standing authority for changes or sends while the user is away is shown to the user for approval.",
		`{"type": "object", "properties": {"title": {"type": "string", "minLength": 1, "maxLength": 200}, "instruction": {"type": "string", "minLength": 1, "maxLength": 6000}, ` + triggerSchema + `,
			"missed_run": {"enum": ["skip", "latest", "all"]}, "execution_window_seconds": {"type": "integer", "minimum": 0}, "max_active_runs": {"type": "integer", "minimum": 1, "maximum": 10},
			"budget_usd": {"type": "number", "minimum": 0, "maximum": 20}, "connection_ids": {"type": "array", "items": {"type": "string"}}, "destination_ids": {"type": "array", "items": {"type": "string"}},
			"end_at": {"type": "string"}, "max_occurrences": {"type": "integer", "minimum": 1}, "goal_id": {"type": "string"},
			"requested_authority": {"type": "object", "properties": {"action_classes": {"type": "array", "items": {"enum": ["read", "reversible_write", "sensitive_send"]}}, "tool_ids": {"type": "array", "items": {"type": "string"}}, "recipients": {"type": "array", "items": {"type": "string"}}, "max_operations": {"type": "integer", "minimum": 1}}, "required": ["action_classes"], "additionalProperties": false}
		}, "required": ["title", "instruction", "trigger"], "additionalProperties": false}`},
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

var Definitions = func() []extensions.Definition {
	out := []extensions.Definition{}
	for _, s := range specs {
		var compact any
		if err := json.Unmarshal([]byte(s.schema), &compact); err != nil {
			panic("nori tool schema " + s.name + ": " + err.Error())
		}
		schema, _ := json.Marshal(compact)
		exposure := extensions.ExposureDirect
		if s.capability == "nori_files" {
			exposure = extensions.ExposureDiscoverable
		}
		d, err := extensions.Seal(extensions.Definition{
			Ref:       extensions.DefinitionRef{ID: "nori/assistant/" + strings.ReplaceAll(s.name, "_", "-"), Version: Version},
			Extension: extensions.DefinitionRef{ID: "nori/assistant", Version: Version},
			ModelName: s.name, Title: s.title, Description: s.description, Capability: s.capability, CapabilityTitle: s.capabilityTitle,
			InputSchema: schema, Binding: extensions.Binding{Adapter: extensions.AdapterNative, Target: s.target, Version: Version},
			Policy:   extensions.Policy{Effect: s.effect, Retry: s.retry, TimeoutMS: 20_000, Location: extensions.LocationRuntime},
			Exposure: exposure,
		})
		if err != nil {
			panic(err)
		}
		out = append(out, d)
	}
	return out
}()

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
		if value.Trigger.Kind == "watch" && value.Trigger.MinIntervalSeconds < 300 {
			return invoke.Prepared{}, fmt.Errorf("%w: watch automations created by the assistant require min_interval_seconds >= 300 (5 minutes)", invoke.ErrInvalidArguments)
		}
		if value.MaxActiveRuns == 0 {
			value.MaxActiveRuns = 1
		}
		existing, listErr := a.Scheduler.List(ctx, "enabled")
		if listErr != nil {
			return invoke.Prepared{}, fmt.Errorf("could not verify active automation limit: %w", listErr)
		}
		if len(existing) >= 25 {
			return invoke.Prepared{}, fmt.Errorf("%w: maximum of 25 active automations reached; pause or cancel an existing automation first", invoke.ErrInvalidArguments)
		}
		preview, err := a.Scheduler.Preview(ctx, value, a.Unattended)
		if err != nil {
			return invoke.Prepared{}, fmt.Errorf("%w: %v", invoke.ErrInvalidArguments, err)
		}
		effect := extensions.EffectReversibleWrite
		if value.RequestedAuthority != nil {
			for _, class := range value.RequestedAuthority.ActionClasses {
				if class != "read" {
					effect = extensions.EffectSensitiveSend
				}
			}
		}
		zone := strings.TrimSpace(call.TimeZone)
		if zone == "" {
			zone = strings.TrimSpace(preview.Timezone)
		}
		summary := formatAutomationSummary(value, preview, zone)
		simpleOneTime := value.Trigger.Kind == "once" && len(value.ConnectionIDs) == 0 && effect == extensions.EffectReversibleWrite
		if simpleOneTime {
			return invoke.Prepared{Summary: summary, Effect: effect, Spec: spec, SkipAutoReview: true}, nil
		}
		promptTitle := "Create this routine?"
		if value.Trigger.Kind == "once" || value.Trigger.Kind == "event_relative" {
			promptTitle = "Create this reminder?"
		}
		return invoke.Prepared{
			Summary:             summary,
			Effect:              effect,
			Spec:                spec,
			PromptTitle:         promptTitle,
			RequireConfirmation: true,
		}, nil
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

func (a Adapter) resolveZone(ctx context.Context, call invoke.Call) string {
	if z := strings.TrimSpace(call.TimeZone); z != "" {
		return z
	}
	if a.Scheduler != nil && a.Scheduler.Timezone != nil {
		if z := strings.TrimSpace(a.Scheduler.Timezone(ctx)); z != "" {
			return z
		}
	}
	return ""
}

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
	return savedResult(created, a.resolveZone(ctx, call)), true
}

func savedResult(created scheduler.Automation, zone string) invoke.Result {
	if strings.TrimSpace(zone) == "" {
		zone = strings.TrimSpace(created.Trigger.Timezone)
	}
	scheduleText := ""
	scheduledAt := ""
	if created.NextAt != nil {
		scheduleText = formatLocalTime(*created.NextAt, zone)
		scheduledAt = created.NextAt.UTC().Format(time.RFC3339)
	} else if created.Trigger.At != nil {
		scheduleText = formatLocalTime(*created.Trigger.At, zone)
		scheduledAt = created.Trigger.At.UTC().Format(time.RFC3339)
	}
	summary := fmt.Sprintf("Saved %q. You can see and change it in Routines.", created.Title)
	if scheduleText != "" {
		summary = fmt.Sprintf("Saved %q for %s. You can see and change it in Routines.", created.Title, scheduleText)
	}
	content := map[string]any{
		"automation": created,
		"summary":    summary,
	}
	if created.Trigger.Kind == "once" && len(created.ConnectionIDs) == 0 {
		fallback := fmt.Sprintf("Reminder set · %s", created.Title)
		if scheduleText != "" {
			fallback = fmt.Sprintf("Reminder set · %s · %s", scheduleText, created.Title)
		}
		card := map[string]any{
			"id":       created.ID,
			"name":     created.Title,
			"text":     scheduleText,
			"fallback": fallback,
		}
		if scheduledAt != "" {
			card["scheduled_at"] = scheduledAt
		}
		content["reminder_card"] = card
	}
	return invoke.Result{Outcome: "confirmed", Content: content}
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
		return savedResult(created, a.resolveZone(ctx, call))
	case "nori.uploads/read":
		if call.Upload == nil {
			return failed(errors.New("attachments are not available here"))
		}
		name, mediaType, data, err := call.Upload(ctx, invoke.String(call.Args, "upload_id"))
		if err != nil {
			return failed(err)
		}
		if !strings.HasPrefix(mediaType, "text/") {
			return failed(fmt.Errorf("%s is %s, not a text file; images use upload_inspect", name, mediaType))
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

func Register(registry *invoke.Registry, adapter Adapter) {
	registry.Register(extensions.AdapterNative, "nori.automations", adapter)
	registry.Register(extensions.AdapterNative, "nori.goals", adapter)
	registry.Register(extensions.AdapterNative, "nori.uploads", adapter)
}

type Source struct{}

func (Source) Inventory(context.Context) (extensions.Inventory, error) {
	return extensions.Inventory{Definitions: Definitions, Capabilities: []extensions.CapabilityState{
		{Capability: "nori_automations", Title: "Reminders and routines", Account: "Nori", Availability: "Available", Ready: true},
		{Capability: "nori_goals", Title: "Goals", Account: "Nori", Availability: "Available", Ready: true},
		{Capability: "nori_files", Title: "Attached text files", Account: "Nori", Availability: "Available", Ready: true},
	}}, nil
}

func parseInstant(value string) (time.Time, error) { return time.Parse(time.RFC3339, value) }

func formatLocalTime(t time.Time, zone string) string {
	loc := time.UTC
	if strings.TrimSpace(zone) != "" {
		if loaded, err := time.LoadLocation(strings.TrimSpace(zone)); err == nil {
			loc = loaded
		}
	}
	local := t.In(loc)
	return local.Format("Mon, Jan 2 at 3:04 PM MST")
}

func formatLocalClock(t time.Time, zone string) string {
	loc := time.UTC
	if strings.TrimSpace(zone) != "" {
		if loaded, err := time.LoadLocation(strings.TrimSpace(zone)); err == nil {
			loc = loaded
		}
	}
	return t.In(loc).Format("3:04 PM MST")
}

func formatAutomationSummary(value scheduler.Editable, preview scheduler.Preview, zone string) string {
	var lines []string
	lines = append(lines, strings.TrimSpace(value.Title))
	if schedule := formatScheduleLine(value.Trigger, preview.NextRuns, zone); schedule != "" {
		lines = append(lines, schedule)
	}
	if auth := strings.TrimSpace(preview.AuthoritySummary); auth != "" {
		lines = append(lines, auth)
	}
	if missed := strings.TrimSpace(preview.MissedRunExplanation); missed != "" {
		lines = append(lines, missed)
	}
	return strings.Join(lines, "\n")
}

func formatScheduleLine(t scheduler.Trigger, nextRuns []time.Time, zone string) string {
	switch t.Kind {
	case "once":
		if len(nextRuns) > 0 {
			return formatLocalTime(nextRuns[0], zone)
		}
		if t.At != nil {
			return formatLocalTime(*t.At, zone)
		}
	case "recurring":
		cadence := describeRRule(t.RRule)
		if len(nextRuns) > 0 {
			clock := formatLocalClock(nextRuns[0], zone)
			nextStr := formatLocalTime(nextRuns[0], zone)
			if cadence != "" {
				return fmt.Sprintf("%s at %s · Next: %s", cadence, clock, nextStr)
			}
			return "Next: " + nextStr
		}
		if cadence != "" {
			return cadence
		}
	case "event_relative":
		if len(nextRuns) > 0 {
			return "Next: " + formatLocalTime(nextRuns[0], zone)
		}
		if t.OffsetSeconds != nil {
			return formatEventOffset(*t.OffsetSeconds)
		}
	case "watch":
		return formatWatchInterval(t.MinIntervalSeconds)
	}
	return ""
}

func describeRRule(rrule string) string {
	fields := map[string]string{}
	for _, part := range strings.Split(strings.ToUpper(strings.TrimSpace(rrule)), ";") {
		if k, v, ok := strings.Cut(part, "="); ok {
			fields[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	freq := fields["FREQ"]
	interval := fields["INTERVAL"]
	if interval == "" {
		interval = "1"
	}
	byDay := fields["BYDAY"]
	switch freq {
	case "HOURLY":
		if interval == "1" {
			return "Every hour"
		}
		return "Every " + interval + " hours"
	case "DAILY":
		if interval == "1" {
			return "Every day"
		}
		return "Every " + interval + " days"
	case "WEEKLY":
		if interval == "1" {
			switch byDay {
			case "MO,TU,WE,TH,FR":
				return "Every weekday"
			case "SA,SU":
				return "Every weekend"
			case "":
				return "Every week"
			default:
				if names := formatWeekdays(byDay); names != "" {
					return "Every " + names
				}
				return "Every week"
			}
		}
		if names := formatWeekdays(byDay); names != "" {
			return fmt.Sprintf("Every %s weeks on %s", interval, names)
		}
		return "Every " + interval + " weeks"
	case "MONTHLY":
		if interval == "1" {
			return "Every month"
		}
		return "Every " + interval + " months"
	case "YEARLY":
		if interval == "1" {
			return "Every year"
		}
		return "Every " + interval + " years"
	}
	return ""
}

func formatWeekdays(byDay string) string {
	dayMap := map[string]string{
		"MO": "Monday", "TU": "Tuesday", "WE": "Wednesday", "TH": "Thursday",
		"FR": "Friday", "SA": "Saturday", "SU": "Sunday",
	}
	var names []string
	for _, code := range strings.Split(byDay, ",") {
		name, ok := dayMap[strings.TrimSpace(code)]
		if !ok {
			return ""
		}
		names = append(names, name)
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
	}
}

func formatEventOffset(offsetSeconds int64) string {
	if offsetSeconds == 0 {
		return "At event start"
	}
	dir := "after"
	abs := offsetSeconds
	if offsetSeconds < 0 {
		dir = "before"
		abs = -offsetSeconds
	}
	if abs%3600 == 0 {
		hours := abs / 3600
		if hours == 1 {
			return "1 hour " + dir + " event"
		}
		return fmt.Sprintf("%d hours %s event", hours, dir)
	}
	if abs%60 == 0 {
		mins := abs / 60
		if mins == 1 {
			return "1 minute " + dir + " event"
		}
		return fmt.Sprintf("%d minutes %s event", mins, dir)
	}
	return fmt.Sprintf("%d seconds %s event", abs, dir)
}

func formatWatchInterval(seconds int64) string {
	if seconds <= 0 {
		return "Monitor"
	}
	if seconds%3600 == 0 {
		hours := seconds / 3600
		if hours == 1 {
			return "Checks every hour"
		}
		return fmt.Sprintf("Checks every %d hours", hours)
	}
	if seconds%60 == 0 {
		mins := seconds / 60
		if mins == 1 {
			return "Checks every minute"
		}
		return fmt.Sprintf("Checks every %d minutes", mins)
	}
	return fmt.Sprintf("Checks every %d seconds", seconds)
}
