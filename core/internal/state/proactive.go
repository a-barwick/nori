package state

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// A proactive offer starts from a scheduled calendar check, with no request from Austin. It may only offer: the
// runtime saves what it found, plans when to say it, and publishes it as its own finished turn. Nothing it says grants
// permission for anything.

// Limits on proactive work. They hold whatever the model or a memory says.
const (
	// ProactiveOffersPerDay is the most offers delivered in one local day.
	ProactiveOffersPerDay = 3
	// ProactiveSpendPerDayUSD bounds the model calls that plan offers in one local day.
	ProactiveSpendPerDayUSD = 0.05
	// ProactivePlanEstimateUSD is charged before a planning call, and kept when its cost is unknown.
	ProactivePlanEstimateUSD = 0.01
	// Scheduled checks are named by their half-day slot, so at most two run a day. ProactiveManualChecksPerDay bounds
	// the checks an operator starts by hand to test the trigger.
	ProactiveManualChecksPerDay = 4
	// ProactivePrepBudgetUSD is the budget of the read-only task that prepares options for an offer.
	ProactivePrepBudgetUSD = 0.05
	// ProactiveCheckLease is how long a running check holds its slot before another call may take it over.
	ProactiveCheckLease = 90 * time.Second
)

// Offer statuses.
const (
	OfferPlanning  = "planning"
	OfferPlanned   = "planned"
	OfferDelivered = "delivered"
	OfferSkipped   = "skipped"
	OfferAccepted  = "accepted"
	OfferDismissed = "dismissed"
)

var (
	ErrOfferState = errors.New("offer is not open")
	// ErrCheckRunning means another call holds the check's lease. ErrProactiveLimit means a daily limit was reached.
	ErrCheckRunning   = errors.New("proactive check is already running")
	ErrProactiveLimit = errors.New("proactive limit reached")
)

// OfferEvent is one calendar event an offer is about. Titles and places came from the calendar, so they are data.
type OfferEvent struct {
	ID       string    `json:"event_id" firestore:"id"`
	Title    string    `json:"title" firestore:"title"`
	Start    time.Time `json:"start" firestore:"start"`
	End      time.Time `json:"end" firestore:"end"`
	Location string    `json:"location" firestore:"location"`
}

// TravelFinding is what the check found: two consecutive events with less time between them than the drive needs.
type TravelFinding struct {
	First  OfferEvent `json:"first" firestore:"first"`
	Second OfferEvent `json:"second" firestore:"second"`
	// GapMinutes is the time between the first event's end and the second's start. DriveMinutes is the driving time
	// the Routes API gave for leaving when the first event ends.
	GapMinutes   int `json:"gap_minutes" firestore:"gap_minutes"`
	DriveMinutes int `json:"drive_minutes" firestore:"drive_minutes"`
}

type Offer struct {
	ID      string        `json:"id" firestore:"id"`
	Kind    string        `json:"kind" firestore:"kind"`
	CheckID string        `json:"check_id" firestore:"check_id"`
	Status  string        `json:"status" firestore:"status"`
	Finding TravelFinding `json:"finding" firestore:"finding"`
	// Text is the offer as it will be shown. DeliverAt is when, and Timing says why that time was chosen.
	Text      string    `json:"text,omitempty" firestore:"text,omitempty"`
	DeliverAt time.Time `json:"deliver_at" firestore:"deliver_at"`
	Timing    string    `json:"timing,omitempty" firestore:"timing,omitempty"`
	// PrepRunID is the read-only task preparing options, when production was running at planning time.
	PrepRunID   string     `json:"prep_run_id,omitempty" firestore:"prep_run_id,omitempty"`
	MessageID   string     `json:"message_id,omitempty" firestore:"message_id,omitempty"`
	SkipReason  string     `json:"skip_reason,omitempty" firestore:"skip_reason,omitempty"`
	CreatedAt   time.Time  `json:"created_at" firestore:"created_at"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty" firestore:"delivered_at,omitempty"`
	DecidedAt   *time.Time `json:"decided_at,omitempty" firestore:"decided_at,omitempty"`
}

// ProactiveCheck is one run of the calendar check. Scheduled runs are named by their local half-day slot, so a repeat
// of the same slot does nothing.
type ProactiveCheck struct {
	ID         string     `json:"id" firestore:"id"`
	Day        string     `json:"day" firestore:"day"`
	Status     string     `json:"status" firestore:"status"`
	Owner      string     `json:"-" firestore:"owner"`
	LeaseUntil time.Time  `json:"-" firestore:"lease_until"`
	StartedAt  time.Time  `json:"started_at" firestore:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty" firestore:"finished_at,omitempty"`
	Events     int        `json:"events" firestore:"events"`
	Found      int        `json:"found" firestore:"found"`
	Offers     []string   `json:"offers" firestore:"offers"`
	Error      string     `json:"error,omitempty" firestore:"error,omitempty"`
}

// ProactiveDay counts what one local day used.
type ProactiveDay struct {
	Day          string  `firestore:"day"`
	Checks       int     `firestore:"checks"`
	ManualChecks int     `firestore:"manual_checks"`
	Delivered    int     `firestore:"delivered"`
	SpentUSD     float64 `firestore:"spent_usd"`
}

// OfferID names a conflict by both events and their times, so a later check that finds the same conflict finds the
// same offer, and a moved event is a new one.
func OfferID(f TravelFinding) string {
	key := fmt.Sprintf("travel|%s|%s|%s|%s|%s|%s", f.First.ID, f.First.End.UTC().Format(time.RFC3339), f.First.Location,
		f.Second.ID, f.Second.Start.UTC().Format(time.RFC3339), f.Second.Location)
	sum := sha256.Sum256([]byte(key))
	return "offer-" + hex.EncodeToString(sum[:12])
}

