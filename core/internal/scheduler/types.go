package scheduler

import (
	"encoding/json"
	"time"

	"nori/internal/state"
)

const (
	CollectionAutomations    = "automations"
	CollectionOccurrences    = "occurrences"
	CollectionDeliveries     = "deliveries"
	CollectionInbox          = "inbox"
	CollectionFindings       = "proactive_findings"
	InAppDestination         = "dest_in_app"
	ChatDestination          = "dest_chat"
	AutomationConversationID = "automation"
	MorningBriefAutomationID = "auto_morning_brief"

	DefaultMorningBriefExecutionWindowSeconds int64 = 7200
	DefaultInactivityPauseDays                      = 14

	DeliveryStatePending   = "pending"
	DeliveryStateHeld      = "held"
	DeliveryStateDelivered = "delivered"
	DeliveryStateFolded    = "folded"
	DeliveryStateDropped   = "dropped"
	DeliveryStateFailed    = "failed"
)

type OutcomeKind string

const (
	OutcomeNoOp   OutcomeKind = "no_op"
	OutcomeUpdate OutcomeKind = "update"
	OutcomeAlert  OutcomeKind = "alert"
)

type ProactiveOutcome struct {
	Outcome       OutcomeKind       `json:"outcome"`
	Summary       string            `json:"summary,omitempty"`
	FindingKeys   []string          `json:"finding_keys,omitempty"`
	Greeting      string            `json:"greeting,omitempty"`
	Meta          string            `json:"meta,omitempty"`
	Items         []state.BriefItem `json:"items,omitempty"`
	InteractionID string            `json:"interaction_id,omitempty"`
}

type ProactiveSpend struct {
	TaskLLMUSD      float64 `json:"task_llm_usd"`
	OuterLoopUSD    float64 `json:"outer_loop_usd"`
	ComputerWakeUSD float64 `json:"computer_wake_usd"`
}

func (p ProactiveSpend) Total() float64 {
	return p.TaskLLMUSD + p.OuterLoopUSD + p.ComputerWakeUSD
}

type FindingState struct {
	Key             string     `json:"key"`
	FirstSurfacedAt time.Time  `json:"first_surfaced_at"`
	LastSurfacedAt  time.Time  `json:"last_surfaced_at"`
	BriefFoldCount  int        `json:"brief_fold_count"`
	InteractionID   string     `json:"interaction_id,omitempty"`
	EngagedAt       *time.Time `json:"engaged_at,omitempty"`
	EngagedVia      string     `json:"engaged_via,omitempty"`
	Dropped         bool       `json:"dropped,omitempty"`
}

type BudgetConfirmation struct {
	Status            string  `json:"status"`
	InteractionID     string  `json:"interaction_id"`
	ActionDigest      string  `json:"action_digest"`
	ProjectedDailyUSD float64 `json:"projected_daily_usd"`
	DailyCapUSD       float64 `json:"daily_cap_usd"`
	Reason            string  `json:"reason"`
	Prompt            string  `json:"prompt"`
}

type AutomationStatus string

const (
	AutomationEnabled   AutomationStatus = "enabled"
	AutomationPaused    AutomationStatus = "paused"
	AutomationCancelled AutomationStatus = "cancelled"
)

type OccurrenceStatus string

const (
	OccurrencePending   OccurrenceStatus = "pending"
	OccurrenceAccepted  OccurrenceStatus = "accepted"
	OccurrenceRunning   OccurrenceStatus = "running"
	OccurrenceCompleted OccurrenceStatus = "completed"
	OccurrenceFailed    OccurrenceStatus = "failed"
	OccurrenceCancelled OccurrenceStatus = "cancelled"
	OccurrenceSkipped   OccurrenceStatus = "skipped"
	OccurrenceExpired   OccurrenceStatus = "expired"
)

