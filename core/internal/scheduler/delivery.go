package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/state"
)

// ErrNoTransport reports a channel whose provider is not configured in this deployment.
var ErrNoTransport = errors.New("no provider is configured for this delivery channel")

func deliveryID(occurrence, destination string) string {
	return "dlv_" + strings.ReplaceAll(uuid.NewSHA1(uuid.NameSpaceOID, []byte(occurrence+"|"+destination)).String(), "-", "")[:24]
}

// deliver records one delivery per destination, deduplicated by occurrence and destination. In-app delivery
// stores an inbox item; push and email go through their channel when one is configured and fail visibly otherwise.
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
		d := deliveryDoc{Delivery: Delivery{ID: id, OccurrenceID: o.ID, ResultIDs: results, TaskID: run.ID, DestinationID: destinationID, State: "pending", CreatedAt: &now},
			Title: title, Body: clip(body, 2000)}
		destination, err := s.Destination(ctx, destinationID)
		if err != nil {
			d.State, d.Error = "failed", &ErrorDetail{Code: "destination_missing", Message: "The destination no longer exists."}
		} else if destination.Channel == "in_app" {
			item := InboxItem{ID: "inb_" + strings.TrimPrefix(id, "dlv_"), DeliveryID: id, Title: title, Body: clip(body, 4000), Link: link, CreatedAt: now, Revision: 1}
			resource, err := state.NewResource(CollectionInbox, item.ID, 0, item, map[string]string{"status": "unread"}, now)
			if err != nil {
				return nil, err
			}
			if _, err = s.Store.PutResource(ctx, resource, 0); err != nil && !errors.Is(err, state.ErrRevisionConflict) {
				return nil, err
			}
			d.State, d.Receipt = "delivered", item.ID
		} else if destination.Status != "verified" {
			d.State, d.Error = "failed", &ErrorDetail{Code: "destination_unverified", Message: "This destination has not been verified."}
		} else {
			if deferred := quietUntil(destination.QuietHours, now); deferred != nil {
				// Quiet hours defer the message; the scheduler sends it when they end.
				d.ScheduledFor = deferred
				if s.Arm != nil {
					s.Arm(ctx, *deferred)
				}
			} else {
				s.sendDelivery(ctx, &d, destination, Message{Title: d.Title, Body: d.Body, URL: d.URL})
				if d.State == "pending" && d.ScheduledFor != nil && s.Arm != nil {
					s.Arm(ctx, *d.ScheduledFor)
				}
			}
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

// quietUntil returns when quiet hours end if now falls inside them. Quiet hours defer delivery; they never cancel
// execution.
func quietUntil(q *QuietHours, now time.Time) *time.Time {
	if q == nil {
		return nil
	}
	loc, err := time.LoadLocation(q.Timezone)
	if err != nil {
		return nil
	}
	local := now.In(loc)
	parse := func(text string) time.Time {
		t, _ := time.Parse("15:04", text)
		return time.Date(local.Year(), local.Month(), local.Day(), t.Hour(), t.Minute(), 0, 0, loc)
	}
	start, end := parse(q.Start), parse(q.End)
	if !end.After(start) {
		// Overnight quiet hours, e.g. 22:00–07:00.
		if local.Before(end) {
			return &end
		}
		if !local.Before(start) {
			next := end.AddDate(0, 0, 1)
			return &next
		}
		return nil
	}
	if !local.Before(start) && local.Before(end) {
		return &end
	}
	return nil
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

// Destination returns a delivery destination; the in-app inbox always exists and is verified.
func (s *Service) Destination(ctx context.Context, id string) (Destination, error) {
	if id == InAppDestination {
		return Destination{ID: InAppDestination, Revision: 1, Channel: "in_app", Label: "Nori inbox", Status: "verified", SupportsReceipts: true}, nil
	}
	r, err := s.Store.Resource(ctx, CollectionDestinations, id)
	if err != nil {
		return Destination{}, err
	}
	var d Destination
	err = r.Decode(&d)
	d.Revision = r.Revision
	return d, err
}

func (s *Service) Destinations(ctx context.Context) ([]Destination, error) {
	out := []Destination{}
	inApp, _ := s.Destination(ctx, InAppDestination)
	out = append(out, inApp)
	page, _, err := s.Store.Resources(ctx, CollectionDestinations, nil, "", 100)
	if err != nil {
		return nil, err
	}
	for _, r := range page {
		var d Destination
		if err := r.Decode(&d); err != nil {
			return nil, err
		}
		d.Revision = r.Revision
		out = append(out, d)
	}
	return out, nil
}

// DestinationEditable enrolls a push or email destination. It starts unverified until the user enters the code
// sent through the channel. Address (email) and Subscription (push) are write-only: they are stored with the
// credentials and never returned.
type DestinationEditable struct {
	Channel      string            `json:"channel"`
	Label        string            `json:"label"`
	Address      string            `json:"address,omitempty"`
	Subscription *PushSubscription `json:"subscription,omitempty"`
	QuietHours   *QuietHours       `json:"quiet_hours,omitempty"`
}

// PushSubscription is the browser's PushSubscription.toJSON() shape.
type PushSubscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func (s *Service) SaveDestination(ctx context.Context, id, requestID string, expected int64, value DestinationEditable) (state.MutationReceipt, error) {
	if value.Channel != "push" && value.Channel != "email" || strings.TrimSpace(value.Label) == "" {
		return state.MutationReceipt{}, fmt.Errorf("%w: a destination needs a push or email channel and a label", state.ErrInvalidResource)
	}
	method, target, status := "PUT", "/v1/delivery-destinations/"+id, 200
	if id == "" {
		id = "dest_" + strings.ReplaceAll(uuid.NewSHA1(uuid.NameSpaceOID, []byte("destination:"+requestID)).String(), "-", "")[:20]
		method, target, status = "POST", "/v1/delivery-destinations", 201
		if value.Address == "" && value.Subscription == nil {
			return state.MutationReceipt{}, fmt.Errorf("%w: a new destination needs an email address or a push subscription", state.ErrInvalidResource)
		}
	}
	// A new address is stored before the resource and must be verified again.
	reenrolled := value.Address != "" || value.Subscription != nil
	if reenrolled {
		if err := s.enroll(ctx, id, value.Channel, value); err != nil {
			return state.MutationReceipt{}, err
		}
	}
	// The receipt payload must not carry the private address.
	value.Address, value.Subscription = "", nil
	return s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: method, Target: target, ExpectedRevision: expected, Payload: value, Collection: CollectionDestinations, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			revision := int64(1)
			d := Destination{ID: id, Channel: value.Channel, Label: value.Label, Status: "pending_verification", QuietHours: value.QuietHours}
			if current != nil {
				revision = current.Revision + 1
				var old Destination
				_ = current.Decode(&old)
				if old.Channel == value.Channel && !reenrolled {
					d.Status = old.Status
				}
			}
			d.Revision = revision
			resource, err := state.NewResource(CollectionDestinations, id, revision, d, nil, s.now())
			if err != nil {
				return state.MutationResult{}, err
			}
			data, _ := json.Marshal(d)
			return state.MutationResult{Writes: []state.Resource{resource}, Receipt: state.MutationReceipt{Status: "applied", Resource: data, HTTPStatus: status}}, nil
		},
	})
}

