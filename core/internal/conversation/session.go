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
	var native state.Session
	var messages []Message
	var next string
	position, err := r.tasks.Store.EventPosition(ctx)
	if err != nil {
		return Session{}, err
	}
	c, t, err := r.tasks.Store.ConversationStatus(ctx)
	if err != nil {
		return Session{}, err
	}
	var attempts []state.ConversationAttempt
	var results []state.Result
	var readPosition state.ReadMark
	var readErrors [5]error
	var reads sync.WaitGroup
	reads.Go(func() { native, readErrors[0] = r.tasks.Store.ConversationProjection(ctx) })
	reads.Go(func() { messages, next, readErrors[1] = r.tasks.Store.ConversationMessages(ctx, before) })
	reads.Go(func() { attempts, readErrors[2] = r.tasks.Store.ConversationTraces(ctx) })
	reads.Go(func() { results, readErrors[3] = r.tasks.Store.RecentResults(ctx, state.RecentResultLimit) })
	reads.Go(func() { readPosition, readErrors[4] = r.tasks.Store.ReadPosition(ctx) })
	reads.Wait()
	for _, err := range readErrors {
		if err != nil {
			return Session{}, err
		}
	}
	native.Messages = messages
	for i := range results {
		results[i] = results[i].Summary()
	}
	result := Session{
		Session: native, NextCursor: next, Dispatches: []Dispatch{}, Traces: []Trace{}, Position: position, Results: results, ReadPosition: readPosition,
		Conversation: conversationStatus(c, t, r.now()),
	}
	for _, a := range attempts {
		trace := Trace{
			ID: a.ID, TurnID: a.TurnID, Attempt: a.Number, InputIDs: a.InputIDs, Model: a.Model,
			ServedModel: a.ServedModel, ModelSelection: a.ModelSelection, Status: a.Status,
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
				if result.Messages[i].ID == state.UserMessageID(id) {
					result.Messages[i].TurnID = a.TurnID
				}
			}
		}
	}
	for i := range result.Messages {
		result.Messages[i] = result.Messages[i].WithParts()
	}
	return result, nil
}

func conversationStatus(c state.ConversationControl, t *state.ConversationTurn, now time.Time) ConversationStatus {
	status := ConversationStatus{
		Introducing: c.Introducing, Pending: c.Pending(), Processing: c.Processing(now),
		Revision: c.Received, Consumed: c.Consumed, InputIDs: []string{}, RoutingBlocked: c.RoutingPending(),
	}
	if t != nil {
		status.TurnID = t.ID
		status.RestartCount = t.Restarts
		status.Frozen = t.Frozen
		for _, in := range t.Inputs {
			status.InputIDs = append(status.InputIDs, in.ID)
		}
	}
	return status
}

type Presence struct {
	Conversation ConversationStatus `json:"conversation"`
	Paused       bool               `json:"paused"`
}

func (r *Runner) Presence(ctx context.Context) (Presence, error) {
	c, t, err := r.tasks.Store.ConversationStatus(ctx)
	if err != nil {
		return Presence{}, err
	}
	control, err := r.tasks.Store.Control(ctx)
	if err != nil {
		return Presence{}, err
	}
	return Presence{Conversation: conversationStatus(c, t, r.now()), Paused: control.Paused}, nil
}
