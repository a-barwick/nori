package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

const (
	MaxTaskOperations      = 4096
	MaxCompletionChecks    = 512
	MaxEvidenceRecordBytes = 128 << 10
	MaxEvidenceReferences  = 256
	MaxSourceInputs        = 512
)

type OperationRecord struct {
	ID          string   `json:"id" firestore:"id"`
	CallID      string   `json:"call_id" firestore:"call_id"`
	Name        string   `json:"name" firestore:"name"`
	EffectClass string   `json:"effect_class" firestore:"effect_class"`
	Outcome     string   `json:"outcome" firestore:"outcome"`
	ResultRef   string   `json:"result_ref" firestore:"result_ref"`
	Revision    int64    `json:"revision" firestore:"revision"`
	Attempt     int      `json:"attempt" firestore:"attempt"`
	ArtifactIDs []string `json:"artifact_ids" firestore:"artifact_ids"`
	Action      *Action  `json:"action,omitempty" firestore:"action,omitempty"`
	// Cleared says why a failed or uncertain operation no longer blocks later ones. The outcome and result stay as saved.
	Cleared string `json:"cleared,omitempty" firestore:"cleared,omitempty"`
}

const (
	// ClearedDidNotRun is a command that never started.
	ClearedDidNotRun = "did_not_run"
	// ClearedReadOnly is a shell command the task declared would only read.
	ClearedReadOnly = "read_only"
	// ClearedAnswered is an operation named in a question the user answered.
	ClearedAnswered = "answered"
)

// Action is the requested write. Digest covers the tool, account and Spec.
type Action struct {
	Digest string            `json:"digest" firestore:"digest"`
	Text   string            `json:"text" firestore:"text"`
	Spec   map[string]string `json:"spec" firestore:"spec"`
}

type CompletionProposal struct {
	Answer   string   `json:"answer" firestore:"answer"`
	Evidence []string `json:"evidence" firestore:"evidence"`
	Unmet    []string `json:"unmet" firestore:"unmet"`
}

type CompletionCheck struct {
	ID       string             `json:"id" firestore:"id"`
	Revision int64              `json:"revision" firestore:"revision"`
	Position int64              `json:"position" firestore:"position"`
	Proposal CompletionProposal `json:"proposal" firestore:"proposal"`
}

func ValidEvidenceID(id string) bool {
	return id != "" && len(id) <= 256 && id != "." && id != ".." && !strings.ContainsAny(id, "/\\\x00")
}

func evidenceSize(value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) > MaxEvidenceRecordBytes {
		return errors.New("task evidence record exceeds 128 KiB or is invalid")
	}
	return nil
}