type Trigger struct {
	Kind               string       `json:"kind"`
	At                 *time.Time   `json:"at,omitempty"`
	LocalStart         string       `json:"local_start,omitempty"`
	Timezone           string       `json:"timezone,omitempty"`
	FollowUserTimezone bool         `json:"follow_user_timezone,omitempty"`
	RRule              string       `json:"rrule,omitempty"`
	Event              *ResourceRef `json:"event,omitempty"`
	OffsetSeconds      *int64       `json:"offset_seconds,omitempty"`
	Source             *ResourceRef `json:"source,omitempty"`
	PredicateRef       string       `json:"predicate_ref,omitempty"`
	MinIntervalSeconds int64        `json:"min_interval_seconds,omitempty"`
}

type ResourceRef struct {
	ConnectionID     string `json:"connection_id"`
	Kind             string `json:"kind"`
	ProviderID       string `json:"provider_id"`
	ProviderRevision string `json:"provider_revision,omitempty"`
}

type RequestedAuthority struct {
	ActionClasses []string `json:"action_classes"`
	ToolIDs       []string `json:"tool_ids,omitempty"`
	ResourceIDs   []string `json:"resource_ids,omitempty"`
	Recipients    []string `json:"recipients,omitempty"`
	MaxOperations int      `json:"max_operations,omitempty"`
}

type Editable struct {
	Title                     string              `json:"title"`
	Instruction               string              `json:"instruction"`
	Trigger                   Trigger             `json:"trigger"`
	ConnectionIDs             []string            `json:"connection_ids"`
	RequestedAuthority        *RequestedAuthority `json:"requested_authority,omitempty"`
	BudgetUSD                 float64             `json:"budget_usd"`
	MaxActiveRuns             int                 `json:"max_active_runs"`
	MissedRun                 string              `json:"missed_run,omitempty"`
	ExecutionWindowSeconds    int64               `json:"execution_window_seconds"`
	DestinationIDs            []string            `json:"destination_ids"`
	EndAt                     *time.Time          `json:"end_at,omitempty"`
	MaxOccurrences            int                 `json:"max_occurrences,omitempty"`
	ConfirmationInteractionID string              `json:"confirmation_interaction_id,omitempty"`
}

type Automation struct {
	ID                     string           `json:"id"`
	Revision               int64            `json:"revision"`
	Title                  string           `json:"title"`
	Instruction            string           `json:"instruction"`
	Status                 AutomationStatus `json:"status"`
	System                 bool             `json:"system,omitempty"`
	PausedReason           string           `json:"paused_reason,omitempty"`
	Trigger                Trigger          `json:"trigger"`
	ConnectionIDs          []string         `json:"connection_ids"`
	AuthorityRuleIDs       []string         `json:"authority_rule_ids"`
	BudgetUSD              float64          `json:"budget_usd"`
	MaxActiveRuns          int              `json:"max_active_runs"`
	MissedRun              string           `json:"missed_run"`
	ExecutionWindowSeconds int64            `json:"execution_window_seconds"`
	DestinationIDs         []string         `json:"destination_ids"`
	NextAt                 *time.Time       `json:"next_at,omitempty"`
	EndAt                  *time.Time       `json:"end_at,omitempty"`
	MaxOccurrences         int              `json:"max_occurrences,omitempty"`
	LastOccurrenceID       string           `json:"last_occurrence_id,omitempty"`
	SnoozedUntil           *time.Time       `json:"snoozed_until,omitempty"`
	LastOccurrence         *Occurrence      `json:"last_occurrence,omitempty"`
	AuthoritySummary       string           `json:"authority_summary,omitempty"`
	CreatedAt              *time.Time       `json:"created_at,omitempty"`
	UpdatedAt              *time.Time       `json:"updated_at,omitempty"`
}

type stored struct {
	Automation
	Generation          int64      `json:"generation"`
	Cursor              *time.Time `json:"cursor,omitempty"`
	Created             int        `json:"created"`
	EventStart          *time.Time `json:"event_start,omitempty"`
	EventRevision       string     `json:"event_revision,omitempty"`
	EventStatus         string     `json:"event_status,omitempty"`
	EventCheckedAt      *time.Time `json:"event_checked_at,omitempty"`
	Checkpoint          string     `json:"checkpoint,omitempty"`
	CheckpointKeys      []string   `json:"checkpoint_keys,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures,omitempty"`
	TransientFailures   int        `json:"transient_failures,omitempty"`
}

