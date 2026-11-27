package invariants

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"nori/internal/permissions"
	"nori/internal/scheduler"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

type rawResourceDoc struct {
	ID        string            `firestore:"id"`
	Revision  int64             `firestore:"revision"`
	Body      string            `firestore:"body"`
	Index     map[string]string `firestore:"index,omitempty"`
	CreatedAt time.Time         `firestore:"created_at"`
	UpdatedAt time.Time         `firestore:"updated_at"`
}

type storedAutomation struct {
	scheduler.Automation
	Generation int64      `json:"generation"`
	Cursor     *time.Time `json:"cursor,omitempty"`
	Created    int        `json:"created"`
}

type storedOccurrence struct {
	scheduler.Occurrence
	Instruction string  `json:"instruction"`
	BudgetUSD   float64 `json:"budget_usd"`
	Monitor     bool    `json:"monitor,omitempty"`
}

type rawInteractionRecord struct {
	resource    state.Resource
	interaction permissions.Interaction
}

type rawOperationRecord struct {
	docID            string
	op               state.OperationRecord
	evidenceRecorded bool
}

type snapshotResourceStore struct {
	snap  memstore.BackendSnapshot
	owner string
}

var _ state.ResourceStore = (*snapshotResourceStore)(nil)

func (s *snapshotResourceStore) collectionPath(collection string) string {
	return "owners/" + s.owner + "/r_" + collection
}

func (s *snapshotResourceStore) Resource(_ context.Context, collection, id string) (state.Resource, error) {
	if !state.ValidResourceID(id) {
		return state.Resource{}, state.ErrNotFound
	}
	docs := s.snap.Collections[s.collectionPath(collection)]
	fields, ok := docs[id]
	if !ok {
		return state.Resource{}, state.ErrNotFound
	}
	return decodeResourceFromFields(collection, fields)
}

func (s *snapshotResourceStore) Resources(_ context.Context, collection string, filter map[string]string, after string, limit int) ([]state.Resource, string, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	docs := s.snap.Collections[s.collectionPath(collection)]
	ids := sortedKeys(docs)
	var out []state.Resource
	for _, id := range ids {
		if after != "" && id <= after {
			continue
		}
		r, err := decodeResourceFromFields(collection, docs[id])
		if err != nil {
			return nil, "", err
		}
		if !matchesResourceFilter(r.Index, filter) {
			continue
		}
		if len(out) == limit {
			return out, out[len(out)-1].ID, nil
		}
		out = append(out, r)
	}
	return out, "", nil
}

func (s *snapshotResourceStore) Mutate(context.Context, state.Mutation) (state.MutationReceipt, error) {
	return state.MutationReceipt{}, errors.New("invariants: snapshotResourceStore is read-only")
}

func (s *snapshotResourceStore) Receipt(context.Context, string) (state.MutationReceipt, error) {
	return state.MutationReceipt{}, state.ErrNotFound
}

func (s *snapshotResourceStore) PutResource(context.Context, state.Resource, int64) (state.Resource, error) {
	return state.Resource{}, errors.New("invariants: snapshotResourceStore is read-only")
}

func matchesResourceFilter(index, filter map[string]string) bool {
	for k, v := range filter {
		if v == "" {
			continue
		}
		if index[k] != v {
			return false
		}
	}
	return true
}

func decodeResourceFromFields(collection string, fields map[string]any) (state.Resource, error) {
	var stored rawResourceDoc
	if err := docstore.Decode(fields, &stored); err != nil {
		return state.Resource{}, err
	}
	return state.Resource{
		Collection: collection,
		ID:         stored.ID,
		Revision:   stored.Revision,
		Body:       json.RawMessage(stored.Body),
		Index:      stored.Index,
		CreatedAt:  stored.CreatedAt,
		UpdatedAt:  stored.UpdatedAt,
	}, nil
}

func decodeControlFromSnapshot(snap memstore.BackendSnapshot) (state.Control, []Violation) {
	var ctrl state.Control
	if sys, ok := snap.Collections["system"]; ok {
		if fields, ok := sys["control"]; ok {
			if err := docstore.Decode(fields, &ctrl); err != nil {
				return state.Control{}, []Violation{{
					Invariant: "control.decode",
					EntityID:  "system/control",
					Message:   fmt.Sprintf("failed to decode system control document: %v", err),
				}}
			}
		}
	}
	return ctrl, nil
}

