package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	MaxTaskOperations      = 4096
	MaxCompletionChecks    = 512
	MaxEvidenceRecordBytes = 128 << 10
	MaxEvidenceReferences  = 256
)

// TaskEvidenceStore is separate from display tracing: successful writes here
// are required before an operation or completion can be treated as recorded.
type TaskEvidenceStore interface {
	AppendHistory(context.Context, string, string, []byte) (string, error)
	ReadHistory(context.Context, string, string) ([][]byte, error)
	PutOperation(context.Context, string, OperationRecord) error
	Operations(context.Context, string) ([]OperationRecord, error)
	PutCompletionCheck(context.Context, string, CompletionCheck) error
}

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
}

type CompletionProposal struct {
	Answer   string   `json:"answer" firestore:"answer"`
	Evidence []string `json:"evidence" firestore:"evidence"`
	Unmet    []string `json:"unmet" firestore:"unmet"`
}

type CompletionCheck struct {
	ID                string             `json:"id" firestore:"id"`
	Revision          int64              `json:"revision" firestore:"revision"`
	Position          int64              `json:"position" firestore:"position"`
	Proposal          CompletionProposal `json:"proposal" firestore:"proposal"`
	RecordFailures    []string           `json:"record_failures" firestore:"record_failures"`
	JevChoice         string             `json:"jev_choice" firestore:"jev_choice"`
	JevScore          float64            `json:"jev_score" firestore:"jev_score"`
	JevAvailable      bool               `json:"jev_available" firestore:"jev_available"`
	CorrectionAttempt int                `json:"correction_attempt" firestore:"correction_attempt"`
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
		if !validEvidenceID(id) || seen[id] {
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
	if !validEvidenceID(op.ID) || !validEvidenceID(op.CallID) || op.Name == "" || len(op.Name) > 256 || op.Revision < 0 || op.Attempt < 0 || op.Attempt > 64 || !validEvidenceIDs(op.ArtifactIDs) || (op.ResultRef != "" && !validHistoryRef(op.ResultRef)) {
		return false
	}
	switch op.EffectClass {
	case "read", "reversible_write", "sensitive_send", "unknown":
	default:
		return false
	}
	switch op.Outcome {
	case "pending", "confirmed", "failed", "uncertain", "skipped":
		return true
	default:
		return false
	}
}

// Counters live separately from Run because existing run projections replace
// the run document. They cap authoritative records without growing Run arrays.
func (s *Google) evidenceCount(tx *firestore.Transaction, runID, kind string) (*firestore.DocumentRef, int64, error) {
	ref := s.runs().Doc(runID).Collection("evidence_metadata").Doc(kind)
	doc, err := tx.Get(ref)
	if status.Code(err) == codes.NotFound {
		return ref, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var count struct {
		Count int64 `firestore:"count"`
	}
	if err := doc.DataTo(&count); err != nil {
		return nil, 0, err
	}
	if count.Count < 0 {
		return nil, 0, errors.New("invalid task evidence count")
	}
	return ref, count.Count, nil
}

func (s *Google) PutOperation(ctx context.Context, runID string, op OperationRecord) error {
	if !validEvidenceID(runID) || !validOperation(op) {
		return errors.New("invalid task operation record")
	}
	if err := evidenceSize(op); err != nil {
		return err
	}
	if len(op.ArtifactIDs) == 0 {
		op.ArtifactIDs = nil
	}
	if op.ResultRef != "" {
		if _, err := s.readHistoryBlock(ctx, runID, op.ResultRef); err != nil {
			return fmt.Errorf("operation result reference unavailable: %w", err)
		}
	}
	return s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		if _, err := tx.Get(s.runs().Doc(runID)); err != nil {
			return err
		}
		ref := s.runs().Doc(runID).Collection("operations").Doc(op.ID)
		doc, err := tx.Get(ref)
		if err != nil && status.Code(err) != codes.NotFound {
			return err
		}
		recorded := false
		if err == nil {
			var old OperationRecord
			if err := doc.DataTo(&old); err != nil {
				return err
			}
			if old.ID != op.ID || old.Revision != op.Revision {
				return errors.New("operation admission identity conflict")
			}
			recorded, _ = doc.Data()["evidence_recorded"].(bool)
			if recorded {
				if len(old.ArtifactIDs) == 0 {
					old.ArtifactIDs = nil
				}
				if reflect.DeepEqual(old, op) {
					return nil
				}
				if old.CallID != op.CallID || old.Name != op.Name || old.EffectClass != op.EffectClass || op.Attempt < old.Attempt || (old.Outcome != "pending" && old.Attempt != op.Attempt) {
					return errors.New("operation evidence identity conflict")
				}
				if old.Outcome != "pending" && !(old.Outcome == "uncertain" && (op.Outcome == "confirmed" || op.Outcome == "failed")) {
					return errors.New("recorded operation outcome is immutable")
				}
			}
		}
		var countRef *firestore.DocumentRef
		var count int64
		if !recorded {
			countRef, count, err = s.evidenceCount(tx, runID, "operations")
			if err != nil {
				return err
			}
			if count >= MaxTaskOperations {
				return errors.New("task exceeds 4096 operation records")
			}
		}
		// Merge preserves admitted_at and any existing receipt/admission metadata.
		data := map[string]any{"id": op.ID, "call_id": op.CallID, "name": op.Name, "effect_class": op.EffectClass, "outcome": op.Outcome, "result_ref": op.ResultRef, "revision": op.Revision, "attempt": op.Attempt, "artifact_ids": op.ArtifactIDs, "evidence_recorded": true}
		if err := tx.Set(ref, data, firestore.MergeAll); err != nil {
			return err
		}
		if !recorded {
			return tx.Set(countRef, map[string]any{"count": count + 1})
		}
		return nil
	})
}