func validEvidenceIDs(ids []string) bool {
	if len(ids) > MaxEvidenceReferences {
		return false
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !ValidEvidenceID(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func boundedEvidenceText(values []string) bool {
	if len(values) > MaxEvidenceReferences {
		return false
	}
	for _, value := range values {
		if len(value) > MaxMessageBytes {
			return false
		}
	}
	return true
}

func validOperation(op OperationRecord) bool {
	if !ValidEvidenceID(op.ID) || !ValidEvidenceID(op.CallID) || op.Name == "" || len(op.Name) > 256 || op.Revision < 0 ||
		op.Attempt < 0 || op.Attempt > 64 || !validEvidenceIDs(op.ArtifactIDs) || (op.ResultRef != "" && !ValidHistoryRef(op.ResultRef)) {
		return false
	}
	switch op.EffectClass {
	case "read", "reversible_write", "sensitive_send", "unknown", "control":
	default:
		return false
	}
	switch op.Outcome {
	case "pending", "confirmed", "failed", "uncertain", "skipped", "ready", "attempted", "submitted", "voided":
		return true
	default:
		return false
	}
}

func CheckEvidenceCount(count int64) error {
	if count < 0 {
		return errors.New("invalid task evidence count")
	}
	return nil
}

// CheckOperationCapacity and CheckCompletionCapacity enforce the per-task record caps. count is the number of records
// already kept, so a task at the cap cannot add another.
func CheckOperationCapacity(count int64) error {
	if count >= MaxTaskOperations {
		return errors.New("task exceeds 4096 operation records")
	}
	return nil
}

func CheckCompletionCapacity(count int64) error {
	if count >= MaxCompletionChecks {
		return errors.New("task exceeds 512 completion checks")
	}
	return nil
}

// ValidateOperation checks a write request for an operation record and returns the form to save.
func ValidateOperation(runID string, op OperationRecord) (OperationRecord, error) {
	if !ValidEvidenceID(runID) || !validOperation(op) {
		return op, errors.New("invalid task operation record")
	}
	if err := evidenceSize(op); err != nil {
		return op, err
	}
	if len(op.ArtifactIDs) == 0 {
		op.ArtifactIDs = nil
	}
	return op, nil
}

// SavedOperation is an operation document as stored. EvidenceRecorded is false for a record that only proves the
// operation was admitted.
type SavedOperation struct {
	Record           OperationRecord
	EvidenceRecorded bool
}

// LateWriteEvent reports a provider answer saved after the task's terminal event.
// The caller validates the transition and commits this event with the operation.
func LateWriteEvent(run Run, saved *SavedOperation, op OperationRecord, now time.Time) (TaskEvent, bool) {
	if run.Status == RunRunning || saved == nil || saved.Record.Outcome == op.Outcome ||
		op.Action == nil || op.Attempt == 0 || op.ResultRef == "" {
		return TaskEvent{}, false
	}
	var text string
	switch op.Outcome {
	case "confirmed":
		text = "The provider confirmed this requested write."
	case "submitted":
		text = "The provider accepted this requested write, but a follow-up read did not complete."
	case "failed":
		text = "The provider refused this requested write or a later read showed it did not happen."
	case "uncertain":
		text = "This requested write may have happened; its outcome could not be confirmed."
	default:
		return TaskEvent{}, false
	}
	return TaskEvent{
		ID: run.ID + "-write-" + op.ID + "-" + op.Outcome, RunID: run.ID, Kind: "write_outcome",
		Sequence: run.EventSequence + 1, Revision: run.Revision, CreatedAt: now,
		Text: text + " " + op.Action.Text, Reason: fmt.Sprintf("The task remains %s.", run.Status),
		OperationRefs: []string{op.ID},
	}, true
}

type OperationWrite struct {
	Noop bool
	// NewRecord is set when this is the first evidence for the operation, so it counts toward the task's cap.
	NewRecord bool
}

// PlanOperation decides whether op may be saved over the stored record, if any. Evidence is immutable once recorded,
// except that an uncertain outcome can later resolve to confirmed or failed.
func PlanOperation(saved *SavedOperation, op OperationRecord) (OperationWrite, error) {
	if saved == nil {
		return OperationWrite{NewRecord: true}, nil
	}
	old := saved.Record
	if old.ID != op.ID || old.Revision != op.Revision {
		return OperationWrite{}, errors.New("operation admission identity conflict")
	}
	if !saved.EvidenceRecorded {
		return OperationWrite{NewRecord: true}, nil
	}
	if len(old.ArtifactIDs) == 0 {
		old.ArtifactIDs = nil
	}
	if reflect.DeepEqual(old, op) {
		return OperationWrite{Noop: true}, nil
	}
	if old.CallID != op.CallID || old.Name != op.Name || old.EffectClass != op.EffectClass || op.Attempt < old.Attempt ||
		(old.Outcome != "pending" && old.Outcome != "ready" && old.Attempt != op.Attempt) {
		return OperationWrite{}, errors.New("operation evidence identity conflict")
	}
	if old.Action != nil && (op.Action == nil || old.Action.Digest != op.Action.Digest) {
		return OperationWrite{}, errors.New("operation evidence identity conflict")
	}
	if old.Cleared != "" && old.Cleared != op.Cleared {
		return OperationWrite{}, errors.New("operation evidence identity conflict")
	}
	if old.Cleared == "" && op.Cleared != "" && old.Outcome == op.Outcome && (op.Outcome == "failed" || op.Outcome == "uncertain") {
		cleared := old
		cleared.Cleared = op.Cleared
		if reflect.DeepEqual(cleared, op) {
			return OperationWrite{}, nil
		}
	}
	if !outcomeMayFollow(old.Outcome, op.Outcome) {
		return OperationWrite{}, errors.New("recorded operation outcome is immutable")
	}
	return OperationWrite{}, nil
}

// outcomeMayFollow lists the only changes to a saved outcome. A write leaves attempted only for what the provider
// shows, and nothing leaves a terminal outcome.
func outcomeMayFollow(old, next string) bool {
	switch old {
	case "pending":
		return true
	case "ready":
		return next == "attempted" || next == "voided" || next == "failed"
	case "attempted":
		return next == "submitted" || next == "confirmed" || next == "failed" || next == "uncertain"
	case "submitted":
		return next == "confirmed"
	case "uncertain":
		return next == "confirmed" || next == "failed"
	}
	return false
}

// ReadOperation validates a stored operation for reading. A record that only proves admission shows an attempt, never
// a completed effect.
func ReadOperation(op OperationRecord, evidenceRecorded bool, documentID string) (OperationRecord, error) {
	if !evidenceRecorded {
		op.EffectClass, op.Outcome = "unknown", "uncertain"
		return op, nil
	}
	if !validOperation(op) || op.ID != documentID {
		return op, errors.New("invalid saved operation evidence")
	}
	return op, nil
}

// ValidateCompletionCheck checks a completion check write and returns the form to save.
func ValidateCompletionCheck(runID string, check CompletionCheck) (CompletionCheck, error) {
	if !ValidEvidenceID(runID) || !ValidEvidenceID(check.ID) || check.Revision < 0 || check.Position < 0 ||
		!boundedEvidenceText(check.Proposal.Evidence) ||
		!boundedEvidenceText(check.Proposal.Unmet) {
		return check, errors.New("invalid task completion check")
	}
	if err := evidenceSize(check); err != nil {
		return check, err
	}
	return normalizeCompletionCheck(check), nil
}

func normalizeCompletionCheck(check CompletionCheck) CompletionCheck {
	if len(check.Proposal.Evidence) == 0 {
		check.Proposal.Evidence = nil
	}
	if len(check.Proposal.Unmet) == 0 {
		check.Proposal.Unmet = nil
	}
	return check
}

// CheckCompletionIdentity ensures a retried save does not replace a different result.
func CheckCompletionIdentity(saved, check CompletionCheck) error {
	if reflect.DeepEqual(normalizeCompletionCheck(saved), check) {
		return nil
	}
	return errors.New("completion check identity conflict")
}

func SourceInputIDs(commands []Command) ([]string, error) {
	seen := map[string]bool{}
	ids := []string{}
	for _, command := range commands {
		for _, id := range command.SourceInputIDs {
			if !ValidEvidenceID(id) {
				return nil, errors.New("invalid task source input reference")
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			if len(ids) >= MaxSourceInputs {
				return nil, fmt.Errorf("task source inputs exceed %d records", MaxSourceInputs)
			}
			ids = append(ids, id)
		}
	}
	return ids, nil
}

type SourceInputs struct {
	inputs []ConversationInput
	total  int
}

func (s *SourceInputs) Add(documentID string, input ConversationInput) error {
	if input.ID != documentID || !input.IsUser() {
		return errors.New("invalid saved task source input")
	}
	s.total += len(input.Text)
	if s.total > MaxHistoryBlockBytes {
		return errors.New("task source inputs exceed 512 KiB")
	}
	s.inputs = append(s.inputs, input)
	return nil
}

func (s *SourceInputs) Inputs() []ConversationInput {
	inputs := append([]ConversationInput{}, s.inputs...)
	sort.Slice(inputs, func(i, j int) bool {
		if inputs[i].Position == inputs[j].Position {
			return inputs[i].ID < inputs[j].ID
		}
		return inputs[i].Position < inputs[j].Position
	})
	return inputs
}

// CitedResult is a bounded copy of a saved tool result that a task's answer cites.
type CitedResult struct {
	Operation string `json:"operation"`
	Tool      string `json:"tool"`
	Result    string `json:"result"`
}

// TaskOutcome is what a task's last completion proposal claims and cites.
type TaskOutcome struct {
	Unmet     []string      `json:"unmet,omitempty"`
	Evidence  []CitedResult `json:"evidence,omitempty"`
	Shortened bool          `json:"shortened,omitempty"`
}

const (
	maxOutcomeEvidenceBytes = 8 << 10
	maxOutcomeUnmet         = 32
)

// LastProposal returns the completion check that describes how the task ended: the latest by revision, position and
// then ID.
func LastProposal(checks []CompletionCheck) (last CompletionCheck, ok bool) {
	found := false
	for _, check := range checks {
		// Check IDs end in an increasing number, so a longer ID is a later check.
		if !found || check.Revision > last.Revision || check.Revision == last.Revision &&
			(check.Position > last.Position || check.Position == last.Position &&
				(len(check.ID) > len(last.ID) || len(check.ID) == len(last.ID) && check.ID > last.ID)) {
			last, found = check, true
		}
	}
	if !found {
		return CompletionCheck{}, false
	}
	return last, true
}

func NewTaskOutcome(proposal CompletionCheck) TaskOutcome {
	outcome := TaskOutcome{Unmet: proposal.Proposal.Unmet}
	if len(outcome.Unmet) > maxOutcomeUnmet {
		outcome.Unmet, outcome.Shortened = outcome.Unmet[:maxOutcomeUnmet], true
	}
	return outcome
}

// CitedOperations lists, in the order given, the operations with a saved result that the proposal cites by operation,
// result or artifact ID.
func CitedOperations(proposal CompletionCheck, ops []OperationRecord) []OperationRecord {
	cited := map[string]bool{}
	for _, ref := range proposal.Proposal.Evidence {
		cited[ref] = true
	}
	var found []OperationRecord
	for _, op := range ops {
		relevant := cited[op.ID] || cited[op.ResultRef]
		for _, id := range op.ArtifactIDs {
			relevant = relevant || cited[id]
		}
		if relevant && op.ResultRef != "" {
			found = append(found, op)
		}
	}
	return found
}

func (o *TaskOutcome) FitEvidence() {
	if len(o.Evidence) == 0 {
		return
	}
	share := maxOutcomeEvidenceBytes / len(o.Evidence)
	for i := range o.Evidence {
		if text := o.Evidence[i].Result; len(text) > share {
			// Page text matters at its start and job output at its end, so keep both.
			o.Evidence[i].Result = strings.ToValidUTF8(text[:share/2], "") + "…" + strings.ToValidUTF8(text[len(text)-share/2:], "")
			o.Shortened = true
		}
	}
}