func decodeRunsFromSnapshot(snap memstore.BackendSnapshot, owner string) (map[string]state.Run, []Violation) {
	col := "owners/" + owner + "/runs"
	docs := snap.Collections[col]
	runs := make(map[string]state.Run, len(docs))
	var vs []Violation
	for _, id := range sortedKeys(docs) {
		var run state.Run
		if err := docstore.Decode(docs[id], &run); err != nil {
			vs = append(vs, Violation{
				Invariant: "run.decode",
				EntityID:  id,
				Message:   fmt.Sprintf("failed to decode run document: %v", err),
			})
			continue
		}
		if run.ID != id {
			vs = append(vs, Violation{
				Invariant: "run.id_matches_doc",
				EntityID:  id,
				Message:   fmt.Sprintf("run document ID %q does not match Run.ID %q", id, run.ID),
			})
		}
		runs[id] = run
	}
	return runs, vs
}

func decodeOperationsFromSnapshot(snap memstore.BackendSnapshot, owner, runID string) ([]rawOperationRecord, []Violation) {
	col := "owners/" + owner + "/runs/" + runID + "/operations"
	docs := snap.Collections[col]
	if len(docs) == 0 {
		return nil, nil
	}
	var ops []rawOperationRecord
	var vs []Violation
	for _, id := range sortedKeys(docs) {
		fields := docs[id]
		var op state.OperationRecord
		if err := docstore.Decode(fields, &op); err != nil {
			vs = append(vs, Violation{
				Invariant: "operation.decode",
				EntityID:  runID + "/" + id,
				Message:   fmt.Sprintf("failed to decode operation document: %v", err),
			})
			continue
		}
		recorded, _ := fields["evidence_recorded"].(bool)
		ops = append(ops, rawOperationRecord{
			docID:            id,
			op:               op,
			evidenceRecorded: recorded,
		})
	}
	return ops, vs
}

func decodeCommandsFromSnapshot(snap memstore.BackendSnapshot, owner string) ([]state.Command, []Violation) {
	col := "owners/" + owner + "/task_commands"
	docs := snap.Collections[col]
	if len(docs) == 0 {
		return nil, nil
	}
	var cmds []state.Command
	var vs []Violation
	for _, id := range sortedKeys(docs) {
		var cmd state.Command
		if err := docstore.Decode(docs[id], &cmd); err != nil {
			vs = append(vs, Violation{
				Invariant: "command.decode",
				EntityID:  id,
				Message:   fmt.Sprintf("failed to decode command document: %v", err),
			})
			continue
		}
		if cmd.ID != id {
			vs = append(vs, Violation{
				Invariant: "command.id_matches_doc",
				EntityID:  id,
				Message:   fmt.Sprintf("command document ID %q does not match Command.ID %q", id, cmd.ID),
			})
		}
		cmds = append(cmds, cmd)
	}
	return cmds, vs
}

func decodeMessagesFromSnapshot(snap memstore.BackendSnapshot, owner string) ([]state.Message, []Violation) {
	col := "owners/" + owner + "/messages"
	docs := snap.Collections[col]
	if len(docs) == 0 {
		return nil, nil
	}
	var msgs []state.Message
	var vs []Violation
	for _, id := range sortedKeys(docs) {
		var msg state.Message
		if err := docstore.Decode(docs[id], &msg); err != nil {
			vs = append(vs, Violation{
				Invariant: "message.decode",
				EntityID:  id,
				Message:   fmt.Sprintf("failed to decode message document: %v", err),
			})
			continue
		}
		msgs = append(msgs, msg)
	}
	return msgs, vs
}

func decodeReadMarkFromSnapshot(snap memstore.BackendSnapshot, owner string) (state.ReadMark, bool, error) {
	col := snap.Collections["owners/"+owner+"/conversation"]
	fields, ok := col["seen"]
	if !ok {
		return state.ReadMark{}, false, nil
	}
	var mark state.ReadMark
	if err := docstore.Decode(fields, &mark); err != nil {
		return state.ReadMark{}, true, err
	}
	return mark, true, nil
}