type Action struct {
	Action                    string     `json:"action"`
	Until                     *time.Time `json:"until,omitempty"`
	ConfirmationInteractionID string     `json:"confirmation_interaction_id,omitempty"`
}

func (a Action) Valid() bool {
	switch a.Action {
	case "pause", "resume", "cancel", "run_now":
		return a.Until == nil
	case "snooze":
		return a.Until != nil
	default:
		return false
	}
}

type ErrorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type Occurrence struct {
	ID                 string           `json:"id"`
	AutomationID       string           `json:"automation_id"`
	AutomationRevision int64            `json:"automation_revision"`
	Generation         int64            `json:"generation"`
	ScheduledAt        time.Time        `json:"scheduled_at"`
	SourceRevision     string           `json:"source_revision,omitempty"`
	Status             OccurrenceStatus `json:"status"`
	TaskIDs            []string         `json:"task_ids"`
	DeliveryIDs        []string         `json:"delivery_ids"`
	StartedAt          *time.Time       `json:"started_at,omitempty"`
	FinishedAt         *time.Time       `json:"finished_at,omitempty"`
	Summary            string           `json:"summary,omitempty"`
	Outcome            OutcomeKind      `json:"outcome,omitempty"`
	FindingKeys        []string         `json:"finding_keys,omitempty"`
	Error              *ErrorDetail     `json:"error,omitempty"`
	Reason             string           `json:"reason,omitempty"`
	Deliveries         []Delivery       `json:"deliveries,omitempty"`
	Instruction        string           `json:"-"`
}

type occurrenceDoc struct {
	Occurrence
	Instruction string  `json:"instruction"`
	BudgetUSD   float64 `json:"budget_usd"`
	Monitor     bool    `json:"monitor,omitempty"`
	Source      string  `json:"source,omitempty"`
}

type Delivery struct {
	ID             string       `json:"id"`
	OccurrenceID   string       `json:"occurrence_id,omitempty"`
	AutomationID   string       `json:"automation_id,omitempty"`
	ResultIDs      []string     `json:"result_ids,omitempty"`
	TaskID         string       `json:"task_id,omitempty"`
	DestinationID  string       `json:"destination_id"`
	State          string       `json:"state"`
	Title          string       `json:"title,omitempty"`
	Body           string       `json:"body,omitempty"`
	Outcome        OutcomeKind  `json:"outcome,omitempty"`
	FindingKeys    []string     `json:"finding_keys,omitempty"`
	InteractionID  string       `json:"interaction_id,omitempty"`
	AttemptID      string       `json:"attempt_id,omitempty"`
	Receipt        string       `json:"receipt,omitempty"`
	AcknowledgedAt *time.Time   `json:"acknowledged_at,omitempty"`
	ScheduledFor   *time.Time   `json:"scheduled_for,omitempty"`
	CreatedAt      *time.Time   `json:"created_at,omitempty"`
	UpdatedAt      *time.Time   `json:"updated_at,omitempty"`
	Error          *ErrorDetail `json:"error,omitempty"`
}

type InboxItem struct {
	ID             string     `json:"id"`
	DeliveryID     string     `json:"delivery_id,omitempty"`
	Title          string     `json:"title"`
	Body           string     `json:"body"`
	Link           InboxLink  `json:"link"`
	CreatedAt      time.Time  `json:"created_at"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	Revision       int64      `json:"revision"`
}

type InboxLink struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Revision int64  `json:"revision,omitempty"`
}

type Preview struct {
	NextRuns             []time.Time       `json:"next_runs"`
	Timezone             string            `json:"timezone"`
	ProposedRules        []json.RawMessage `json:"proposed_rules"`
	AuthoritySummary     string            `json:"authority_summary,omitempty"`
	MissedRunExplanation string            `json:"missed_run_explanation"`
	Warnings             []Warning         `json:"warnings"`
}

type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
