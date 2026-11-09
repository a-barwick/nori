package records

import (
	"encoding/json"
	"time"

	"nori/internal/state"
	"nori/internal/state/docstore"
)

// CollectionInteractions holds pending questions, approvals, account choices, sign-ins and computer takeovers.
const CollectionInteractions = "interactions"

type pendingInteraction struct {
	resource state.Resource
	runID    string
}

// readPendingInteractions reads, inside the acceptance transaction, the pending interactions of the tasks a batch
// names, so a correction can supersede them in the same commit that advances the task's revision.
func (d *Database) readPendingInteractions(tx *docstore.Transaction, runIDs []string) ([]pendingInteraction, error) {
	var pending []pendingInteraction
	for _, id := range runIDs {
		query := d.resources(CollectionInteractions).Where("index.task_id", "==", id).Where("index.status", "==", "pending")
		docs, err := tx.Documents(query).GetAll()
		if err != nil {
			return nil, err
		}
		for _, doc := range docs {
			r, err := toResource(CollectionInteractions, doc)
			if err != nil {
				return nil, err
			}
			pending = append(pending, pendingInteraction{resource: r, runID: id})
		}
	}
	return pending, nil
}

// supersedeInteractions marks pending interactions superseded when their task's instruction revision advanced and
// the batch did not answer them. An approval for revision N can then never authorize work at revision N+1.
func (d *Database) supersedeInteractions(tx *docstore.Transaction, pending []pendingInteraction, before map[string]int64, batch state.CommandBatch, commands []state.Command) error {
	answered := map[string]string{}
	for _, cmd := range commands {
		if cmd.QuestionID != "" {
			answered[cmd.QuestionID] = cmd.ID
		}
	}
	now := time.Now().UTC()
	for _, p := range pending {
		run, ok := batch.Runs[p.runID]
		if !ok || run.Revision == before[p.runID] {
			continue
		}
		if _, ok := answered[p.resource.ID]; ok {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(p.resource.Body, &body); err != nil {
			return err
		}
		body["status"] = "superseded"
		body["resolution"] = "superseded"
		body["resolved_at"] = now.Format(time.RFC3339Nano)
		for _, cmd := range commands {
			if cmd.RunID == p.runID {
				body["superseded_by"] = cmd.ID
			}
		}
		next := p.resource
		next.Revision++
		next.Index = map[string]string{}
		for key, value := range p.resource.Index {
			next.Index[key] = value
		}
		next.Index["status"] = "superseded"
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		next.Body, next.UpdatedAt = data, now
		if err := tx.Set(d.resources(CollectionInteractions).Doc(next.ID), fromResource(next)); err != nil {
			return err
		}
	}
	return nil
}
