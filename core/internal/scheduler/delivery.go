package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/state"
)

func deliveryID(occurrence, destination string) string {
	return "dlv_" + strings.ReplaceAll(uuid.NewSHA1(uuid.NameSpaceOID, []byte(occurrence+"|"+destination)).String(), "-", "")[:24]
}

func (s *Service) deliver(
	ctx context.Context,
	a stored,
	o occurrenceDoc,
	run state.Run,
	outcome ProactiveOutcome,
	summary string,
	status OccurrenceStatus,
	results []string,
	pausedOnFailure bool,
) ([]string, error) {
	destinations := a.DestinationIDs
	if len(destinations) == 0 {
		destinations = []string{InAppDestination}
	}
	title := a.Title
	body := summary
	switch status {
	case OccurrenceFailed:
		if pausedOnFailure {
			body = fmt.Sprintf("Paused %q after %d consecutive failures: %s", a.Title, s.failurePauseThreshold(), clip(run.Error, 400))
		} else {
			body = "This run did not finish: " + clip(run.Error, 400)
		}
	case OccurrenceCancelled:
		body = "This run was stopped."
	}
	if body == "" {
		body = "Finished."
	}
	var ids []string
	for _, destinationID := range destinations {
		id := deliveryID(o.ID, destinationID)
		ids = append(ids, id)
		if _, err := s.Store.Resource(ctx, CollectionDeliveries, id); err == nil {
			continue
		}
		now := s.now()
		d := Delivery{
			ID:            id,
			OccurrenceID:  o.ID,
			AutomationID:  a.ID,
			ResultIDs:     results,
			TaskID:        run.ID,
			DestinationID: destinationID,
			State:         DeliveryStatePending,
			Title:         title,
			Body:          clip(body, 4000),
			Outcome:       outcome.Outcome,
			FindingKeys:   outcome.FindingKeys,
			InteractionID: outcome.InteractionID,
			CreatedAt:     &now,
		}
		// Hold non-Morning-Brief background deliveries during Quiet Hours until quiet_end.
		// Explicit manual run_now occurrences deliver immediately because the user requested them now.
		if inQuiet, quietEnd := s.quietHoursCheck(ctx, now); inQuiet && a.ID != MorningBriefAutomationID && o.Source != "run_now" {
			d.State = DeliveryStateHeld
			d.ScheduledFor = &quietEnd
			d.UpdatedAt = &now
			resource, err := state.NewResource(CollectionDeliveries, id, 0, d, map[string]string{"occurrence_id": o.ID, "state": DeliveryStateHeld}, now)
			if err != nil {
				return nil, err
			}
			if _, err = s.Store.PutResource(ctx, resource, 0); err != nil && !errors.Is(err, state.ErrRevisionConflict) {
				return nil, err
			}
			if s.Arm != nil {
				s.Arm(context.WithoutCancel(ctx), quietEnd)
			}
			continue
		}

		if err := s.dispatchDelivery(ctx, &d, now); err != nil {
			return nil, err
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

func (s *Service) dispatchDelivery(ctx context.Context, d *Delivery, now time.Time) error {
	switch d.DestinationID {
	case InAppDestination:
		if err := s.deliverInboxItem(ctx, d, now); err != nil {
			return err
		}
	case ChatDestination:
		if err := s.deliverChat(ctx, d, now); err != nil {
			return err
		}
	default:
		d.State = DeliveryStateFailed
		d.Error = &ErrorDetail{Code: "destination_missing", Message: "The destination is not supported."}
		return nil
	}
	if d.State == DeliveryStateDelivered && len(d.FindingKeys) > 0 {
		_ = s.recordFindingSurfaced(ctx, d.FindingKeys, d.InteractionID, now, false)
	}
	if d.State == DeliveryStateDelivered && s.OnPush != nil {
		_ = s.OnPush(ctx, *d)
	}
	return nil
}

func (s *Service) deliverInboxItem(ctx context.Context, d *Delivery, now time.Time) error {
	link := InboxLink{Kind: "task", ID: d.TaskID}
	item := InboxItem{
		ID:         "inb_" + strings.TrimPrefix(d.ID, "dlv_"),
		DeliveryID: d.ID,
		Title:      d.Title,
		Body:       clip(d.Body, 4000),
		Link:       link,
		CreatedAt:  now,
		Revision:   1,
	}
	resource, err := state.NewResource(CollectionInbox, item.ID, 0, item, map[string]string{"status": "unread"}, now)
	if err != nil {
		return err
	}
	if _, err = s.Store.PutResource(ctx, resource, 0); err != nil && !errors.Is(err, state.ErrRevisionConflict) {
		return err
	}
	d.State, d.Receipt = DeliveryStateDelivered, item.ID
	return nil
}

func (s *Service) deliverChat(ctx context.Context, d *Delivery, now time.Time) error {
	text := strings.TrimSpace(d.Body)
	if d.Title != "" && !strings.HasPrefix(strings.ToLower(text), strings.ToLower(d.Title)) {
		text = fmt.Sprintf("**%s**\n\n%s", d.Title, text)
	}
	// Watch alerts with EnqueueProactiveEvent wired emit a surfaced task event
	// into the conversation queue; otherwise append directly to Chat.
	if d.Outcome == OutcomeAlert && s.EnqueueProactiveEvent != nil {
		event := state.TaskEvent{
			ID:        d.ID,
			RunID:     d.TaskID,
			Kind:      "proactive_alert",
			Text:      text,
			CreatedAt: now,
		}
		if err := s.EnqueueProactiveEvent(ctx, event); err != nil {
			return err
		}
		d.State = DeliveryStateDelivered
		d.Receipt = state.TaskEventInputID(d.ID)
		return nil
	}
	if s.AppendProactiveMessage != nil {
		msg := state.Message{
			Role:      "assistant",
			Status:    state.MessageStatusCompleted,
			RunID:     d.TaskID,
			Text:      text,
			Parts:     []state.Part{{Type: state.PartText, Text: text}},
			ResultIDs: d.ResultIDs,
			CreatedAt: now,
		}
		appended, err := s.AppendProactiveMessage(ctx, d.ID, msg)
		if err != nil {
			return err
		}
		d.State = DeliveryStateDelivered
		d.Receipt = appended.ID
		return nil
	}
	return s.deliverInboxItem(ctx, d, now)
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

func (s *Service) UnreadCount(ctx context.Context) int {
	items, err := s.Inbox(ctx, true)
	if err != nil {
		return 0
	}
	return len(items)
}

func (s *Service) NextScheduled(ctx context.Context) *time.Time {
	all, err := s.automations(ctx, string(AutomationEnabled))
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

func (s *Service) NextWake(ctx context.Context) *time.Time {
	now := s.now()
	earliest := s.NextScheduled(ctx)
	if earliest != nil && !earliest.After(now) {
		return earliest
	}
	if pending, _, err := s.Store.Resources(ctx, CollectionOccurrences, map[string]string{"status": string(OccurrencePending)}, "", 1); err == nil && len(pending) > 0 {
		return &now
	}
	if s.Tasks != nil {
		for _, status := range []OccurrenceStatus{OccurrenceAccepted, OccurrenceRunning} {
			occs, err := s.allResources(ctx, CollectionOccurrences, map[string]string{"status": string(status)}, 200)
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
	if _, held, err := s.heldDeliveryResources(ctx); err == nil {
		for _, d := range held {
			if d.ScheduledFor != nil && (earliest == nil || d.ScheduledFor.Before(*earliest)) {
				t := *d.ScheduledFor
				earliest = &t
			}
		}
	}
	return earliest
}
