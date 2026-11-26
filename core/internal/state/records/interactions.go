package records

import (
	"cmp"
	"encoding/json"
	"slices"
	"time"

	"nori/internal/state"
	"nori/internal/state/docstore"
)

const CollectionInteractions = "interactions"

type pendingInteraction struct {
	resource state.Resource
	runID    string
}

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
	slices.SortFunc(pending, func(a, b pendingInteraction) int {
		return cmp.Compare(a.resource.ID, b.resource.ID)
	})
	return pending, nil
}

func (d *Database) supersedeInteractions(tx *docstore.Transaction, pending []pendingInteraction, before map[string]int64, batch state.CommandBatch, commands []state.Command) error {
	answered := map[string]string{}
	for _, cmd := range commands {
		if cmd.QuestionID != "" {
			answered[cmd.QuestionID] = cmd.ID
		}
	}
	now := d.now()
	for _, p := range pending {
		run, ok := batch.Runs[p.runID]
		_, isAnswered := answered[p.resource.ID]
		if !ok || run.Revision == before[p.runID] && !isAnswered {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(p.resource.Body, &body); err != nil {
			return err
		}
		status := "superseded"
		if commandID, ok := answered[p.resource.ID]; ok {
			status = "resolved"
			for _, cmd := range commands {
				if cmd.ID == commandID {
					body["resolution"] = map[string]any{"kind": "answer", "text": cmd.Text, "at": now.Format(time.RFC3339Nano)}
				}
			}
		} else {
			delete(body, "resolution")
			for _, cmd := range commands {
				if cmd.RunID == p.runID {
					body["superseded_by"] = cmd.ID
				}
			}
		}
		body["status"] = status
		body["resolved_at"] = now.Format(time.RFC3339Nano)
		next := p.resource
		next.Revision++
		next.Index = map[string]string{}
		for key, value := range p.resource.Index {
			next.Index[key] = value
		}
		next.Index["status"] = status
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
