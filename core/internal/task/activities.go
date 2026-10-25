// Package task owns one bounded Temporal task. It calls Go agent steps directly.
package task

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/genai"
	"nori/internal/agent"
	"nori/internal/computer"
	"nori/internal/state"
)

type Activities struct {
	Agent       *agent.Service
	Store       state.TaskStore
	CleanupOnly bool
}
type Step struct {
	RunID    string
	Revision int64
}
type ModelInput struct {
	Step
	Contents []*genai.Content
}
type ToolInput struct {
	Step
	OperationID string
	Call        *genai.FunctionCall
}
type Publication struct {
	Step
	Status, Detail, Text string
	StopConfirmed        bool
}
type Projection struct {
	Step
	Kind, Text string
}

func classified(err error) error {
	if err == nil {
		return nil
	}
	kind := "activity_failed"
	switch {
	case errors.Is(err, state.ErrSuperseded):
		kind = "superseded"
	case errors.Is(err, state.ErrCancelled):
		kind = "cancelled"
	case errors.Is(err, state.ErrPaused):
		kind = "paused"
	}
	return temporal.NewNonRetryableApplicationError(err.Error(), kind, err)
}
func (a *Activities) Context(ctx context.Context, id string) ([]*genai.Content, error) {
	session, e := a.Store.Session(ctx)
	if e != nil {
		return nil, classified(e)
	}
	// Accepted task commands are incorporated in their durable order separately.
	history := session.Messages[:0]
	for _, m := range session.Messages {
		if m.RunID != id {
			history = append(history, m)
		}
	}
	session.Messages = history
	return agent.Context(session), nil
}
func (a *Activities) Reconcile(ctx context.Context, id string) (state.TaskSnapshot, error) {
	snap, e := a.Store.Reconcile(ctx, id)
	if e != nil {
		return snap, classified(e)
	}
	if snap.Run.CancelRequested {
		return snap, classified(state.ErrCancelled)
	}
	if _, e = a.Store.Control(ctx); e != nil {
		return snap, classified(e)
	}
	e = a.Store.Incorporate(ctx, id, snap.Run.Revision, snap.Run.Revision)
	if e != nil {
		return snap, classified(e)
	}
	_, e = a.Store.Guard(ctx, id, snap.Run.Revision)
	return snap, classified(e)
}

// bounded tracks pause, expiry and Stop throughout I/O. Corrections invalidate
// the next boundary; already admitted bounded work is allowed to finish.
func (a *Activities) bounded(parent context.Context, step Step) (context.Context, func(), error) {
	if a.CleanupOnly {
		return nil, nil, classified(state.ErrPaused)
	}
	run, e := a.Store.Guard(parent, step.RunID, step.Revision)
	if e != nil {
		return nil, nil, classified(e)
	}
	ctx, cancel := context.WithDeadline(parent, run.Deadline)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				activity.RecordHeartbeat(ctx)
				current, e := a.Store.GetRun(ctx, step.RunID)
				if e != nil || current.CancelRequested || current.Status != "running" {
					cancel()
					return
				}
				control, e := a.Store.Control(ctx)
				if e != nil {
					cancel()
					return
				}
				if _, e = control.Deadline(); e != nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(); <-done }, nil
}

// ioError distinguishes durable Stop from an ordinary canceled HTTP request.
func (a *Activities) ioError(step Step, err error) error {
	if err == nil {
		return nil
	}
	check, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	current, readErr := a.Store.GetRun(check, step.RunID)
	if readErr == nil && current.CancelRequested {
		return classified(state.ErrCancelled)
	}
	return classified(err)
}
func (a *Activities) Model(ctx context.Context, in ModelInput) (agent.Decision, error) {
	ctx, done, e := a.bounded(ctx, in.Step)
	if e != nil {
		return agent.Decision{}, e
	}
	defer done()
	modelCtx, stop := context.WithTimeout(ctx, 45*time.Second)
	defer stop()
	decision, e := a.Agent.Decide(modelCtx, in.Contents, true)
	return decision, a.ioError(in.Step, e)
}
func (a *Activities) Tool(ctx context.Context, in ToolInput) (map[string]any, error) {
	ctx, done, e := a.bounded(ctx, in.Step)
	if e != nil {
		return nil, e
	}
	defer done()
	if e = a.Store.Admit(ctx, in.RunID, in.Revision, in.OperationID); e != nil {
		return nil, classified(e)
	}
	c, e := computer.Open(ctx, a.Agent.Computer, computerRun(in.Step))
	if e != nil {
		return nil, a.ioError(in.Step, e)
	}
	defer c.Close()
	if e = c.RequireReceipts(); e != nil {
		return nil, classified(e)
	}
	c.SetOperation(in.OperationID)
	c.SetTaskID(in.RunID)
	// Starting the guest and connecting IAP can consume up to 90 seconds; execution
	// itself retains the same 45-second limit as the inline path.
	if _, e = a.Store.Guard(ctx, in.RunID, in.Revision); e != nil {
		return nil, classified(e)
	}
	toolCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	result, e := a.Agent.Tool(toolCtx, in.RunID, c, in.Call)
	cancel()
	if e != nil {
		return nil, a.ioError(in.Step, fmt.Errorf("tool outcome failed or uncertain; operation %s is not retried: %w", in.OperationID, e))
	}
	return result, nil
}
func computerRun(step Step) string { return fmt.Sprintf("%s-r%d", step.RunID, step.Revision) }
func (a *Activities) Stop(ctx context.Context, ids []string) error {
	return classified(StopComputer(ctx, a.Agent.Computer, ids))
}
func StopComputer(ctx context.Context, config computer.Config, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	c, e := computer.Attach(ctx, config, ids[0])
	if errors.Is(e, computer.ErrStopped) {
		return nil
	}
	if e != nil {
		return e
	}
	defer c.Close()
	for _, id := range ids {
		c.SetRun(id)
		if e = c.Cancel(ctx); e != nil {
			return e
		}
	}
	return nil
}
func (a *Activities) Publish(ctx context.Context, p Publication) (state.Run, error) {
	if p.Status != "completed" && p.StopConfirmed {
		current, e := a.Store.GetRun(ctx, p.RunID)
		if e != nil {
			return state.Run{}, classified(e)
		}
		if current.CancelRequested {
			p.Status = "cancelled"
			p.Detail = "Cancelled by Austin."
			p.Text = ""
		}
	}
	r, e := a.Store.Publish(ctx, p.RunID, p.Revision, p.Status, p.Detail, p.Text)
	return r, classified(e)
}
func (a *Activities) Project(ctx context.Context, p Projection) error {
	return classified(a.Store.Project(ctx, p.RunID, p.Revision, p.Kind, p.Text))
}
