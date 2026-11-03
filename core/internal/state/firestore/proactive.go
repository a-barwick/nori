package firestore

import (
	"context"
	"sort"
	"time"

	"cloud.google.com/go/firestore"
	"nori/internal/state"
)

func (d *Database) proactiveChecks() *firestore.CollectionRef {
	return d.root().Collection("proactive_checks")
}
func (d *Database) offers() *firestore.CollectionRef { return d.root().Collection("proactive_offers") }
func (d *Database) proactiveDays() *firestore.CollectionRef {
	return d.root().Collection("proactive_days")
}

func (d *Database) readProactiveDay(tx *firestore.Transaction, day string) (state.ProactiveDay, error) {
	var saved state.ProactiveDay
	doc, err := tx.Get(d.proactiveDays().Doc(day))
	if notFound(err) {
		return state.ProactiveDay{Day: day}, nil
	}
	if err != nil {
		return saved, err
	}
	return saved, doc.DataTo(&saved)
}

func (d *Database) readOffer(tx *firestore.Transaction, id string) (state.Offer, error) {
	var o state.Offer
	doc, err := tx.Get(d.offers().Doc(id))
	if notFound(err) {
		return o, state.ErrNotFound
	}
	if err != nil {
		return o, err
	}
	return o, doc.DataTo(&o)
}

func (d *Database) BeginProactiveCheck(ctx context.Context, id, day, owner string, manual bool) (state.ProactiveCheck, bool, error) {
	var check state.ProactiveCheck
	var proceed bool
	err := d.logged(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		var saved *state.ProactiveCheck
		doc, err := tx.Get(d.proactiveChecks().Doc(id))
		if err == nil {
			saved = &state.ProactiveCheck{}
			if err = doc.DataTo(saved); err != nil {
				return err
			}
		} else if !notFound(err) {
			return err
		}
		counts, err := d.readProactiveDay(tx, day)
		if err != nil {
			return err
		}
		var updated state.ProactiveDay
		check, updated, proceed, err = state.BeginCheck(saved, counts, id, day, owner, manual, time.Now().UTC())
		if err != nil || !proceed {
			return err
		}
		if saved == nil {
			if err = tx.Set(d.proactiveDays().Doc(day), updated); err != nil {
				return err
			}
		}
		return tx.Set(d.proactiveChecks().Doc(id), check)
	})
	return check, proceed, err
}

// FinishProactiveCheck saves the check's outcome if this caller still holds it.
func (d *Database) FinishProactiveCheck(ctx context.Context, check state.ProactiveCheck) error {
	return d.logged(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		var saved state.ProactiveCheck
		doc, err := tx.Get(d.proactiveChecks().Doc(check.ID))
		if err != nil {
			return err
		}
		if err = doc.DataTo(&saved); err != nil {
			return err
		}
		if saved.Owner != check.Owner || saved.Status != "running" {
			return state.ErrCheckRunning
		}
		return tx.Set(d.proactiveChecks().Doc(check.ID), check)
	})
}

// ClaimOffer saves a newly found conflict, or returns the one already saved under its ID. proceed is true when the
// offer still needs planning: it's new, or a check stopped before planning it.
func (d *Database) ClaimOffer(ctx context.Context, offer state.Offer) (state.Offer, bool, error) {
	var saved state.Offer
	var proceed bool
	err := d.logged(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		var err error
		saved, err = d.readOffer(tx, offer.ID)
		if err == nil {
			proceed = saved.Status == state.OfferPlanning
			return nil
		}
		if err != state.ErrNotFound {
			return err
		}
		offer.Status, offer.CreatedAt = state.OfferPlanning, time.Now().UTC()
		saved, proceed = offer, true
		return tx.Create(d.offers().Doc(offer.ID), offer)
	})
	return saved, proceed, err
}

// ChargeProactivePlan reserves one planning call against the day's cap.
func (d *Database) ChargeProactivePlan(ctx context.Context, day string) error {
	return d.logged(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		counts, err := d.readProactiveDay(tx, day)
		if err != nil {
			return err
		}
		if counts, err = state.ChargePlan(counts, day); err != nil {
			return err
		}
		return tx.Set(d.proactiveDays().Doc(day), counts)
	})
}

func (d *Database) SettleProactivePlan(ctx context.Context, day string, cost *float64) error {
	return d.logged(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		counts, err := d.readProactiveDay(tx, day)
		if err != nil {
			return err
		}
		return tx.Set(d.proactiveDays().Doc(day), state.SettlePlan(counts, cost))
	})
}