func (s *Google) Operations(ctx context.Context, runID string) ([]OperationRecord, error) {
	if !validEvidenceID(runID) {
		return nil, errors.New("invalid task operation task")
	}
	if _, err := s.GetRun(ctx, runID); err != nil {
		return nil, err
	}
	it := s.runs().Doc(runID).Collection("operations").OrderBy(firestore.DocumentID, firestore.Asc).Limit(MaxTaskOperations + 1).Documents(ctx)
	defer it.Stop()
	operations := []OperationRecord{}
	for {
		doc, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(operations) == MaxTaskOperations {
			return nil, errors.New("task exceeds 4096 operation records")
		}
		var op OperationRecord
		if err := doc.DataTo(&op); err != nil {
			return nil, err
		}
		recorded, _ := doc.Data()["evidence_recorded"].(bool)
		if !recorded {
			// Legacy admission records prove an attempt, never a completed effect.
			op.EffectClass, op.Outcome = "unknown", "uncertain"
		} else if !validOperation(op) || op.ID != doc.Ref.ID {
			return nil, errors.New("invalid saved operation evidence")
		}
		operations = append(operations, op)
	}
	return operations, nil
}

func (s *Google) PutCompletionCheck(ctx context.Context, runID string, check CompletionCheck) error {
	if !validEvidenceID(runID) || !validEvidenceID(check.ID) || check.Revision < 0 || check.Position < 0 || check.CorrectionAttempt < 0 || check.CorrectionAttempt > 64 || !boundedEvidenceText(check.Proposal.Evidence) || !boundedEvidenceText(check.Proposal.Unmet) || !boundedEvidenceText(check.RecordFailures) || math.IsNaN(check.JevScore) || math.IsInf(check.JevScore, 0) || check.JevScore < 0 || check.JevScore > 1 {
		return errors.New("invalid task completion check")
	}
	if check.JevAvailable && check.JevChoice != "yes" && check.JevChoice != "partly" && check.JevChoice != "no" {
		return errors.New("invalid completion choice")
	}
	if err := evidenceSize(check); err != nil {
		return err
	}
	if len(check.Proposal.Evidence) == 0 {
		check.Proposal.Evidence = nil
	}
	if len(check.Proposal.Unmet) == 0 {
		check.Proposal.Unmet = nil
	}
	if len(check.RecordFailures) == 0 {
		check.RecordFailures = nil
	}
	return s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		if _, err := tx.Get(s.runs().Doc(runID)); err != nil {
			return err
		}
		ref := s.runs().Doc(runID).Collection("completion_checks").Doc(check.ID)
		doc, err := tx.Get(ref)
		if err == nil {
			var old CompletionCheck
			if err := doc.DataTo(&old); err != nil {
				return err
			}
			if len(old.Proposal.Evidence) == 0 {
				old.Proposal.Evidence = nil
			}
			if len(old.Proposal.Unmet) == 0 {
				old.Proposal.Unmet = nil
			}
			if len(old.RecordFailures) == 0 {
				old.RecordFailures = nil
			}
			if reflect.DeepEqual(old, check) {
				return nil
			}
			if !old.JevAvailable && old.JevChoice == "" {
				final := old
				final.JevAvailable, final.JevChoice, final.JevScore = check.JevAvailable, check.JevChoice, check.JevScore
				if reflect.DeepEqual(final, check) {
					return tx.Set(ref, check)
				}
			}
			return errors.New("completion check identity conflict")
		}
		if status.Code(err) != codes.NotFound {
			return err
		}
		countRef, count, err := s.evidenceCount(tx, runID, "completion_checks")
		if err != nil {
			return err
		}
		if count >= MaxCompletionChecks {
			return errors.New("task exceeds 512 completion checks")
		}
		if err := tx.Create(ref, check); err != nil {
			return err
		}
		return tx.Set(countRef, map[string]any{"count": count + 1})
	})
}

// ReadTaskSourceInputs resolves exact retained inputs instead of relying on the
// bounded recent-conversation view. Direct task commands remain in Reconcile.
func (s *Google) ReadTaskSourceInputs(ctx context.Context, runID string) ([]ConversationInput, error) {
	if !validEvidenceID(runID) {
		return nil, errors.New("invalid task source task")
	}
	snapshot, err := s.Reconcile(ctx, runID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	refs := []*firestore.DocumentRef{}
	for _, command := range snapshot.Commands {
		for _, id := range command.SourceInputIDs {
			if !validEvidenceID(id) {
				return nil, errors.New("invalid task source input reference")
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			if len(refs) >= 512 {
				return nil, errors.New("task source inputs exceed 512 records")
			}
			refs = append(refs, s.conversationInputs().Doc(id))
		}
	}
	inputs := []ConversationInput{}
	if len(refs) == 0 {
		return inputs, nil
	}
	docs, err := s.db.GetAll(ctx, refs)
	if err != nil {
		return nil, err
	}
	total := 0
	for _, doc := range docs {
		if !doc.Exists() {
			return nil, errors.New("task source input unavailable")
		}
		var input ConversationInput
		if err := doc.DataTo(&input); err != nil {
			return nil, err
		}
		if input.ID != doc.Ref.ID || !input.IsUser() {
			return nil, errors.New("invalid saved task source input")
		}
		total += len(input.Text)
		if total > MaxHistoryBlockBytes {
			return nil, errors.New("task source inputs exceed 512 KiB")
		}
		inputs = append(inputs, input)
	}
	sort.Slice(inputs, func(i, j int) bool {
		if inputs[i].Position == inputs[j].Position {
			return inputs[i].ID < inputs[j].ID
		}
		return inputs[i].Position < inputs[j].Position
	})
	return inputs, nil
}

var _ TaskEvidenceStore = (*Google)(nil)