func OfferInputID(offerID string) string   { return "input-" + offerID }
func OfferTurnID(offerID string) string    { return "turn-" + offerID }
func OfferMessageID(offerID string) string { return OfferTurnID(offerID) + "-assistant" }

// BeginCheck decides whether a call may run the check now. A finished check is not run again; a running one is left
// alone until its lease ends, so a worker restart midway lets the next call take it over.
func BeginCheck(saved *ProactiveCheck, day ProactiveDay, id, localDay, owner string, manual bool, now time.Time) (ProactiveCheck, ProactiveDay, bool, error) {
	if saved != nil {
		if saved.Status != "running" {
			return *saved, day, false, nil
		}
		if saved.LeaseUntil.After(now) {
			return *saved, day, false, ErrCheckRunning
		}
		c := *saved
		c.Owner, c.LeaseUntil = owner, now.Add(ProactiveCheckLease)
		return c, day, true, nil
	}
	if manual {
		if day.ManualChecks >= ProactiveManualChecksPerDay {
			return ProactiveCheck{}, day, false, fmt.Errorf("%w: %d checks were already started by hand on %s", ErrProactiveLimit, day.ManualChecks, localDay)
		}
		day.ManualChecks++
	}
	day.Day = localDay
	day.Checks++
	return ProactiveCheck{
		ID: id, Day: localDay, Status: "running", Owner: owner, LeaseUntil: now.Add(ProactiveCheckLease), StartedAt: now, Offers: []string{},
	}, day, true, nil
}

// ChargePlan reserves the estimate for one planning call, or refuses when the day's cap would be passed.
func ChargePlan(day ProactiveDay, localDay string) (ProactiveDay, error) {
	if day.SpentUSD+ProactivePlanEstimateUSD > ProactiveSpendPerDayUSD+1e-9 {
		return day, fmt.Errorf("%w: proactive model spend for %s is at its cap", ErrProactiveLimit, localDay)
	}
	day.Day = localDay
	day.SpentUSD += ProactivePlanEstimateUSD
	return day, nil
}

// SettlePlan replaces a call's estimate with its known cost. An unknown cost keeps the estimate.
func SettlePlan(day ProactiveDay, cost *float64) ProactiveDay {
	if cost != nil && *cost >= 0 {
		day.SpentUSD += *cost - ProactivePlanEstimateUSD
	}
	return day
}

// DeliveryOutcome decides what happens to a due offer: deliver it, or skip it for a reason. The conversation must be
// idle, which the caller checks in the same transaction.
func DeliveryOutcome(o Offer, day ProactiveDay, now time.Time) (deliver bool, skip string, err error) {
	if o.Status != OfferPlanned {
		return false, "", ErrOfferState
	}
	if !now.Before(o.Finding.Second.Start) {
		return false, "the second event had already started", nil
	}
	if day.Delivered >= ProactiveOffersPerDay {
		return false, fmt.Sprintf("%d offers were already delivered that day", ProactiveOffersPerDay), nil
	}
	return true, "", nil
}

// OfferTurn is the finished turn and message an offer becomes. It goes into the conversation history like any reply,
// so the outer model sees it when Austin answers.
func OfferTurn(c ConversationControl, o Offer, note, text string, now time.Time) (ConversationControl, ConversationInput, ConversationTurn, Message) {
	c.Received++
	input := ConversationInput{ID: OfferInputID(o.ID), Text: note, Position: c.Received, CreatedAt: now, Offer: o.ID}
	t := ConversationTurn{
		ID: OfferTurnID(o.ID), Inputs: []ConversationInput{input}, Through: c.Received, UserThrough: min(c.UserReceived, c.Consumed),
		StartedAt: now, FinishedAt: &now, Status: "completed", Protocol: []string{}, Context: []string{},
		Attempt: ConversationAttempt{
			ID: OfferTurnID(o.ID) + "-attempt-1", TurnID: OfferTurnID(o.ID), Number: 1, InputIDs: []string{input.ID}, Model: "none",
			Status: "completed", StartedAt: now, FinishedAt: &now, Text: text, Results: []string{}, Actions: []ConversationAction{}, Usage: []Usage{},
		},
	}
	c.Consumed = c.Received
	c.LatestAt = now
	message := Message{ID: OfferMessageID(o.ID), Role: "assistant", Text: text, CreatedAt: now, TurnID: t.ID,
		Parts: append(TextParts(text), Part{Type: PartOffer, ID: o.ID, Status: "open", Fallback: "Reply to accept or turn this down."})}
	return c, input, t, message
}

// DecideOffer records Austin's accept or dismiss on a delivered offer. Deciding again the same way changes nothing.
func DecideOffer(o Offer, decision string, now time.Time) (Offer, bool, error) {
	if decision != OfferAccepted && decision != OfferDismissed {
		return o, false, fmt.Errorf("%w: decision must be accepted or dismissed", ErrCommandInvalid)
	}
	if o.Status == decision {
		return o, false, nil
	}
	if o.Status != OfferDelivered {
		return o, false, ErrOfferState
	}
	o.Status, o.DecidedAt = decision, &now
	return o, true, nil
}

// MarkOfferPart sets the status shown on a message's offer card.
func MarkOfferPart(m Message, offerID, status string) Message {
	for i := range m.Parts {
		if m.Parts[i].Type == PartOffer && m.Parts[i].ID == offerID {
			m.Parts[i].Status = status
		}
	}
	return m
}
