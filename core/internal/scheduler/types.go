// Package scheduler owns Automations, their occurrences, delivery and goals. Definitions and occurrences are
// durable records; due work becomes task commands through the existing acceptance path with stable identities, so
// duplicate or lost wakes never duplicate or lose a settled occurrence.
package scheduler

import (
	"encoding/json"
	"time"
)

const (
	CollectionAutomations  = "automations"
	CollectionOccurrences  = "occurrences"
	CollectionDeliveries   = "deliveries"
	CollectionDestinations = "delivery_destinations"
	CollectionInbox        = "inbox"
	CollectionGoals        = "goals"
	InAppDestination       = "dest_in_app"
)

// Trigger is the contract Trigger union.
type Trigger struct {
	Kind               string       `json:"kind"`
	At                 *time.Time   `json:"at,omitempty"`
	LocalStart         string       `json:"local_start,omitempty"`
	Timezone           string       `json:"timezone,omitempty"`
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

type DefinitionRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// Editable is the contract AutomationEditable.
type Editable struct {
	Title                  string              `json:"title"`
	Instruction            string              `json:"instruction"`
	Trigger                Trigger             `json:"trigger"`
	ConnectionIDs          []string            `json:"connection_ids"`
	RequestedAuthority     *RequestedAuthority `json:"requested_authority,omitempty"`
	BudgetUSD              float64             `json:"budget_usd"`
	MaxActiveRuns          int                 `json:"max_active_runs"`
	MissedRun              string              `json:"missed_run"`
	ExecutionWindowSeconds int64               `json:"execution_window_seconds"`
	DestinationIDs         []string            `json:"destination_ids"`
	EndAt                  *time.Time          `json:"end_at,omitempty"`
	MaxOccurrences         int                 `json:"max_occurrences,omitempty"`
	GoalID                 string              `json:"goal_id,omitempty"`
	SkillRef               *DefinitionRef      `json:"skill_ref,omitempty"`
}

// Automation is the contract Automation projection.
type Automation struct {
	ID                     string         `json:"id"`
	Revision               int64          `json:"revision"`
	Title                  string         `json:"title"`
	Instruction            string         `json:"instruction"`
	Status                 string         `json:"status"`
	Trigger                Trigger        `json:"trigger"`
	ConnectionIDs          []string       `json:"connection_ids"`
	AuthorityRuleIDs       []string       `json:"authority_rule_ids"`
	BudgetUSD              float64        `json:"budget_usd"`
	MaxActiveRuns          int            `json:"max_active_runs"`
	MissedRun              string         `json:"missed_run"`
	ExecutionWindowSeconds int64          `json:"execution_window_seconds"`
	DestinationIDs         []string       `json:"destination_ids"`
	NextAt                 *time.Time     `json:"next_at,omitempty"`
	EndAt                  *time.Time     `json:"end_at,omitempty"`
	MaxOccurrences         int            `json:"max_occurrences,omitempty"`
	GoalID                 string         `json:"goal_id,omitempty"`
	SkillRef               *DefinitionRef `json:"skill_ref,omitempty"`
	LastOccurrenceID       string         `json:"last_occurrence_id,omitempty"`
	SnoozedUntil           *time.Time     `json:"snoozed_until,omitempty"`
	LastOccurrence         *Occurrence    `json:"last_occurrence,omitempty"`
	AuthoritySummary       string         `json:"authority_summary,omitempty"`
	CreatedAt              *time.Time     `json:"created_at,omitempty"`
	UpdatedAt              *time.Time     `json:"updated_at,omitempty"`
}

// stored is the private record: the projection plus the schedule cursor.
type stored struct {
	Automation
	// Generation advances on every edit; occurrences of an older generation that were not accepted are superseded.
	Generation int64 `json:"generation"`
	// Cursor is the latest nominal slot already turned into an occurrence (or skipped). Edits never move it back,
	// so an edited schedule cannot duplicate an accepted slot.
	Cursor *time.Time `json:"cursor,omitempty"`
	// Created counts occurrences for max_occurrences.
	Created int `json:"created"`
	// EventStart caches an event-relative trigger's source time and revision.
	EventStart     *time.Time `json:"event_start,omitempty"`
	EventRevision  string     `json:"event_revision,omitempty"`
	EventStatus    string     `json:"event_status,omitempty"`
	EventCheckedAt *time.Time `json:"event_checked_at,omitempty"`
	// Checkpoint is a monitor's last meaningful observation, carried into its next run.
	Checkpoint string `json:"checkpoint,omitempty"`
}

// Action is the contract AutomationAction value.
type Action struct {
	Action string     `json:"action"`
	Until  *time.Time `json:"until,omitempty"`
}

type ErrorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// Occurrence is the contract Occurrence.
type Occurrence struct {
	ID                 string       `json:"id"`
	AutomationID       string       `json:"automation_id"`
	AutomationRevision int64        `json:"automation_revision"`
	Generation         int64        `json:"generation"`
	ScheduledAt        time.Time    `json:"scheduled_at"`
	SourceRevision     string       `json:"source_revision,omitempty"`
	Status             string       `json:"status"`
	TaskIDs            []string     `json:"task_ids"`
	DeliveryIDs        []string     `json:"delivery_ids"`
	StartedAt          *time.Time   `json:"started_at,omitempty"`
	FinishedAt         *time.Time   `json:"finished_at,omitempty"`
	Summary            string       `json:"summary,omitempty"`
	Error              *ErrorDetail `json:"error,omitempty"`
	Reason             string       `json:"reason,omitempty"`
	Deliveries         []Delivery   `json:"deliveries,omitempty"`
	// Instruction is the exact text the occurrence's task was given; private.
	Instruction string `json:"-"`
}

type occurrenceDoc struct {
	Occurrence
	Instruction string  `json:"instruction"`
	BudgetUSD   float64 `json:"budget_usd"`
	GoalID      string  `json:"goal_id,omitempty"`
	Monitor     bool    `json:"monitor,omitempty"`
}

// Delivery is the contract Delivery.
type Delivery struct {
	ID             string       `json:"id"`
	OccurrenceID   string       `json:"occurrence_id,omitempty"`
	ResultIDs      []string     `json:"result_ids,omitempty"`
	TaskID         string       `json:"task_id,omitempty"`
	DestinationID  string       `json:"destination_id"`
	State          string       `json:"state"`
	AttemptID      string       `json:"attempt_id,omitempty"`
	Receipt        string       `json:"receipt,omitempty"`
	AcknowledgedAt *time.Time   `json:"acknowledged_at,omitempty"`
	ScheduledFor   *time.Time   `json:"scheduled_for,omitempty"`
	CreatedAt      *time.Time   `json:"created_at,omitempty"`
	UpdatedAt      *time.Time   `json:"updated_at,omitempty"`
	Error          *ErrorDetail `json:"error,omitempty"`
}

// Destination is the contract DeliveryDestination.
type Destination struct {
	ID               string      `json:"id"`
	Revision         int64       `json:"revision"`
	Channel          string      `json:"channel"`
	Label            string      `json:"label"`
	Status           string      `json:"status"`
	SupportsReceipts bool        `json:"supports_receipts,omitempty"`
	QuietHours       *QuietHours `json:"quiet_hours,omitempty"`
}

type QuietHours struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	Timezone string `json:"timezone"`
}

