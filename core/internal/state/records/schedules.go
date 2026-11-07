package records

import (
	"context"
	"sort"
	"time"

	"nori/internal/state"
	"nori/internal/state/docstore"
)

func (d *Database) schedules() *docstore.CollectionRef { return d.root().Collection("schedules") }
func (d *Database) notifications() *docstore.CollectionRef {
	return d.root().Collection("notifications")
}
func (d *Database) notifyDays() *docstore.CollectionRef { return d.root().Collection("notify_days") }
func (d *Database) notifySeen() *docstore.DocumentRef {
	return d.root().Collection("conversation").Doc("seen")
}

// readSchedule returns nil when the schedule doesn't exist.
func (d *Database) readSchedule(tx *docstore.Transaction, id string) (*state.Schedule, error) {
	doc, err := tx.Get(d.schedules().Doc(id))
	if notFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s state.Schedule
	return &s, doc.DataTo(&s)
}

// Schedules returns every saved schedule, oldest first. Open ones are bounded when they are created; closed ones stay
// as history.
func (d *Database) Schedules(ctx context.Context) ([]state.Schedule, error) {
	it := d.schedules().OrderBy("created_at", docstore.Asc).Limit(500).Documents(ctx)
	defer it.Stop()
	return documents[state.Schedule](it)
}

// ChangeScheduleNow applies a pause, resume or cancel from a client control to the saved schedule.
func (d *Database) ChangeScheduleNow(ctx context.Context, id, change string, location *time.Location, now time.Time) (state.Schedule, error) {
	var s state.Schedule
	err := d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		prior, err := d.readSchedule(tx, id)
		if err != nil {
			return err
		}
		if prior == nil {
			return state.ErrNotFound
		}
		if s, err = state.ChangeSchedule(*prior, state.ScheduleChangeRequest{ID: id, Change: change}, location, now); err != nil {
			return err
		}
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		if err = log.add(state.EventSchedule, id); err != nil {
			return err
		}
		return tx.Set(d.schedules().Doc(id), s)
	})
	return s, err
}

// readScheduleChanges reads the schedules a turn's changes touch, before the publish transaction writes anything.
func (d *Database) readScheduleChanges(tx *docstore.Transaction, changes []state.ScheduleChangeRequest) (map[string]*state.Schedule, error) {
	saved := map[string]*state.Schedule{}
	for _, c := range changes {
		s, err := d.readSchedule(tx, c.ID)
		if err != nil {
			return nil, err
		}
		saved[c.ID] = s
	}
	return saved, nil
}

// writeScheduleChanges applies a turn's schedule changes inside its publish transaction. They were checked when the
// reply was written; they are applied again here to the saved schedules, so a change made since is kept. A refused
// change means none apply.
func (d *Database) writeScheduleChanges(
	tx *docstore.Transaction, log *eventLog, t state.ConversationTurn, saved map[string]*state.Schedule, now time.Time,
) error {
	for _, c := range t.Attempt.Schedules {
		if c.Error != "" {
			return nil
		}
	}
	source := state.SourceInput(t.Inputs)
	for _, c := range t.Attempt.Schedules {
		location, err := time.LoadLocation(c.TimeZone)
		if err != nil {
			location = time.UTC
		}
		var s state.Schedule
		if c.Action == state.ScheduleCreate {
			if prior := saved[c.ID]; prior != nil {
				continue
			}
			s, err = state.NewSchedule(c, location, now)
			s.SourceInputID, s.SourceText = source.ID, state.BoundedSource(source.Text)
		} else if prior := saved[c.ID]; prior == nil {
			err = state.ErrNotFound
		} else {
			s, err = state.ChangeSchedule(*prior, c, location, now)
		}
		// The change was checked when the reply was written. One that no longer applies, such as a cancel that met a
		// schedule which just finished, is left out rather than holding back the reply.
		if err != nil {
			continue
		}
		saved[c.ID] = &s
		if err = log.add(state.EventSchedule, s.ID); err != nil {
			return err
		}
		if err = tx.Set(d.schedules().Doc(s.ID), s); err != nil {
			return err
		}
	}
	return nil
}

// ClaimSchedule claims the due occurrence named by key and moves the schedule on. runID is the task the occurrence
// started, if any. A message, when given, is published as its own finished turn in the same transaction, which needs an
// idle conversation; the occurrence then waits for the next pass.
func (d *Database) ClaimSchedule(
	ctx context.Context, id, key, runID, reason string, message *state.ScheduleMessage, now time.Time,
) (state.Schedule, error) {
	var saved state.Schedule
	err := d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		s, err := d.readSchedule(tx, id)
		if err != nil {
			return err
		}
		if s == nil {
			return state.ErrNotFound
		}
		updated, fire, ok := state.FireSchedule(*s, now.UTC(), reason)
		if !ok || fire.Key != key {
			return state.ErrScheduleState
		}
		if runID != "" {
			updated.LastRunID = runID
		}
		saved = updated

		c, err := d.readConversation(tx)
		if err != nil {
			return err
		}
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		if message == nil {
			if err = log.add(state.EventSchedule, id); err != nil {
				return err
			}
			return tx.Set(d.schedules().Doc(id), updated)
		}
		if !state.Idle(c) {
			return state.ErrConversationBusy
		}
		at := now.UTC()
		c, input, turn, m := state.ScheduleTurn(c, updated, key, message.Note, message.Text, at)
		turn.Protocol = message.Protocol
		notification := state.NotificationFor(m, message.Urgent)
		writes := []func() error{
			func() error { return tx.Set(d.conversation(), c) },
			func() error { return tx.Create(d.conversationInputs().Doc(input.ID), input) },
			func() error { return tx.Create(d.conversationTurns().Doc(turn.ID), turn) },
			func() error { return tx.Set(d.conversationAttempts().Doc(turn.Attempt.ID), turn.Attempt) },
			func() error { return tx.Create(d.messages().Doc(m.ID), m) },
			func() error { return tx.Set(d.notifications().Doc(notification.ID), notification) },
			func() error { return tx.Set(d.schedules().Doc(id), updated) },
			func() error { return log.add(state.EventSchedule, id) },
			func() error { return log.add(state.EventMessage, m.ID) },
			func() error { return log.add(state.EventConversation, "") },
		}
		for _, write := range writes {
			if err := write(); err != nil {
				return err
			}
		}
		return nil
	})
	return saved, err
}