// SavePlannedOffer stores the planned text and time of an offer that is still being planned.
func (d *Database) SavePlannedOffer(ctx context.Context, offer state.Offer) error {
	return d.logged(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		saved, err := d.readOffer(tx, offer.ID)
		if err != nil {
			return err
		}
		if saved.Status != state.OfferPlanning {
			return state.ErrOfferState
		}
		offer.Status, offer.CreatedAt = state.OfferPlanned, saved.CreatedAt
		return tx.Set(d.offers().Doc(offer.ID), offer)
	})
}

// PlannedOffers lists offers waiting for their delivery time, earliest first. There are only ever a few.
func (d *Database) PlannedOffers(ctx context.Context) ([]state.Offer, error) {
	offers, err := documents[state.Offer](d.offers().Where("status", "==", state.OfferPlanned).Limit(100).Documents(ctx))
	if err != nil {
		return nil, err
	}
	sort.Slice(offers, func(i, j int) bool { return offers[i].DeliverAt.Before(offers[j].DeliverAt) })
	return offers, nil
}

func (d *Database) GetOffer(ctx context.Context, id string) (state.Offer, error) {
	doc, err := d.offers().Doc(id).Get(ctx)
	if notFound(err) {
		return state.Offer{}, state.ErrNotFound
	}
	if err != nil {
		return state.Offer{}, err
	}
	var o state.Offer
	return o, doc.DataTo(&o)
}

// DeliverOffer publishes a due offer as its own finished turn, only while the conversation is idle, so it never cuts
// into a reply. Past its conflict or the day's cap, it is skipped instead. note is what the outer model later sees
// about the offer, text is what Austin sees, and protocol is the turn's saved model history.
func (d *Database) DeliverOffer(ctx context.Context, id, day, note, text string, protocol []string, now time.Time) (state.Offer, error) {
	var offer state.Offer
	err := d.logged(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		var err error
		if offer, err = d.readOffer(tx, id); err != nil {
			return err
		}
		counts, err := d.readProactiveDay(tx, day)
		if err != nil {
			return err
		}
		c, err := d.readConversation(tx)
		if err != nil {
			return err
		}
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		now := now.UTC()
		deliver, skip, err := state.DeliveryOutcome(offer, counts, now)
		if err != nil {
			return err
		}
		if !deliver {
			offer.Status, offer.SkipReason = state.OfferSkipped, skip
			return tx.Set(d.offers().Doc(id), offer)
		}
		if !state.Idle(c) {
			return state.ErrConversationBusy
		}
		c, input, turn, message := state.OfferTurn(c, offer, note, text, now)
		turn.Protocol = protocol
		offer.Status, offer.Text, offer.DeliveredAt, offer.MessageID = state.OfferDelivered, text, &now, message.ID
		counts.Day = day
		counts.Delivered++
		writes := []func() error{
			func() error { return tx.Set(d.conversation(), c) },
			func() error { return tx.Create(d.conversationInputs().Doc(input.ID), input) },
			func() error { return tx.Create(d.conversationTurns().Doc(turn.ID), turn) },
			func() error { return tx.Set(d.conversationAttempts().Doc(turn.Attempt.ID), turn.Attempt) },
			func() error { return tx.Create(d.messages().Doc(message.ID), message) },
			func() error { return tx.Set(d.offers().Doc(id), offer) },
			func() error { return tx.Set(d.proactiveDays().Doc(day), counts) },
			func() error { return log.add(state.EventMessage, message.ID) },
			func() error { return log.add(state.EventConversation, "") },
		}
		for _, write := range writes {
			if err := write(); err != nil {
				return err
			}
		}
		return nil
	})
	return offer, err
}

// DecideOffer records Austin's accept or dismiss and updates the card on the offer's message.
func (d *Database) DecideOffer(ctx context.Context, id, decision string) (state.Offer, error) {
	var offer state.Offer
	err := d.logged(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		var err error
		if offer, err = d.readOffer(tx, id); err != nil {
			return err
		}
		var changed bool
		if offer, changed, err = state.DecideOffer(offer, decision, time.Now().UTC()); err != nil || !changed {
			return err
		}
		var message state.Message
		doc, err := tx.Get(d.messages().Doc(offer.MessageID))
		if err != nil {
			return err
		}
		if err = doc.DataTo(&message); err != nil {
			return err
		}
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		if err = tx.Set(d.offers().Doc(id), offer); err != nil {
			return err
		}
		if err = tx.Set(d.messages().Doc(message.ID), state.MarkOfferPart(message, id, decision)); err != nil {
			return err
		}
		return log.add(state.EventMessage, message.ID)
	})
	return offer, err
}
