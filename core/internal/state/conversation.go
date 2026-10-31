package state

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"nori/internal/observability"
)

var ErrConversationBusy = errors.New("conversation lease is held by another processor")
var ErrConversationConflict = errors.New("message ID already accepted with different text")
var ErrConversationStale = errors.New("conversation attempt superseded")

const ConversationLease = 30 * time.Second

type ConversationInput struct {
	ID           string            `firestore:"id"`
	Text         string            `firestore:"text"`
	Position     int64             `firestore:"position"`
	CreatedAt    time.Time         `firestore:"created_at"`
	Introduction bool              `firestore:"introduction,omitempty"`
	Event        *TaskEvent        `firestore:"event,omitempty"`
	TraceContext map[string]string `json:"-" firestore:"trace_context,omitempty"`
}
type ConversationControl struct {
	ProtocolVersion int       `firestore:"protocol_version,omitempty"`
	UserReceived    int64     `firestore:"user_received,omitempty"`
	UserRouted      int64     `firestore:"user_routed,omitempty"`
	Introducing     bool      `firestore:"introducing,omitempty"`
	Received        int64     `firestore:"received"`
	Consumed        int64     `firestore:"consumed"`
	Active          string    `firestore:"active"`
	Owner           string    `firestore:"owner"`
	Fence           int64     `firestore:"fence"`
	LeaseUntil      time.Time `firestore:"lease_until"`
	LatestAt        time.Time `firestore:"latest_at"`
}
type ConversationAction struct {
	ID        string    `json:"id" firestore:"id"`
	Request   string    `json:"request" firestore:"request"`
	Bytes     int       `json:"bytes" firestore:"bytes"`
	Status    string    `json:"status" firestore:"status"`
	Error     string    `json:"error,omitempty" firestore:"error,omitempty"`
	CreatedAt time.Time `json:"created_at" firestore:"created_at"`
	Command   Command   `json:"command" firestore:"command"`
}
type ConversationAttempt struct {
	ID               string               `firestore:"id"`
	TurnID           string               `firestore:"turn_id"`
	Number           int                  `firestore:"number"`
	InputIDs         []string             `firestore:"input_ids"`
	Model            string               `firestore:"model"`
	Status           string               `firestore:"status"`
	StartedAt        time.Time            `firestore:"started_at"`
	FinishedAt       *time.Time           `firestore:"finished_at,omitempty"`
	DurationMS       int64                `firestore:"duration_ms"`
	Hint             string               `firestore:"hint"`
	Reply            string               `firestore:"reply"`
	ProtocolInvalid  bool                 `firestore:"protocol_invalid"`
	Text             string               `firestore:"text"`
	Results          []string             `firestore:"results"`
	Actions          []ConversationAction `firestore:"actions"`
	Usage            []Usage              `firestore:"usage"`
	Error            string               `firestore:"error"`
	ContextTruncated bool                 `firestore:"context_truncated"`
	ContextTurns     int                  `firestore:"context_turns"`
	ContextBytes     int                  `firestore:"context_bytes"`
}
type ConversationTurn struct {
	ID          string              `firestore:"id"`
	Inputs      []ConversationInput `firestore:"inputs"`
	Through     int64               `firestore:"through"`
	UserThrough int64               `firestore:"user_through,omitempty"`
	Restarts    int                 `firestore:"restarts"`
	Frozen      bool                `firestore:"frozen"`
	StartedAt   time.Time           `firestore:"started_at"`
	FinishedAt  *time.Time          `firestore:"finished_at,omitempty"`
	Status      string              `firestore:"status"`
	Protocol    []string            `firestore:"protocol"`
	Context     []string            `firestore:"context"`
	Attempt     ConversationAttempt `firestore:"attempt"`
}
type ConversationFence struct {
	Owner       string
	Fence       int64
	TurnID      string
	Through     int64
	UserThrough int64
	Frozen      bool
}