// ArmSchedule records the time of the timed wake asked for, so the next pass doesn't ask again.
func (d *Database) ArmSchedule(ctx context.Context, id string, at time.Time) error {
	_, err := d.schedules().Doc(id).Update(ctx, []docstore.Update{{Path: "armed_for", Value: at.UTC()}})
	return err
}

// ReadPosition returns the shared read position, or an empty mark before anything was read.
func (d *Database) ReadPosition(ctx context.Context) (state.NotifySeenMark, error) {
	doc, err := d.notifySeen().Get(ctx)
	if notFound(err) {
		return state.NotifySeenMark{}, nil
	}
	if err != nil {
		return state.NotifySeenMark{}, err
	}
	var seen state.NotifySeenMark
	err = doc.DataTo(&seen)
	return seen, err
}

// MarkSeen records the newest reply shown in a client's viewport. The mark only moves forward, and its event is
// committed with the mark so other clients can resume it after being offline.
func (d *Database) MarkSeen(ctx context.Context, messageID string) error {
	return d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		doc, err := tx.Get(d.messages().Doc(messageID))
		if notFound(err) {
			return state.ErrNotFound
		}
		if err != nil {
			return err
		}
		var m state.Message
		if err = doc.DataTo(&m); err != nil {
			return err
		}
		if m.Role != "assistant" {
			return state.ErrCommandInvalid
		}
		var seen state.NotifySeenMark
		doc, err = tx.Get(d.notifySeen())
		if err == nil {
			if err = doc.DataTo(&seen); err != nil {
				return err
			}
		} else if !notFound(err) {
			return err
		}
		if !m.CreatedAt.After(seen.Through) {
			return nil
		}
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		if err = log.add(state.EventReadPosition, ""); err != nil {
			return err
		}
		return tx.Set(d.notifySeen(), state.NotifySeenMark{MessageID: m.ID, Through: m.CreatedAt, Position: log.seq})
	})
}

// PendingNotifications lists notifications still waiting, earliest first.
func (d *Database) PendingNotifications(ctx context.Context) ([]state.Notification, error) {
	pending, err := documents[state.Notification](d.notifications().Where("status", "==", state.NotifyPending).Limit(100).Documents(ctx))
	if err != nil {
		return nil, err
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].SendAfter.Before(pending[j].SendAfter) })
	return pending, nil
}

// ClaimNotification decides a due notification. A send is claimed and counted against the day; a hold moves its time;
// anything else ends it. The caller sends only when the decision is NotifySend.
func (d *Database) ClaimNotification(ctx context.Context, id string, quiet state.QuietHours, local time.Time) (state.Notification, string, error) {
	var n state.Notification
	var decision string
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		doc, err := tx.Get(d.notifications().Doc(id))
		if err != nil {
			return err
		}
		if err = doc.DataTo(&n); err != nil {
			return err
		}
		if n.Status != state.NotifyPending {
			decision = n.Status
			return nil
		}
		var seen state.NotifySeenMark
		if doc, err = tx.Get(d.notifySeen()); err == nil {
			if err = doc.DataTo(&seen); err != nil {
				return err
			}
		} else if !notFound(err) {
			return err
		}
		day := state.NotifyDay{Day: local.Format("2006-01-02")}
		if doc, err = tx.Get(d.notifyDays().Doc(day.Day)); err == nil {
			if err = doc.DataTo(&day); err != nil {
				return err
			}
		} else if !notFound(err) {
			return err
		}
		var until time.Time
		decision, until, n.Reason = state.DecideNotification(n, seen, quiet, day, local)
		switch decision {
		case state.NotifySend:
			n.Status = state.NotifySending
			day.Sent++
			if err = tx.Set(d.notifyDays().Doc(day.Day), day); err != nil {
				return err
			}
		case state.NotifyHold:
			n.SendAfter = until.UTC()
		default:
			n.Status = decision
		}
		return tx.Set(d.notifications().Doc(id), n)
	})
	return n, decision, err
}

// FinishNotification records what a claimed send did.
func (d *Database) FinishNotification(ctx context.Context, id, status, reason string, now time.Time) error {
	updates := []docstore.Update{{Path: "status", Value: status}, {Path: "reason", Value: reason}}
	if status == state.NotifySent {
		updates = append(updates, docstore.Update{Path: "sent_at", Value: now.UTC()})
	}
	_, err := d.notifications().Doc(id).Update(ctx, updates)
	return err
}

func (d *Database) GetSchedule(ctx context.Context, id string) (state.Schedule, error) {
	var schedule state.Schedule
	doc, err := d.schedules().Doc(id).Get(ctx)
	if notFound(err) {
		return schedule, state.ErrNotFound
	}
	if err != nil {
		return schedule, err
	}
	return schedule, doc.DataTo(&schedule)
}
