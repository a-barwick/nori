package proactive

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"nori/internal/conversation"
	"nori/internal/prompts"
	"nori/internal/state"
)

const maxPrepText = 1500

// Deliver publishes every offer whose time has come. It runs after each conversation pass, so an offer due during a
// reply goes out once that reply is published. A busy conversation leaves the rest for the next pass.
func (s *Service) Deliver(ctx context.Context) error {
	offers, err := s.Store.PlannedOffers(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	for _, offer := range offers {
		if offer.DeliverAt.After(now) {
			break
		}
		text := offer.Text + s.prepText(ctx, offer)
		note := offerNote(offer, s)
		protocol := []string{message("system", note), message("assistant", text)}
		delivered, err := s.Store.DeliverOffer(ctx, offer.ID, now.Format("2006-01-02"), note, text, protocol, now)
		if errors.Is(err, state.ErrConversationBusy) {
			return nil
		}
		if err != nil && !errors.Is(err, state.ErrOfferState) {
			return err
		}
		log.Printf("proactive offer %s %s %s", offer.ID, delivered.Status, delivered.SkipReason)
		// Once its offer is out or skipped, nothing waits on the prep task, and nobody could answer its questions.
		s.stopPrep(ctx, offer)
	}
	return nil
}

// prepText reports the read-only task's options, or says there are none yet.
func (s *Service) prepText(ctx context.Context, offer state.Offer) string {
	if offer.PrepRunID == "" {
		return "\n\nI haven't looked for other times yet."
	}
	run, err := s.Store.GetRun(ctx, offer.PrepRunID)
	if err != nil {
		return "\n\nI haven't looked for other times yet."
	}
	switch {
	case run.Status == state.RunCompleted && strings.TrimSpace(run.Result) != "":
		result := strings.TrimSpace(run.Result)
		if len(result) > maxPrepText {
			cut := maxPrepText
			for cut > 0 && !utf8.RuneStart(result[cut]) {
				cut--
			}
			result = result[:cut] + "…"
		}
		// The calendar can change between the prep task's read and delivery, so the times say when they were read.
		read := run.StartedAt
		if run.FinishedAt != nil {
			read = *run.FinishedAt
		}
		return "\n\nTimes that looked open when I read your calendar at " + clock(read, s.Location) +
			". It may have changed since, and I'll check again before changing anything:\n" + result
	case run.Status == state.RunRunning:
		return "\n\nI didn't finish looking for other times."
	}
	return "\n\nI couldn't find other times on my own."
}

func offerNote(o state.Offer, s *Service) string {
	finding, _ := json.Marshal(o.Finding)
	note := prompts.Runtime("offer-note", map[string]any{"Finding": string(finding)})
	if o.PrepRunID != "" {
		note += "\nA read-only task looked for other times first: " + o.PrepRunID + ". Its times come from an earlier " +
			"calendar read and may be out of date, so read the calendar again before suggesting one."
	}
	return note
}

func message(role, content string) string {
	data, _ := json.Marshal(map[string]string{"role": role, "content": content})
	return string(data)
}

// Decide records the user's Accept or Dismiss. Accept sends their yes as an ordinary message, so the outer loop handles it
// like anything they say. Dismiss stops a prep task that is still looking.
func (s *Service) Decide(ctx context.Context, offerID, decision string) (state.Offer, error) {
	offer, err := s.Store.DecideOffer(ctx, offerID, decision)
	if err != nil {
		return offer, err
	}
	switch decision {
	case state.OfferAccepted:
		if s.Runner != nil {
			id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(offer.ID+"|accept")).String()
			_, err = s.Runner.HandleInput(ctx, conversation.Input{ID: id, Message: "Yes, please look for another time."})
		}
	case state.OfferDismissed:
		s.stopPrep(ctx, offer)
	}
	return offer, err
}

// stopPrep stops an offer's prep task that is still running.
func (s *Service) stopPrep(ctx context.Context, offer state.Offer) {
	if s.Tasks == nil || offer.PrepRunID == "" {
		return
	}
	run, err := s.Store.GetRun(ctx, offer.PrepRunID)
	if err != nil || run.Status != state.RunRunning || run.CancelRequested {
		return
	}
	_, err = s.Tasks.AcceptBatch(ctx, []state.Command{{ID: offer.PrepRunID + "-stop", RunID: offer.PrepRunID, Kind: state.CommandStop}}, nil)
	if err != nil && !errors.Is(err, state.ErrDuplicate) {
		log.Printf("proactive offer %s left its prep task running: %v", offer.ID, err)
	}
}