func (s *Google) conversation() *firestore.DocumentRef {
	return s.root().Collection("conversation").Doc("control")
}
func (s *Google) conversationInputs() *firestore.CollectionRef {
	return s.root().Collection("conversation_inputs")
}
func (s *Google) conversationTurns() *firestore.CollectionRef {
	return s.root().Collection("conversation_turns")
}
func (s *Google) conversationAttempts() *firestore.CollectionRef {
	return s.root().Collection("conversation_attempts")
}
func readConversation(tx *firestore.Transaction, ref *firestore.DocumentRef) (ConversationControl, error) {
	var c ConversationControl
	d, e := tx.Get(ref)
	if status.Code(e) == codes.NotFound {
		c.ProtocolVersion = 2
		return c, nil
	}
	if e != nil {
		return c, e
	}
	e = d.DataTo(&c)
	normalizeConversation(&c)
	return c, e
}

func normalizeConversation(c *ConversationControl) {
	if c.ProtocolVersion < 2 {
		c.UserReceived, c.UserRouted = c.Received, c.Consumed
		c.ProtocolVersion = 2
	}
}

func (in ConversationInput) IsUser() bool { return !in.Introduction && in.Event == nil }

func userThrough(inputs []ConversationInput) int64 {
	var through int64
	for _, in := range inputs {
		if in.IsUser() && in.Position > through {
			through = in.Position
		}
	}
	return through
}
func (s *Google) ConversationStatus(ctx context.Context) (ConversationControl, *ConversationTurn, error) {
	var c ConversationControl
	d, e := s.conversation().Get(ctx)
	if status.Code(e) == codes.NotFound {
		return c, nil, nil
	}
	if e != nil {
		return c, nil, e
	}
	if e = d.DataTo(&c); e != nil {
		return c, nil, e
	}
	normalizeConversation(&c)
	if c.Active == "" {
		return c, nil, nil
	}
	d, e = s.conversationTurns().Doc(c.Active).Get(ctx)
	if e != nil {
		return c, nil, e
	}
	var t ConversationTurn
	e = d.DataTo(&t)
	if t.UserThrough == 0 {
		t.UserThrough = userThrough(t.Inputs)
	}
	return c, &t, e
}
func (s *Google) AcceptConversationInput(ctx context.Context, id, text string) error {
	traceContext := observability.InjectTrace(ctx)
	return s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		d, e := tx.Get(s.conversationInputs().Doc(id))
		if e == nil {
			var old ConversationInput
			if e = d.DataTo(&old); e != nil {
				return e
			}
			if old.Text != text {
				return ErrConversationConflict
			}
			return nil
		}
		if status.Code(e) != codes.NotFound {
			return e
		}
		c, e := readConversation(tx, s.conversation())
		if e != nil {
			return e
		}
		now := time.Now().UTC()
		c.Received++
		c.UserReceived = c.Received
		c.LatestAt = now
		in := ConversationInput{ID: id, Text: text, Position: c.Received, CreatedAt: now, TraceContext: traceContext}
		if e = tx.Set(s.conversation(), c); e != nil {
			return e
		}
		if e = tx.Create(s.conversationInputs().Doc(id), in); e != nil {
			return e
		}
		return tx.Create(s.messages().Doc(id+"-user"), Message{ID: id + "-user", Role: "user", Text: text, CreatedAt: now, TurnID: id})
	})
}
func (s *Google) conversationBatch(tx *firestore.Transaction, after int64) ([]ConversationInput, error) {
	it := tx.Documents(s.conversationInputs().Where("position", ">", after).OrderBy("position", firestore.Asc).Limit(64))
	defer it.Stop()
	out := []ConversationInput{}
	// Keep the turn document comfortably below Firestore's 1 MiB document limit.
	bytes := 0
	for {
		d, e := it.Next()
		if e == iterator.Done {
			break
		}
		if e != nil {
			return nil, e
		}
		var in ConversationInput
		if e = d.DataTo(&in); e != nil {
			return nil, e
		}
		inputBytes := len(in.Text)
		if in.Event != nil {
			inputBytes += len(in.Event.Text)
			for _, hold := range in.Event.Holds {
				inputBytes += len(hold.Question)
			}
		}
		if bytes+inputBytes > 128<<10 && len(out) > 0 {
			break
		}
		bytes += inputBytes
		out = append(out, in)
	}
	return out, nil
}
func (s *Google) AcquireConversation(ctx context.Context, owner, turnID string) (ConversationControl, *ConversationTurn, error) {
	var c ConversationControl
	var turn *ConversationTurn
	e := s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		var e error
		c, e = readConversation(tx, s.conversation())
		if e != nil {
			return e
		}
		now := time.Now().UTC()
		if c.Owner != "" && c.LeaseUntil.After(now) {
			return ErrConversationBusy
		}
		if c.Active == "" && c.Received <= c.Consumed {
			return nil
		}
		var t ConversationTurn
		if c.Active != "" {
			d, e := tx.Get(s.conversationTurns().Doc(c.Active))
			if e != nil {
				return e
			}
			if e = d.DataTo(&t); e != nil {
				return e
			}
			if t.UserThrough == 0 {
				t.UserThrough = userThrough(t.Inputs)
			}
		} else {
			ins, e := s.conversationBatch(tx, c.Consumed)
			if e != nil {
				return e
			}
			if len(ins) == 0 {
				return nil
			}
			t = ConversationTurn{ID: turnID, Inputs: ins, Through: ins[len(ins)-1].Position, StartedAt: now, Status: "running"}
			// A failed routing attempt stays unresolved until the next user turn.
			// Replay its inputs as context without creating another visible message.
			if userThrough(ins) > 0 && c.UserRouted < c.Consumed {
				unresolved, err := s.unroutedInputs(tx, c.UserRouted, c.Consumed)
				if err != nil {
					return err
				}
				t.Inputs = append(unresolved, ins...)
			}
			t.UserThrough = max(userThrough(t.Inputs), min(c.UserReceived, c.Consumed))
			t.Frozen = t.Through < c.Received
			c.Active = t.ID
		}
		c.Owner = owner
		c.Fence++
		c.LeaseUntil = now.Add(ConversationLease)
		turn = &t
		if e = tx.Set(s.conversation(), c); e != nil {
			return e
		}
		for _, in := range t.Inputs {
			if !in.IsUser() {
				continue
			}
			if e = tx.Update(s.messages().Doc(in.ID+"-user"), []firestore.Update{{Path: "turn_id", Value: t.ID}}); e != nil {
				return e
			}
		}
		return tx.Set(s.conversationTurns().Doc(t.ID), t)
	})
	return c, turn, e
}
func conversationOwned(c ConversationControl, f ConversationFence) error {
	if c.Owner != f.Owner || c.Fence != f.Fence || c.Active != f.TurnID || !c.LeaseUntil.After(time.Now()) {
		return ErrConversationBusy
	}
	return nil
}
func conversationFresh(c ConversationControl, f ConversationFence) error {
	if e := conversationOwned(c, f); e != nil {
		return e
	}
	if !f.Frozen && c.UserReceived > f.UserThrough {
		return ErrConversationStale
	}
	return nil
}
func (s *Google) CheckConversation(ctx context.Context, f ConversationFence) (bool, error) {
	d, e := s.conversation().Get(ctx)
	if e != nil {
		return false, e
	}
	var c ConversationControl
	if e = d.DataTo(&c); e != nil {
		return false, e
	}
	normalizeConversation(&c)
	if e = conversationOwned(c, f); e != nil {
		return false, e
	}
	return !f.Frozen && c.UserReceived > f.UserThrough, nil
}
func (s *Google) RenewConversation(ctx context.Context, f ConversationFence) (bool, error) {
	stale := false
	e := s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		c, e := readConversation(tx, s.conversation())
		if e != nil {
			return e
		}
		if e = conversationOwned(c, f); e != nil {
			return e
		}
		stale = !f.Frozen && c.UserReceived > f.UserThrough
		c.LeaseUntil = time.Now().UTC().Add(ConversationLease)
		return tx.Set(s.conversation(), c)
	})
	return stale, e
}
func (s *Google) ReleaseConversation(ctx context.Context, f ConversationFence) error {
	return s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		c, e := readConversation(tx, s.conversation())
		if e != nil {
			return e
		}
		if e = conversationOwned(c, f); e != nil {
			return e
		}
		c.Owner = ""
		c.LeaseUntil = time.Time{}
		return tx.Set(s.conversation(), c)
	})
}

