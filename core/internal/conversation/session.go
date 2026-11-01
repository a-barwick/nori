package conversation

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"nori/internal/state"
)

func (r *Runner) Snapshot(ctx context.Context) (Session, error) { return r.SnapshotPage(ctx, "") }

func (r *Runner) SnapshotPage(ctx context.Context, before string) (Session, error) {
	// These projections are independent. Keep each read's result local until all
	// finish so a slow history page does not serialize the other Firestore reads.
	var native state.Session
	var messages []Message
	var next string
	// Read completion before its projections: an idle snapshot must include the
	// reply committed with that status. Reading these concurrently can stop UI
	// polling on an older message list just as a turn completes.
	c, t, err := r.tasks.Store.ConversationStatus(ctx)
	if err != nil {
		return Session{}, err
	}
	var attempts []state.ConversationAttempt
	var readErrors [3]error
	var reads sync.WaitGroup
	reads.Go(func() { native, readErrors[0] = r.tasks.Store.ConversationProjection(ctx) })
	reads.Go(func() { messages, next, readErrors[1] = r.tasks.Store.ConversationMessages(ctx, before) })
	reads.Go(func() { attempts, readErrors[2] = r.tasks.Store.ConversationTraces(ctx) })
	reads.Wait()
	for _, err := range readErrors {
		if err != nil {
			return Session{}, err
		}
	}
	native.Messages = messages
	result := Session{
		Session: native, NextCursor: next, Dispatches: []Dispatch{}, Traces: []Trace{},
		Conversation: ConversationStatus{
			Introducing: c.Introducing, Pending: c.Received > c.Consumed, Processing: c.Active != "" && c.LeaseUntil.After(time.Now()),
			Revision: c.Received, Consumed: c.Consumed, InputIDs: []string{}, RoutingBlocked: c.UserReceived > c.UserRouted,
		},
	}
	if t != nil {
		result.Conversation.TurnID = t.ID
		result.Conversation.RestartCount = t.Restarts
		result.Conversation.Frozen = t.Frozen
		for _, in := range t.Inputs {
			result.Conversation.InputIDs = append(result.Conversation.InputIDs, in.ID)
		}
	}
	for _, a := range attempts {
		trace := Trace{
			ID: a.ID, TurnID: a.TurnID, Attempt: a.Number, InputIDs: a.InputIDs, Model: a.Model, Status: a.Status,
			StartedAt: a.StartedAt, FinishedAt: a.FinishedAt, DurationMS: a.DurationMS, Actions: a.Actions, Usage: a.Usage, Error: a.Error,
			ContextTruncated: a.ContextTruncated, ContextTurns: a.ContextTurns, ContextBytes: a.ContextBytes,
		}
		if trace.Actions == nil {
			trace.Actions = []state.ConversationAction{}
		}
		if trace.Usage == nil {
			trace.Usage = []state.Usage{}
		}
		if a.Hint != "" {
			trace.Classification = json.RawMessage(a.Hint)
			if result.Classification == nil {
				var h Classification
				if json.Unmarshal([]byte(a.Hint), &h) == nil {
					result.Classification = &h
				}
			}
		}
		result.Traces = append(result.Traces, trace)
		for _, action := range a.Actions {
			if action.Status == "accepted" && (action.Command.Kind == "start" || action.Command.Kind == "") {
				result.Dispatches = append(result.Dispatches, Dispatch{ID: action.ID, Request: action.Request, Status: action.Status, CreatedAt: action.CreatedAt})
			}
		}
		for i := range result.Messages {
			for _, id := range a.InputIDs {
				if result.Messages[i].ID == id+"-user" {
					result.Messages[i].TurnID = a.TurnID
				}
			}
		}
	}
	return result, nil
}
