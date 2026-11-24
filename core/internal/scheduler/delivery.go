package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/state"
)

func deliveryID(occurrence, destination string) string {
	return "dlv_" + strings.ReplaceAll(uuid.NewSHA1(uuid.NameSpaceOID, []byte(occurrence+"|"+destination)).String(), "-", "")[:24]
}

// deliver records one delivery per destination, deduplicated by occurrence and destination. In-app delivery
// stores an inbox item; any non-inbox destination fails visibly.
func (s *Service) deliver(ctx context.Context, a stored, o occurrenceDoc, run state.Run, summary, status string, results []string) ([]string, error) {
	destinations := a.DestinationIDs
	if len(destinations) == 0 {
		destinations = []string{InAppDestination}
	}
	title := a.Title
	body := summary
	switch status {
	case "failed":
		body = "This run did not finish: " + clip(run.Error, 400)
	case "cancelled":
		body = "This run was stopped."
	}
	if body == "" {
		body = "Finished."
	}
	link := InboxLink{Kind: "task", ID: run.ID}
	var ids []string
	for _, destinationID := range destinations {
		id := deliveryID(o.ID, destinationID)
		ids = append(ids, id)
		if _, err := s.Store.Resource(ctx, CollectionDeliveries, id); err == nil {
			continue
		}
		now := s.now()
		d := Delivery{ID: id, OccurrenceID: o.ID, ResultIDs: results, TaskID: run.ID, DestinationID: destinationID, State: "pending", CreatedAt: &now}
		if destinationID == InAppDestination {
			item := InboxItem{ID: "inb_" + strings.TrimPrefix(id, "dlv_"), DeliveryID: id, Title: title, Body: clip(body, 4000), Link: link, CreatedAt: now, Revision: 1}
			resource, err := state.NewResource(CollectionInbox, item.ID, 0, item, map[string]string{"status": "unread"}, now)
			if err != nil {
				return nil, err
			}
			if _, err = s.Store.PutResource(ctx, resource, 0); err != nil && !errors.Is(err, state.ErrRevisionConflict) {
				return nil, err
			}
			d.State, d.Receipt = "delivered", item.ID
		} else {
			d.State, d.Error = "failed", &ErrorDetail{Code: "destination_missing", Message: "The destination is not supported."}
		}
		d.UpdatedAt = &now
		resource, err := state.NewResource(CollectionDeliveries, id, 0, d, map[string]string{"occurrence_id": o.ID, "state": d.State}, now)
		if err != nil {
			return nil, err
		}
		if _, err = s.Store.PutResource(ctx, resource, 0); err != nil && !errors.Is(err, state.ErrRevisionConflict) {
			return nil, err
		}
	}
	return ids, nil
}

func (s *Service) Delivery(ctx context.Context, id string) (Delivery, error) {
	r, err := s.Store.Resource(ctx, CollectionDeliveries, id)
	if err != nil {
		return Delivery{}, err
	}
	var d Delivery
	err = r.Decode(&d)
	return d, err
}

// Inbox lists inbox items, newest first.
func (s *Service) Inbox(ctx context.Context, unread bool) ([]InboxItem, error) {
	filter := map[string]string{}
	if unread {
		filter["status"] = "unread"
	}
	resources, err := s.allResources(ctx, CollectionInbox, filter, 200)
	if err != nil {
		return nil, err
	}
	out := make([]InboxItem, 0, len(resources))
	for _, r := range resources {
		var item InboxItem
		if err := r.Decode(&item); err != nil {
			return nil, err
		}
		item.Revision = r.Revision
		out = append(out, item)
	}
	slices.SortStableFunc(out, func(a, b InboxItem) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out, nil
}

// Acknowledge marks an inbox item read and records the acknowledgement on its delivery.
func (s *Service) Acknowledge(ctx context.Context, id, requestID string, expected int64) (state.MutationReceipt, error) {
	now := s.now()
	return s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "POST", Target: "/v1/inbox/" + id + "/actions", ExpectedRevision: expected,
		Payload: map[string]string{"action": "acknowledge"}, Collection: CollectionInbox, ID: id,
		Apply: func(current *state.Resource, read func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			var item InboxItem
			if err := current.Decode(&item); err != nil {
				return state.MutationResult{}, err
			}
			item.AcknowledgedAt, item.Revision = &now, current.Revision+1
			resource, err := state.NewResource(CollectionInbox, id, item.Revision, item, map[string]string{"status": "read"}, now)
			if err != nil {
				return state.MutationResult{}, err
			}
			writes := []state.Resource{resource}
			if item.DeliveryID != "" {
				if dr, err := read(CollectionDeliveries, item.DeliveryID); err == nil && dr != nil {
					var d Delivery
					if dr.Decode(&d) == nil {
						d.AcknowledgedAt, d.UpdatedAt = &now, &now
						if next, err := state.NewResource(CollectionDeliveries, d.ID, dr.Revision+1, d, dr.Index, now); err == nil {
							writes = append(writes, next)
						}
					}
				}
			}
			data, _ := json.Marshal(item)
			return state.MutationResult{Writes: writes, Receipt: state.MutationReceipt{Status: "applied", Resource: data, HTTPStatus: 200}}, nil
		},
	})
}

// UnreadCount counts unread inbox items for navigation badges.
func (s *Service) UnreadCount(ctx context.Context) int {
	items, err := s.Inbox(ctx, true)
	if err != nil {
		return 0
	}
	return len(items)
}

// NextScheduled is the earliest next run of an enabled automation.
func (s *Service) NextScheduled(ctx context.Context) *time.Time {
	all, err := s.automations(ctx, "enabled")
	if err != nil {
		return nil
	}
	var earliest *time.Time
	for _, a := range all {
		if a.NextAt != nil && (earliest == nil || a.NextAt.Before(*earliest)) {
			next := *a.NextAt
			earliest = &next
		}
	}
	return earliest
}

// NextWake returns the earliest time the scheduler lane needs to run again,
// covering both enabled automation schedules (NextScheduled) and any pending
// occurrences or terminal-task active occurrences awaiting settlement/delivery.
func (s *Service) NextWake(ctx context.Context) *time.Time {
	now := s.now()
	earliest := s.NextScheduled(ctx)
	if earliest != nil && !earliest.After(now) {
		return earliest
	}
	if pending, _, err := s.Store.Resources(ctx, CollectionOccurrences, map[string]string{"status": "pending"}, "", 1); err == nil && len(pending) > 0 {
		return &now
	}
	if s.Tasks != nil {
		for _, status := range []string{"accepted", "running"} {
			occs, err := s.allResources(ctx, CollectionOccurrences, map[string]string{"status": status}, 200)
			if err != nil {
				continue
			}
			for _, r := range occs {
				var o occurrenceDoc
				if r.Decode(&o) != nil || len(o.TaskIDs) == 0 {
					continue
				}
				if run, err := s.Tasks.GetRun(ctx, o.TaskIDs[0]); err == nil && run.Status != state.RunRunning {
					return &now
				}
			}
		}
	}
	return earliest
}