// SaveConversation fences every checkpoint, including model replies and failed attempts.
func (s *Google) SaveConversation(ctx context.Context, f ConversationFence, t ConversationTurn) error {
	return s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		c, e := readConversation(tx, s.conversation())
		if e != nil {
			return e
		}
		if e = conversationOwned(c, f); e != nil {
			return e
		}
		if e = tx.Set(s.conversationTurns().Doc(t.ID), t); e != nil {
			return e
		}
		if t.Attempt.ID != "" {
			return tx.Set(s.conversationAttempts().Doc(t.Attempt.ID), t.Attempt)
		}
		return nil
	})
}

// RestartConversation incorporates the full waiting batch only after one quiet second.
// The fourth attempt freezes its coverage; later input remains queued for the next turn.
func (s *Google) RestartConversation(ctx context.Context, f ConversationFence, t ConversationTurn) (ConversationTurn, time.Duration, error) {
	var wait time.Duration
	original := t
	e := s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		t = original
		c, e := readConversation(tx, s.conversation())
		if e != nil {
			return e
		}
		if e = conversationOwned(c, f); e != nil {
			return e
		}
		if c.UserReceived <= t.UserThrough || t.Frozen {
			return nil
		}
		wait = time.Until(c.LatestAt.Add(time.Second))
		if wait > 0 {
			return nil
		}
		ins, e := s.conversationBatch(tx, c.Consumed)
		if e != nil {
			return e
		}
		if c.UserRouted < c.Consumed {
			unresolved, err := s.unroutedInputs(tx, c.UserRouted, c.Consumed)
			if err != nil {
				return err
			}
			ins = append(unresolved, ins...)
		}
		through := ins[len(ins)-1].Position
		if through == t.Through {
			// The waiting suffix cannot fit this batch. Keep its original coverage and
			// attempt; later inputs will be handled by the next turn.
			t.Frozen = true
			return tx.Set(s.conversationTurns().Doc(t.ID), t)
		}
		if t.Attempt.ID != "" {
			accepted := false
			for _, a := range t.Attempt.Actions {
				accepted = accepted || a.Status == "accepted"
			}
			if accepted && t.Attempt.Reply != "" {
				t.Context = append(t.Context, t.Attempt.Reply)
				t.Context = append(t.Context, t.Attempt.Results...)
			}
			now := time.Now().UTC()
			t.Attempt.Status = "superseded"
			t.Attempt.Error = ""
			t.Attempt.FinishedAt = &now
			t.Attempt.DurationMS = now.Sub(t.Attempt.StartedAt).Milliseconds()
			if e = tx.Set(s.conversationAttempts().Doc(t.Attempt.ID), t.Attempt); e != nil {
				return e
			}
		}
		t.Inputs = ins
		t.Through = through
		t.UserThrough = max(userThrough(ins), min(c.UserReceived, c.Consumed))
		t.Restarts++
		t.Frozen = t.Restarts >= 3 || t.Through < c.Received
		t.Attempt = ConversationAttempt{}
		for _, in := range t.Inputs {
			if !in.IsUser() {
				continue
			}
			if e = tx.Update(s.messages().Doc(in.ID+"-user"), []firestore.Update{{Path: "turn_id", Value: t.ID}}); e != nil {
				return e
			}
		}
		return tx.Set(s.conversationTurns().Doc(t.ID), t)
	})
	return t, wait, e
}
func (s *Google) PublishConversation(ctx context.Context, f ConversationFence, t ConversationTurn, text string) error {
	return s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		c, e := readConversation(tx, s.conversation())
		if e != nil {
			return e
		}
		if e = conversationFresh(c, f); e != nil {
			return e
		}
		heldRuns, e := s.holdAcknowledgements(tx, t, text)
		if e != nil {
			return e
		}
		now := time.Now().UTC()
		t.Status = "completed"
		t.FinishedAt = &now
		c.Consumed = t.Through
		if t.Attempt.Status != "error" {
			c.UserRouted = max(c.UserRouted, userThrough(t.Inputs))
		}
		c.Introducing = false
		c.Active = ""
		c.Owner = ""
		c.LeaseUntil = time.Time{}
		for id, run := range heldRuns {
			if e = tx.Set(s.runs().Doc(id), run); e != nil {
				return e
			}
		}
		if e = tx.Set(s.conversation(), c); e != nil {
			return e
		}
		if e = tx.Set(s.conversationTurns().Doc(t.ID), t); e != nil {
			return e
		}
		if e = tx.Set(s.conversationAttempts().Doc(t.Attempt.ID), t.Attempt); e != nil {
			return e
		}
		return tx.Set(s.messages().Doc(t.ID+"-assistant"), Message{ID: t.ID + "-assistant", Role: "assistant", Text: text, CreatedAt: now, TurnID: t.ID})
	})
}
func (s *Google) ConversationHistory(ctx context.Context) ([]ConversationTurn, bool, error) {
	it := s.conversationTurns().OrderBy("started_at", firestore.Desc).Limit(25).Documents(ctx)
	defer it.Stop()
	out := []ConversationTurn{}
	size := 0
	examined := 0
	truncated := false
	for {
		d, e := it.Next()
		if e == iterator.Done {
			break
		}
		if e != nil {
			return nil, false, e
		}
		examined++
		if examined >= 25 {
			truncated = true
		}
		var t ConversationTurn
		if e = d.DataTo(&t); e != nil {
			return nil, false, e
		}
		if t.Status != "completed" {
			continue
		}
		n := 0
		for _, p := range t.Protocol {
			n += len(p)
		}
		if size+n > 192<<10 || len(out) >= 24 {
			truncated = true
			break
		}
		size += n
		out = append(out, t)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, truncated, nil
}
func (s *Google) ConversationTraces(ctx context.Context) ([]ConversationAttempt, error) {
	it := s.conversationAttempts().OrderBy("started_at", firestore.Desc).Limit(60).Documents(ctx)
	defer it.Stop()
	out := []ConversationAttempt{}
	for {
		d, e := it.Next()
		if e == iterator.Done {
			break
		}
		if e != nil {
			return nil, e
		}
		var a ConversationAttempt
		if e = d.DataTo(&a); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, nil
}

// conversationAdmission is part of Accept's transaction: input acceptance and new
// task admission serialize on the same conversation control document.
func (s *Google) conversationAdmission(tx *firestore.Transaction, f *ConversationFence) error {
	if f == nil {
		return nil
	}
	c, e := readConversation(tx, s.conversation())
	if e != nil {
		return e
	}
	return conversationFresh(c, *f)
}
func (s *Google) ConversationMessages(ctx context.Context, before string) ([]Message, string, error) {
	q := s.messages().OrderBy("created_at", firestore.Desc).OrderBy(firestore.DocumentID, firestore.Desc)
	if before != "" {
		d, e := s.messages().Doc(before).Get(ctx)
		if e != nil {
			return nil, "", fmt.Errorf("invalid history cursor: %w", e)
		}
		q = q.StartAfter(d)
	}
	it := q.Limit(81).Documents(ctx)
	defer it.Stop()
	out := []Message{}
	next := ""
	for {
		d, e := it.Next()
		if e == iterator.Done {
			break
		}
		if e != nil {
			return nil, "", e
		}
		if len(out) == 80 {
			next = out[len(out)-1].ID
			break
		}
		var m Message
		if e = d.DataTo(&m); e != nil {
			return nil, "", e
		}
		out = append(out, m)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, next, nil
}