// InboxItem is the contract InboxItem.
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

// Preview is the contract AutomationPreview.
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

// Goal is the contract Goal.
type Goal struct {
	ID                 string      `json:"id"`
	Revision           int64       `json:"revision"`
	Title              string      `json:"title"`
	Outcome            string      `json:"outcome"`
	CompletionCriteria []Criterion `json:"completion_criteria"`
	Status             string      `json:"status"`
	Progress           string      `json:"progress"`
	NextActions        []string    `json:"next_actions"`
	Blockers           []Blocker   `json:"blockers"`
	Linked             GoalLinks   `json:"linked"`
	Deadline           *time.Time  `json:"deadline,omitempty"`
	FollowUp           string      `json:"follow_up,omitempty"`
	AuthorityRuleIDs   []string    `json:"authority_rule_ids,omitempty"`
	CreatedAt          *time.Time  `json:"created_at,omitempty"`
	UpdatedAt          *time.Time  `json:"updated_at,omitempty"`
	CompletedAt        *time.Time  `json:"completed_at,omitempty"`
}

type Criterion struct {
	ID                string   `json:"id"`
	Text              string   `json:"text"`
	Met               bool     `json:"met"`
	EvidenceResultIDs []string `json:"evidence_result_ids,omitempty"`
}

type Blocker struct {
	Text          string `json:"text"`
	InteractionID string `json:"interaction_id,omitempty"`
	ConnectionID  string `json:"connection_id,omitempty"`
}

type GoalLinks struct {
	TaskIDs       []string `json:"task_ids"`
	AutomationIDs []string `json:"automation_ids"`
	ResultIDs     []string `json:"result_ids"`
}

// GoalEditable is the contract GoalEditable.
type GoalEditable struct {
	Title              string `json:"title"`
	Outcome            string `json:"outcome"`
	CompletionCriteria []struct {
		ID   string `json:"id,omitempty"`
		Text string `json:"text"`
	} `json:"completion_criteria"`
	Deadline *time.Time `json:"deadline,omitempty"`
	FollowUp string     `json:"follow_up,omitempty"`
}

// GoalAction is the contract GoalAction value.
type GoalAction struct {
	Action       string `json:"action"`
	KeepMonitors bool   `json:"keep_monitors,omitempty"`
}