func (s *Service) RemoveDestination(ctx context.Context, id, requestID string, expected int64) (state.MutationReceipt, error) {
	return s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "DELETE", Target: "/v1/delivery-destinations/" + id, ExpectedRevision: expected, Payload: id,
		Collection: CollectionDestinations, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			if s.Targets != nil {
				_ = s.Targets.Delete(ctx, targetKey(id))
			}
			return state.MutationResult{Deletes: []state.ResourceKey{{Collection: CollectionDestinations, ID: id}},
				Receipt: state.MutationReceipt{Status: "applied", Resource: current.Body, HTTPStatus: 200}}, nil
		},
	})
}

// Inbox lists inbox items, newest first.
func (s *Service) Inbox(ctx context.Context, unread bool) ([]InboxItem, error) {
	filter := map[string]string{}
	if unread {
		filter["status"] = "unread"
	}
	page, _, err := s.Store.Resources(ctx, CollectionInbox, filter, "", 200)
	if err != nil {
		return nil, err
	}
	out := make([]InboxItem, 0, len(page))
	for _, r := range page {
		var item InboxItem
		if err := r.Decode(&item); err != nil {
			return nil, err
		}
		item.Revision = r.Revision
		out = append(out, item)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CreatedAt.After(out[j-1].CreatedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
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
