package scheduler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/mail"
	"strings"
	"time"

	"nori/internal/secret"
	"nori/internal/state"
)

// Push and email delivery.
//
// Enrollment stores the destination's private target (an email address, or a Web Push subscription's endpoint and
// keys) in the credential store, never in the destination resource the API projects. A destination is usable only
// after verification: Nori sends a six-digit code through the channel and the user enters it. Every send records the
// provider's receipt honestly: "accepted" means the provider took the message, not that anyone read it; a lost
// response is "uncertain"; an expired push subscription fails the destination until the user enrolls again.

// Target is a destination's private address. It is stored, never projected.
type Target struct {
	Address  string `json:"address,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	P256dh   string `json:"p256dh,omitempty"`
	Auth     string `json:"auth,omitempty"`
}

// Message is what a channel sends. Topic lets a provider replace an undelivered earlier copy.
type Message struct {
	Title, Body, URL, Topic string
}

// Receipt is the provider's answer to a send. State is "accepted".
type Receipt struct {
	ID    string
	State string
}

// Channel delivers to one kind of destination.
type Channel interface {
	Kind() string
	Send(ctx context.Context, target Target, msg Message) (Receipt, error)
}

var (
	ErrTargetInvalid  = errors.New("the destination's address is invalid")
	ErrTargetExpired  = errors.New("the destination's subscription has expired")
	ErrRetryLater     = errors.New("the provider is temporarily unavailable")
	ErrOutcomeUnknown = errors.New("the provider's answer was lost")
)

const (
	CollectionVerifications = "destination_verifications"
	verificationTTL         = 15 * time.Minute
	verificationAttempts    = 5
	maxDeliveryAttempts     = 5
)

func targetKey(destinationID string) string { return "delivery_" + destinationID }

func (s *Service) channel(kind string) Channel {
	if c, ok := s.Channels[kind]; ok && c != nil {
		return c
	}
	return nil
}

func (s *Service) target(ctx context.Context, destinationID string) (Target, error) {
	if s.Targets == nil {
		return Target{}, secret.ErrCredentialUnavailable
	}
	c, err := s.Targets.Get(ctx, targetKey(destinationID))
	if err != nil {
		return Target{}, err
	}
	var t Target
	err = json.Unmarshal(c.Data, &t)
	return t, err
}

// enroll validates and stores a destination's target.
func (s *Service) enroll(ctx context.Context, destinationID, channel string, value DestinationEditable) error {
	var t Target
	switch channel {
	case "email":
		address, err := mail.ParseAddress(strings.TrimSpace(value.Address))
		if err != nil {
			return fmt.Errorf("%w: an email destination needs a valid address", state.ErrInvalidResource)
		}
		t.Address = address.Address
	case "push":
		sub := value.Subscription
		if sub == nil || !strings.HasPrefix(sub.Endpoint, "https://") || sub.Keys.P256dh == "" || sub.Keys.Auth == "" {
			return fmt.Errorf("%w: a push destination needs the browser subscription's https endpoint and keys", state.ErrInvalidResource)
		}
		t.Endpoint, t.P256dh, t.Auth = sub.Endpoint, sub.Keys.P256dh, sub.Keys.Auth
	}
	if s.Targets == nil {
		return secret.ErrCredentialUnavailable
	}
	data, _ := json.Marshal(t)
	current, err := s.Targets.Get(ctx, targetKey(destinationID))
	expected := int64(0)
	if err == nil {
		expected = current.Version
	} else if !errors.Is(err, secret.ErrCredentialNotFound) {
		return err
	}
	_, err = s.Targets.Replace(ctx, targetKey(destinationID), expected, data)
	return err
}

type verification struct {
	DestinationID string    `json:"destination_id"`
	CodeHash      string    `json:"code_hash"`
	Salt          string    `json:"salt"`
	ExpiresAt     time.Time `json:"expires_at"`
	Attempts      int       `json:"attempts"`
}

func hashCode(salt, code string) string {
	sum := sha256.Sum256([]byte(salt + ":" + code))
	return hex.EncodeToString(sum[:])
}

// SendVerification sends a fresh code through the destination's channel. Sending is the only effect; a repeated
// request sends a new code and invalidates the old one.
func (s *Service) SendVerification(ctx context.Context, id string) error {
	d, err := s.Destination(ctx, id)
	if err != nil {
		return err
	}
	if d.Channel == "in_app" {
		return fmt.Errorf("%w: the inbox needs no verification", state.ErrInvalidResource)
	}
	c := s.channel(d.Channel)
	if c == nil {
		return ErrNoTransport
	}
	t, err := s.target(ctx, id)
	if err != nil {
		return err
	}
	n, _ := rand.Int(rand.Reader, big.NewInt(1_000_000))
	code := fmt.Sprintf("%06d", n.Int64())
	saltBytes := make([]byte, 8)
	_, _ = rand.Read(saltBytes)
	salt := hex.EncodeToString(saltBytes)
	v := verification{DestinationID: id, CodeHash: hashCode(salt, code), Salt: salt, ExpiresAt: s.now().Add(verificationTTL)}
	if err := s.putVerification(ctx, v); err != nil {
		return err
	}
	_, err = c.Send(ctx, t, Message{Title: "Nori verification code", Body: "Your Nori verification code is " + code + ". It expires in 15 minutes.", Topic: "verify"})
	if errors.Is(err, ErrTargetExpired) {
		s.markDestination(ctx, id, "failed")
	}
	return err
}

func (s *Service) putVerification(ctx context.Context, v verification) error {
	r, err := state.NewResource(CollectionVerifications, v.DestinationID, 0, v, nil, s.now())
	if err != nil {
		return err
	}
	current, err := s.Store.Resource(ctx, CollectionVerifications, v.DestinationID)
	expected := int64(0)
	if err == nil {
		expected = current.Revision
	}
	_, err = s.Store.PutResource(ctx, r, expected)
	return err
}

// ErrVerificationFailed is a wrong, expired or exhausted code.
var ErrVerificationFailed = errors.New("the verification code is wrong or expired")

// Verify checks a code and marks the destination verified.
func (s *Service) Verify(ctx context.Context, id, code string) error {
	r, err := s.Store.Resource(ctx, CollectionVerifications, id)
	if err != nil {
		return ErrVerificationFailed
	}
	var v verification
	if err := r.Decode(&v); err != nil {
		return err
	}
	if v.Attempts >= verificationAttempts || s.now().After(v.ExpiresAt) {
		return ErrVerificationFailed
	}
	v.Attempts++
	if err := s.putVerification(ctx, v); err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(hashCode(v.Salt, strings.TrimSpace(code))), []byte(v.CodeHash)) != 1 {
		return ErrVerificationFailed
	}
	v.Attempts = verificationAttempts // a code works once
	_ = s.putVerification(ctx, v)
	return s.markDestination(ctx, id, "verified")
}

func (s *Service) markDestination(ctx context.Context, id, status string) error {
	r, err := s.Store.Resource(ctx, CollectionDestinations, id)
	if err != nil {
		return err
	}
	var d Destination
	if err := r.Decode(&d); err != nil {
		return err
	}
	d.Status, d.Revision = status, r.Revision+1
	next, err := state.NewResource(CollectionDestinations, id, d.Revision, d, nil, s.now())
	if err != nil {
		return err
	}
	_, err = s.Store.PutResource(ctx, next, r.Revision)
	return err
}

// sendDelivery attempts one push or email delivery and records the outcome on the delivery.
// deliveryDoc is a delivery as stored: the projected Delivery plus the message and attempt count, which the API does
// not show.
type deliveryDoc struct {
	Delivery
	Title    string `json:"title,omitempty"`
	Body     string `json:"body,omitempty"`
	URL      string `json:"url,omitempty"`
	Attempts int    `json:"attempts,omitempty"`
}

func (s *Service) sendDelivery(ctx context.Context, d *deliveryDoc, destination Destination, msg Message) {
	now := s.now()
	c := s.channel(destination.Channel)
	if c == nil {
		d.State, d.Error = "failed", &ErrorDetail{Code: "no_transport", Message: ErrNoTransport.Error()}
		return
	}
	target, err := s.target(ctx, destination.ID)
	if err != nil {
		d.State, d.Error = "failed", &ErrorDetail{Code: "destination_missing", Message: "The destination's address is not stored; enroll it again."}
		return
	}
	d.Attempts++
	d.AttemptID = fmt.Sprintf("%s-a%d", d.ID, d.Attempts)
	msg.Topic = strings.TrimPrefix(d.ID, "dlv_")[:24]
	receipt, err := c.Send(ctx, target, msg)
	switch {
	case err == nil:
		d.State, d.Receipt, d.Error, d.ScheduledFor = receipt.State, receipt.ID, nil, nil
	case errors.Is(err, ErrTargetExpired):
		d.State, d.Error = "failed", &ErrorDetail{Code: "subscription_expired", Message: err.Error()}
		s.markDestination(ctx, destination.ID, "failed")
	case errors.Is(err, ErrRetryLater) && d.Attempts < maxDeliveryAttempts:
		// Nothing was accepted, so trying again cannot duplicate.
		next := now.Add(time.Duration(1<<d.Attempts) * time.Minute)
		d.State, d.ScheduledFor, d.Error = "pending", &next, &ErrorDetail{Code: "retrying", Message: err.Error()}
	case errors.Is(err, ErrOutcomeUnknown) && destination.Channel == "push" && d.Attempts < maxDeliveryAttempts:
		// A push resend carries the same topic, so the service replaces an undelivered copy instead of adding one.
		next := now.Add(time.Duration(1<<d.Attempts) * time.Minute)
		d.State, d.ScheduledFor, d.Error = "pending", &next, &ErrorDetail{Code: "retrying", Message: err.Error()}
	case errors.Is(err, ErrOutcomeUnknown):
		// An email may already be on its way; it is never sent twice automatically.
		d.State, d.Error = "uncertain", &ErrorDetail{Code: "outcome_unknown", Message: err.Error()}
	default:
		d.State, d.Error = "failed", &ErrorDetail{Code: "provider_refused", Message: err.Error()}
	}
}

// sendDue retries pending deliveries whose time has come (quiet hours ended or a retry is due) and returns the next
// due time.
func (s *Service) sendDue(ctx context.Context, now time.Time) (*time.Time, error) {
	page, _, err := s.Store.Resources(ctx, CollectionDeliveries, map[string]string{"state": "pending"}, "", 100)
	if err != nil {
		return nil, err
	}
	var earliest *time.Time
	for _, r := range page {
		var d deliveryDoc
		if r.Decode(&d) != nil {
			continue
		}
		if d.ScheduledFor != nil && d.ScheduledFor.After(now) {
			if earliest == nil || d.ScheduledFor.Before(*earliest) {
				t := *d.ScheduledFor
				earliest = &t
			}
			continue
		}
		destination, err := s.Destination(ctx, d.DestinationID)
		if err != nil || destination.Status != "verified" {
			d.State, d.Error = "failed", &ErrorDetail{Code: "destination_unverified", Message: "The destination was removed or is no longer verified."}
		} else {
			s.sendDelivery(ctx, &d, destination, Message{Title: d.Title, Body: d.Body, URL: d.URL})
		}
		d.UpdatedAt = &now
		next, err := state.NewResource(CollectionDeliveries, d.ID, r.Revision+1, d, map[string]string{"occurrence_id": d.OccurrenceID, "state": d.State}, now)
		if err != nil {
			return earliest, err
		}
		if _, err := s.Store.PutResource(ctx, next, r.Revision); err != nil && !errors.Is(err, state.ErrRevisionConflict) {
			return earliest, err
		}
		if d.State == "pending" && d.ScheduledFor != nil && (earliest == nil || d.ScheduledFor.Before(*earliest)) {
			t := *d.ScheduledFor
			earliest = &t
		}
	}
	return earliest, nil
}