func decodeConversationFromSnapshot(snap memstore.BackendSnapshot, owner string) (state.ConversationControl, bool, error) {
	col := snap.Collections["owners/"+owner+"/conversation"]
	fields, ok := col["control"]
	if !ok {
		return state.ConversationControl{}, false, nil
	}
	var ctrl state.ConversationControl
	if err := docstore.Decode(fields, &ctrl); err != nil {
		return state.ConversationControl{}, true, err
	}
	return ctrl, true, nil
}

func decodeInteractionsFromSnapshot(snap memstore.BackendSnapshot, owner string) ([]rawInteractionRecord, []Violation) {
	col := "owners/" + owner + "/r_" + permissions.CollectionInteractions
	docs := snap.Collections[col]
	if len(docs) == 0 {
		return nil, nil
	}
	var out []rawInteractionRecord
	var vs []Violation
	for _, id := range sortedKeys(docs) {
		r, err := decodeResourceFromFields(permissions.CollectionInteractions, docs[id])
		if err != nil {
			vs = append(vs, Violation{
				Invariant: "interaction.decode",
				EntityID:  id,
				Message:   fmt.Sprintf("failed to decode interaction resource: %v", err),
			})
			continue
		}
		var inter permissions.Interaction
		if err := r.Decode(&inter); err != nil {
			vs = append(vs, Violation{
				Invariant: "interaction.decode",
				EntityID:  id,
				Message:   fmt.Sprintf("failed to decode interaction body: %v", err),
			})
			continue
		}
		inter.Revision = r.Revision
		out = append(out, rawInteractionRecord{resource: r, interaction: inter})
	}
	return out, vs
}

func decodeAutomationsFromSnapshot(snap memstore.BackendSnapshot, owner string) (map[string]storedAutomation, []Violation) {
	col := "owners/" + owner + "/r_" + scheduler.CollectionAutomations
	docs := snap.Collections[col]
	out := make(map[string]storedAutomation, len(docs))
	var vs []Violation
	for _, id := range sortedKeys(docs) {
		r, err := decodeResourceFromFields(scheduler.CollectionAutomations, docs[id])
		if err != nil {
			vs = append(vs, Violation{
				Invariant: "automation.decode",
				EntityID:  id,
				Message:   fmt.Sprintf("failed to decode automation resource: %v", err),
			})
			continue
		}
		var a storedAutomation
		if err := r.Decode(&a); err != nil {
			vs = append(vs, Violation{
				Invariant: "automation.decode",
				EntityID:  id,
				Message:   fmt.Sprintf("failed to decode automation body: %v", err),
			})
			continue
		}
		a.Revision = r.Revision
		out[id] = a
	}
	return out, vs
}

func decodeOccurrencesFromSnapshot(snap memstore.BackendSnapshot, owner string) ([]storedOccurrence, []Violation) {
	col := "owners/" + owner + "/r_" + scheduler.CollectionOccurrences
	docs := snap.Collections[col]
	if len(docs) == 0 {
		return nil, nil
	}
	var out []storedOccurrence
	var vs []Violation
	for _, id := range sortedKeys(docs) {
		r, err := decodeResourceFromFields(scheduler.CollectionOccurrences, docs[id])
		if err != nil {
			vs = append(vs, Violation{
				Invariant: "occurrence.decode",
				EntityID:  id,
				Message:   fmt.Sprintf("failed to decode occurrence resource: %v", err),
			})
			continue
		}
		var o storedOccurrence
		if err := r.Decode(&o); err != nil {
			vs = append(vs, Violation{
				Invariant: "occurrence.decode",
				EntityID:  id,
				Message:   fmt.Sprintf("failed to decode occurrence body: %v", err),
			})
			continue
		}
		out = append(out, o)
	}
	return out, vs
}

func extractOwnerFromPath(path string) (string, bool) {
	if !strings.HasPrefix(path, "owners/") {
		return "", false
	}
	rest := strings.TrimPrefix(path, "owners/")
	if rest == "" {
		return "", false
	}
	owner, _, _ := strings.Cut(rest, "/")
	return owner, owner != ""
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
