package firestore

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"nori/internal/state"
)

// Counters live separately from the run because run projections replace the run document. They cap authoritative
// records without growing the run's arrays.
func (d *Database) evidenceCount(tx *firestore.Transaction, runID, kind string) (*firestore.DocumentRef, int64, error) {
	ref := d.runs().Doc(runID).Collection("evidence_metadata").Doc(kind)
	doc, err := tx.Get(ref)
	if notFound(err) {
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
	if err := state.CheckEvidenceCount(count.Count); err != nil {
		return nil, 0, err
	}
	return ref, count.Count, nil
}

func (d *Database) PutOperation(ctx context.Context, runID string, op state.OperationRecord) error {
	op, err := state.ValidateOperation(runID, op)
	if err != nil {
		return err
	}
	if op.ResultRef != "" {
		if _, err := d.readHistoryBlock(ctx, runID, op.ResultRef); err != nil {
			return fmt.Errorf("operation result reference unavailable: %w", err)
		}
	}
	return d.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		if _, err := tx.Get(d.runs().Doc(runID)); err != nil {
			return err
		}
		ref := d.runs().Doc(runID).Collection("operations").Doc(op.ID)
		doc, err := tx.Get(ref)
		var saved *state.SavedOperation
		switch {
		case err == nil:
			var old state.OperationRecord
			if err := doc.DataTo(&old); err != nil {
				return err
			}
			recorded, _ := doc.Data()["evidence_recorded"].(bool)
			saved = &state.SavedOperation{Record: old, EvidenceRecorded: recorded}
		case !notFound(err):
			return err
		}
		write, err := state.PlanOperation(saved, op)
		if err != nil || write.Noop {
			return err
		}
		var countRef *firestore.DocumentRef
		var count int64
		if write.NewRecord {
			if countRef, count, err = d.evidenceCount(tx, runID, "operations"); err != nil {
				return err
			}
			if err = state.CheckOperationCapacity(count); err != nil {
				return err
			}
		}
		// Merge preserves admitted_at and any existing receipt/admission metadata.
		data := map[string]any{
			"id": op.ID, "call_id": op.CallID, "name": op.Name, "effect_class": op.EffectClass, "outcome": op.Outcome,
			"result_ref": op.ResultRef, "revision": op.Revision, "attempt": op.Attempt, "artifact_ids": op.ArtifactIDs,
			"evidence_recorded": true,
		}
		if op.Action != nil {
			data["action"] = op.Action
		}
		if err := tx.Set(ref, data, firestore.MergeAll); err != nil {
			return err
		}
		if write.NewRecord {
			return tx.Set(countRef, map[string]any{"count": count + 1})
		}
		return nil
	})
}

func (d *Database) Operations(ctx context.Context, runID string) ([]state.OperationRecord, error) {
	if !state.ValidEvidenceID(runID) {
		return nil, errors.New("invalid task operation task")
	}
	if _, err := d.GetRun(ctx, runID); err != nil {
		return nil, err
	}
	it := d.runs().Doc(runID).Collection("operations").OrderBy(firestore.DocumentID, firestore.Asc).Limit(state.MaxTaskOperations + 1).Documents(ctx)
	defer it.Stop()
	operations := []state.OperationRecord{}
	for {
		doc, err := it.Next()
		if isDone(err) {
			break
		}
		if err != nil {
			return nil, err
		}
		if err = state.CheckOperationCapacity(int64(len(operations))); err != nil {
			return nil, err
		}
		var op state.OperationRecord
		if err = doc.DataTo(&op); err != nil {
			return nil, err
		}
		recorded, _ := doc.Data()["evidence_recorded"].(bool)
		if op, err = state.ReadOperation(op, recorded, doc.Ref.ID); err != nil {
			return nil, err
		}
		operations = append(operations, op)
	}
	return operations, nil
}

func (d *Database) PutCompletionCheck(ctx context.Context, runID string, check state.CompletionCheck) error {
	check, err := state.ValidateCompletionCheck(runID, check)
	if err != nil {
		return err
	}
	return d.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		if _, err := tx.Get(d.runs().Doc(runID)); err != nil {
			return err
		}
		ref := d.runs().Doc(runID).Collection("completion_checks").Doc(check.ID)
		doc, err := tx.Get(ref)
		if err == nil {
			var saved state.CompletionCheck
			if err := doc.DataTo(&saved); err != nil {
				return err
			}
			write, err := state.PlanCompletionCheck(saved, check)
			if err != nil {
				return err
			}
			if write == state.CompletionFinalize {
				return tx.Set(ref, check)
			}
			return nil
		}
		if !notFound(err) {
			return err
		}
		countRef, count, err := d.evidenceCount(tx, runID, "completion_checks")
		if err != nil {
			return err
		}
		if err := state.CheckCompletionCapacity(count); err != nil {
			return err
		}
		if err := tx.Create(ref, check); err != nil {
			return err
		}
		return tx.Set(countRef, map[string]any{"count": count + 1})
	})
}

