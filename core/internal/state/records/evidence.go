package records

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"nori/internal/state"
	"nori/internal/state/docstore"
)

func (d *Database) evidenceCount(tx *docstore.Transaction, runID, kind string) (*docstore.DocumentRef, int64, error) {
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
	return d.putOperation(ctx, runID, op, false)
}

func (d *Database) SettleOperation(ctx context.Context, runID string, op state.OperationRecord) error {
	if op.ResultRef == "" {
		return errors.New("operation settlement requires a result")
	}
	return d.putOperation(ctx, runID, op, true)
}

func (d *Database) putOperation(ctx context.Context, runID string, op state.OperationRecord, settle bool) error {
	op, err := state.ValidateOperation(runID, op)
	if err != nil {
		return err
	}
	if op.ResultRef != "" {
		if _, err := d.readHistoryBlock(ctx, runID, op.ResultRef); err != nil {
			return fmt.Errorf("operation result reference unavailable: %w", err)
		}
	}
	return d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		run, err := decodeRun(tx.Get(d.runs().Doc(runID)))
		if err != nil {
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
			if settle && old.ResultRef != "" {
				return docstore.ErrExists
			}
			recorded, _ := doc.Data()["evidence_recorded"].(bool)
			saved = &state.SavedOperation{Record: old, EvidenceRecorded: recorded}
		case !notFound(err):
			return err
		}
		if saved != nil && saved.Record.CreatedAt != nil && op.CreatedAt == nil {
			op.CreatedAt = saved.Record.CreatedAt
		}
		write, err := state.PlanOperation(saved, op)
		if err != nil || write.Noop {
			return err
		}
		var countRef *docstore.DocumentRef
		var count int64
		if write.NewRecord {
			if countRef, count, err = d.evidenceCount(tx, runID, "operations"); err != nil {
				return err
			}
			if err = state.CheckOperationCapacity(count); err != nil {
				return err
			}
		}
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		if event, notify := state.LateWriteEvent(run, saved, op, d.now()); notify {
			inserted, err := d.appendTaskEvent(tx, run, event, run.TraceContext)
			if err != nil {
				return err
			}
			if inserted {
				if err = tx.Update(d.runs().Doc(runID), []docstore.Update{{Path: "event_sequence", Value: event.Sequence}}); err != nil {
					return err
				}
				if err = log.add(state.EventTask, runID); err != nil {
					return err
				}
				if err = log.add(state.EventConversation, ""); err != nil {
					return err
				}
			}
		}
		if err = log.add(state.EventOperation, runID+"/"+op.ID); err != nil {
			return err
		}
		data := map[string]any{
			"id": op.ID, "call_id": op.CallID, "name": op.Name, "effect_class": op.EffectClass, "outcome": op.Outcome,
			"result_ref": op.ResultRef, "revision": op.Revision, "attempt": op.Attempt, "artifact_ids": op.ArtifactIDs,
			"evidence_recorded": true,
		}
		if op.Action != nil {
			data["action"] = op.Action
		}
		if op.Cleared != "" {
			data["cleared"] = op.Cleared
		}
		if op.Tool != nil {
			data["tool"] = op.Tool
		}
		if op.ConnectionID != "" {
			data["connection_id"] = op.ConnectionID
		}
		if op.AuthorityRef != "" {
			data["authority_ref"] = op.AuthorityRef
		}
		if op.ParentOperationID != "" {
			data["parent_operation_id"] = op.ParentOperationID
		}
		if len(op.ProviderRefs) > 0 {
			data["provider_refs"] = op.ProviderRefs
		}
		if op.CreatedAt != nil {
			data["created_at"] = op.CreatedAt
		}
		if err := tx.Set(ref, data, docstore.MergeAll); err != nil {
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
	it := d.runs().Doc(runID).Collection("operations").OrderBy(docstore.DocumentID, docstore.Asc).Limit(state.MaxTaskOperations + 1).Documents(ctx)
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

func (d *Database) OperationsByRuns(ctx context.Context, runIDs []string) (map[string][]state.OperationRecord, error) {
	out := make(map[string][]state.OperationRecord, len(runIDs))
	if len(runIDs) == 0 {
		return out, nil
	}
	seen := make(map[string]struct{}, len(runIDs))
	collections := make([]string, 0, len(runIDs))
	runsPrefix := d.runs().Path + "/"
	for _, runID := range runIDs {
		if _, ok := seen[runID]; ok {
			continue
		}
		if !state.ValidEvidenceID(runID) {
			return nil, errors.New("invalid task operation task")
		}
		seen[runID] = struct{}{}
		collections = append(collections, d.runs().Doc(runID).Collection("operations").Path)
	}
	records, err := d.db.Backend.Query(ctx, docstore.QuerySpec{
		Collections: collections,
		Orders:      []docstore.Order{{Field: docstore.DocumentID, Direction: docstore.Asc}},
	})
	if err != nil {
		return nil, err
	}
	for _, rec := range records {
		rest, ok := strings.CutPrefix(rec.Path, runsPrefix)
		if !ok {
			continue
		}
		runID, tail, ok := strings.Cut(rest, "/operations/")
		if !ok || runID == "" || tail == "" {
			continue
		}
		if err = state.CheckOperationCapacity(int64(len(out[runID]))); err != nil {
			return nil, err
		}
		var op state.OperationRecord
		if err = docstore.Decode(rec.Fields, &op); err != nil {
			return nil, err
		}
		recorded, _ := rec.Fields["evidence_recorded"].(bool)
		if op, err = state.ReadOperation(op, recorded, tail); err != nil {
			return nil, err
		}
		out[runID] = append(out[runID], op)
	}
	return out, nil
}

func (d *Database) PutCompletionCheck(ctx context.Context, runID string, check state.CompletionCheck) error {
	check, err := state.ValidateCompletionCheck(runID, check)
	if err != nil {
		return err
	}
	return d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
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
			return state.CheckCompletionIdentity(saved, check)
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
	refs := make([]*docstore.DocumentRef, 0, len(ids))
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
	cite := func(owner string, ops []state.OperationRecord) error {
		for _, op := range state.CitedOperations(proposal, ops) {
			blocks, err := d.ReadHistory(ctx, owner, op.ResultRef)
			if err != nil {
				return err
			}
			result := bytes.Join(blocks, []byte("\n"))
			outcome.Evidence = append(outcome.Evidence, state.CitedResult{
				Operation: op.ID, Tool: op.Name, Summary: state.CitedSummary(result), Result: string(result),
			})
		}
		return nil
	}
	if err := cite(runID, ops); err != nil {
		return state.TaskOutcome{}, err
	}
	if run, err := d.GetRun(ctx, runID); err == nil {
		for hops := 0; hops < 8 && run.Origin != nil && run.Origin.ContinuesTaskID != ""; hops++ {
			previous := run.Origin.ContinuesTaskID
			earlier, err := d.Operations(ctx, previous)
			if err != nil {
				break
			}
			if err := cite(previous, earlier); err != nil {
				return state.TaskOutcome{}, err
			}
			if run, err = d.GetRun(ctx, previous); err != nil {
				break
			}
		}
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

func (d *Database) nextHistoryBlock(ctx context.Context, runID, parent string, data []byte) (state.HistoryBlock, string, error) {
	if err := state.ValidateHistoryAppend(runID, parent, data); err != nil {
		return state.HistoryBlock{}, "", err
	}
	if _, err := d.GetRun(ctx, runID); err != nil {
		return state.HistoryBlock{}, "", err
	}
	var previous *state.HistoryBlock
	if parent != "" {
		block, err := d.readHistoryBlock(ctx, runID, parent)
		if err != nil {
			return state.HistoryBlock{}, "", fmt.Errorf("task history parent unavailable: %w", err)
		}
		previous = &block
	}
	return state.NextHistoryBlock(runID, parent, previous, data)
}

func (d *Database) AppendHistory(ctx context.Context, runID, parent string, data []byte) (string, error) {
	block, ref, err := d.nextHistoryBlock(ctx, runID, parent, data)
	if err != nil {
		return "", err
	}
	_, err = d.runs().Doc(runID).Collection("history").Doc(ref).Create(ctx, block)
	if errors.Is(err, docstore.ErrExists) {
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

func (d *Database) StartTaskModelCheckpoint(ctx context.Context, runID string, checkpoint state.TaskModelCheckpoint) error {
	if !state.ValidEvidenceID(runID) || len(checkpoint.Outcome) != 0 {
		return errors.New("invalid task model intent")
	}
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	_, err := d.runs().Doc(runID).Collection("model_checkpoints").Doc(checkpoint.ID).Create(ctx, checkpoint)
	return err
}

func (d *Database) SaveTaskModelCheckpoint(ctx context.Context, runID string, checkpoint state.TaskModelCheckpoint, messages []byte) (string, error) {
	if len(checkpoint.Outcome) == 0 {
		return "", errors.New("task model settlement requires an outcome")
	}
	if err := checkpoint.Validate(); err != nil {
		return "", err
	}
	run := d.runs().Doc(runID)
	record := run.Collection("model_checkpoints").Doc(checkpoint.ID)
	ref := checkpoint.Parent
	var block state.HistoryBlock
	if len(messages) == 0 {
		if _, err := d.GetRun(ctx, runID); err != nil {
			return "", err
		}
	} else {
		var err error
		block, ref, err = d.nextHistoryBlock(ctx, runID, checkpoint.Parent, messages)
		if err != nil {
			return "", err
		}
	}
	checkpoint.Ref = ref
	err := d.db.RunTransaction(ctx, func(_ context.Context, tx *docstore.Transaction) error {
		doc, err := tx.Get(record)
		switch {
		case err == nil:
			var saved state.TaskModelCheckpoint
			if err := doc.DataTo(&saved); err != nil {
				return err
			}
			if saved.ID != checkpoint.ID || saved.Revision != checkpoint.Revision || saved.Parent != checkpoint.Parent || saved.Subtask != checkpoint.Subtask {
				return errors.New("task model checkpoint input conflict")
			}
			if len(saved.Outcome) != 0 || !saved.RecoveryUntil.Equal(checkpoint.RecoveryUntil) {
				return docstore.ErrExists
			}
		case !notFound(err):
			return err
		case !checkpoint.RecoveryUntil.IsZero():
			return state.ErrNotFound
		}
		if len(messages) == 0 {
			return tx.Set(record, checkpoint)
		}
		history := run.Collection("history").Doc(ref)
		doc, err = tx.Get(history)
		switch {
		case notFound(err):
			if err := tx.Create(history, block); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			var saved state.HistoryBlock
			if err := doc.DataTo(&saved); err != nil {
				return err
			}
			if err := state.ValidateSavedHistoryBlock(runID, ref, saved); err != nil {
				return err
			}
			if err := state.CheckHistoryReplay(saved, block); err != nil {
				return err
			}
		}
		return tx.Set(record, checkpoint)
	})
	return ref, err
}

func (d *Database) TaskModelCheckpoint(ctx context.Context, runID, id string) (state.TaskModelCheckpoint, error) {
	var checkpoint state.TaskModelCheckpoint
	if !state.ValidEvidenceID(runID) || !state.ValidEvidenceID(id) {
		return checkpoint, state.ErrNotFound
	}
	doc, err := d.runs().Doc(runID).Collection("model_checkpoints").Doc(id).Get(ctx)
	if notFound(err) {
		return checkpoint, state.ErrNotFound
	}
	if err != nil {
		return checkpoint, err
	}
	if err := doc.DataTo(&checkpoint); err != nil {
		return checkpoint, err
	}
	if checkpoint.ID != id {
		return checkpoint, errors.New("task model checkpoint identity conflict")
	}
	return checkpoint, checkpoint.Validate()
}

func (d *Database) Operation(ctx context.Context, runID, id string) (state.OperationRecord, error) {
	if !state.ValidEvidenceID(runID) || !state.ValidEvidenceID(id) {
		return state.OperationRecord{}, state.ErrNotFound
	}
	doc, err := d.runs().Doc(runID).Collection("operations").Doc(id).Get(ctx)
	if notFound(err) {
		return state.OperationRecord{}, state.ErrNotFound
	}
	if err != nil {
		return state.OperationRecord{}, err
	}
	var op state.OperationRecord
	if err = doc.DataTo(&op); err != nil {
		return op, err
	}
	recorded, _ := doc.Data()["evidence_recorded"].(bool)
	return state.ReadOperation(op, recorded, doc.Ref.ID)
}
