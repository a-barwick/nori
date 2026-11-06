package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"nori/internal/proactive"
	"nori/internal/state"
)

// Proactive is what the proactive routes call into.
type Proactive interface {
	Check(ctx context.Context, manual bool) (proactive.CheckResult, error)
	Decide(ctx context.Context, offerID, decision string) (state.Offer, error)
}

var offerID = regexp.MustCompile(`^offer-[0-9a-f]{24}$`)

// check runs the scheduled calendar check. Cloud Scheduler calls it at midnight and noon; manual is for an operator
// testing the trigger. A finished slot answers with its saved outcome and does nothing.
func (s *server) check(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Manual bool `json:"manual"`
	}
	if !decode(w, r, &body) {
		return
	}
	if s.Proactive == nil {
		writeJSON(w, 503, map[string]string{"error": "The calendar check isn't configured."})
		return
	}
	// Shorter than the check's lease, so a call can't still be running when another takes the check over.
	ctx, cancel := context.WithTimeout(r.Context(), 80*time.Second)
	defer cancel()
	result, err := s.Proactive.Check(ctx, body.Manual)
	// The twice-daily call also wakes a conversation pass, which delivers due offers and fires due schedules. A
	// schedule whose own wake was lost then waits at most until the next check.
	if wakeErr := s.wakeConversation(r.Context()); wakeErr != nil {
		log.Printf("proactive check couldn't wake the conversation: %v", wakeErr)
	}
	if errors.Is(err, state.ErrCheckRunning) {
		writeJSON(w, 409, result)
		return
	}
	if errors.Is(err, state.ErrProactiveLimit) {
		writeJSON(w, 429, result)
		return
	}
	if err != nil {
		log.Printf("proactive check failed: %v", err)
		writeJSON(w, 503, map[string]any{"error": "The calendar check will retry.", "retryable": true})
		return
	}
	// A failed calendar read is a finished check: retrying the same slot wouldn't help, and the next slot runs anyway.
	writeJSON(w, 200, result)
}

// schedule pauses, resumes or cancels a schedule from the web client's list. A wake follows, so a resumed schedule is
// armed for its next time.
func (s *server) schedule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID     string `json:"id"`
		Change string `json:"change"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.ID == "" || strings.ContainsAny(body.ID, "/.") || (body.Change != state.ChangePause && body.Change != state.ChangeResume && body.Change != state.ChangeCancel) {
		writeJSON(w, 400, map[string]string{"error": "Send a schedule ID and a change of pause, resume or cancel."})
		return
	}
	saved, err := s.Runner.ChangeSchedule(r.Context(), body.ID, body.Change)
	if errors.Is(err, state.ErrScheduleState) {
		writeJSON(w, 409, map[string]string{"error": "This schedule has already ended."})
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.wakeConversation(r.Context()); err != nil {
		log.Printf("schedule change couldn't wake the conversation: %v", err)
	}
	writeJSON(w, 200, saved)
}

// offer records Accept or Dismiss on a delivered offer. Accept also sends the user's yes as an ordinary message, so
// any work starts on the normal task path.
func (s *server) offer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID       string `json:"id"`
		Decision string `json:"decision"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !offerID.MatchString(body.ID) || (body.Decision != state.OfferAccepted && body.Decision != state.OfferDismissed) {
		writeJSON(w, 400, map[string]string{"error": "Send an offer ID and a decision of accepted or dismissed."})
		return
	}
	if s.Proactive == nil {
		writeJSON(w, 503, map[string]string{"error": "Offers aren't configured."})
		return
	}
	offer, err := s.Proactive.Decide(r.Context(), body.ID, body.Decision)
	if errors.Is(err, state.ErrOfferState) {
		writeJSON(w, 409, map[string]string{"error": "This offer was already settled."})
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if offer.Status == state.OfferAccepted {
		if err := s.wakeConversation(r.Context()); err != nil {
			log.Printf("accepted offer wake failed: %v", err)
		}
	}
	writeJSON(w, 200, map[string]string{"id": offer.ID, "status": offer.Status})
}