// ReadTaskSourceInputs resolves exact retained inputs instead of relying on the bounded recent-conversation view.
// Direct task commands remain in Reconcile.
func (d *Database) ReadTaskSourceInputs(ctx context.Context, runID string) ([]state.ConversationInput, error) {
	if !state.ValidEvidenceID(runID) {
		return nil, errors.New("invalid task source task")
	}
	snapshot, err := d.Reconcile(ctx, runID)
	if err != nil {
		return nil, err
	}
	ids, err := state.SourceInputIDs(snapshot.Commands)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []state.ConversationInput{}, nil
	}
	refs := make([]*firestore.DocumentRef, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, d.conversationInputs().Doc(id))
	}
	docs, err := d.db.GetAll(ctx, refs)
	if err != nil {
		return nil, err
	}
	var sources state.SourceInputs
	for _, doc := range docs {
		if !doc.Exists() {
			return nil, errors.New("task source input unavailable")
		}
		var input state.ConversationInput
		if err := doc.DataTo(&input); err != nil {
			return nil, err
		}
		if err := sources.Add(doc.Ref.ID, input); err != nil {
			return nil, err
		}
	}
	return sources.Inputs(), nil
}

// ReadTaskOutcome reads the last proposal's unmet items and the results it cites.
func (d *Database) ReadTaskOutcome(ctx context.Context, runID string) (state.TaskOutcome, error) {
	if !state.ValidEvidenceID(runID) {
		return state.TaskOutcome{}, errors.New("invalid task outcome task")
	}
	checks, err := documents[state.CompletionCheck](
		d.runs().Doc(runID).Collection("completion_checks").Limit(state.MaxCompletionChecks + 1).Documents(ctx))
	if err != nil {
		return state.TaskOutcome{}, err
	}
	proposal, ok := state.LastProposal(checks)
	if !ok {
		return state.TaskOutcome{}, nil
	}
	outcome := state.NewTaskOutcome(proposal)
	ops, err := d.Operations(ctx, runID)
	if err != nil {
		return state.TaskOutcome{}, err
	}
	for _, op := range state.CitedOperations(proposal, ops) {
		blocks, err := d.ReadHistory(ctx, runID, op.ResultRef)
		if err != nil {
			return state.TaskOutcome{}, err
		}
		outcome.Evidence = append(outcome.Evidence, state.CitedResult{
			Operation: op.ID, Tool: op.Name, Result: string(bytes.Join(blocks, []byte("\n"))),
		})
	}
	outcome.FitEvidence()
	return outcome, nil
}

func (d *Database) readHistoryBlock(ctx context.Context, runID, ref string) (state.HistoryBlock, error) {
	var block state.HistoryBlock
	if !state.ValidHistoryLookup(runID, ref) {
		return block, errors.New("invalid task history reference")
	}
	doc, err := d.runs().Doc(runID).Collection("history").Doc(ref).Get(ctx)
	if err != nil {
		return block, err
	}
	if err := doc.DataTo(&block); err != nil {
		return block, err
	}
	if err := state.ValidateSavedHistoryBlock(runID, ref, block); err != nil {
		return state.HistoryBlock{}, err
	}
	return block, nil
}

func (d *Database) AppendHistory(ctx context.Context, runID, parent string, data []byte) (string, error) {
	if err := state.ValidateHistoryAppend(runID, parent, data); err != nil {
		return "", err
	}
	if _, err := d.GetRun(ctx, runID); err != nil {
		return "", err
	}
	var previous *state.HistoryBlock
	if parent != "" {
		block, err := d.readHistoryBlock(ctx, runID, parent)
		if err != nil {
			return "", fmt.Errorf("task history parent unavailable: %w", err)
		}
		previous = &block
	}
	block, ref, err := state.NextHistoryBlock(runID, parent, previous, data)
	if err != nil {
		return "", err
	}
	_, err = d.runs().Doc(runID).Collection("history").Doc(ref).Create(ctx, block)
	if status.Code(err) == codes.AlreadyExists {
		saved, readErr := d.readHistoryBlock(ctx, runID, ref)
		if readErr != nil {
			return "", readErr
		}
		if err := state.CheckHistoryReplay(saved, block); err != nil {
			return "", err
		}
		return ref, nil
	}
	if err != nil {
		return "", err
	}
	return ref, nil
}

// ReadHistory returns exact payloads in chronological order. Missing, foreign, malformed or over-limit chains fail
// closed rather than return partial context.
func (d *Database) ReadHistory(ctx context.Context, runID, ref string) ([][]byte, error) {
	if !state.ValidEvidenceID(runID) {
		return nil, errors.New("invalid task history task")
	}
	if _, err := d.GetRun(ctx, runID); err != nil {
		return nil, err
	}
	chain := state.NewHistoryChain()
	for ref != "" {
		if err := chain.Visit(ref); err != nil {
			return nil, err
		}
		block, err := d.readHistoryBlock(ctx, runID, ref)
		if err != nil {
			return nil, err
		}
		if ref, err = chain.Add(block); err != nil {
			return nil, err
		}
	}
	return chain.Payloads()
}
