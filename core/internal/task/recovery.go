package task

import (
	"context"
	"errors"
	"fmt"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"nori/internal/computer"
	"nori/internal/state"
)

// RepairClosed handles the projection crash window only after Temporal proves
// execution is closed (or it never started and admission closed). It never
// retries execution. Known computer scopes must report their real stop outcome.
func RepairClosed(ctx context.Context, store state.TaskStore, c client.Client, config computer.Config) error {
	session, e := store.Session(ctx)
	if e != nil {
		return e
	}
	_, admissionErr := (state.Control{Paused: session.Paused, ExpiresAt: session.ExpiresAt}).Deadline()
	var failures []error
	for _, run := range session.Runs {
		if !run.Temporal || run.Status != "running" {
			continue
		}
		execution, e := c.DescribeWorkflowExecution(ctx, WorkflowID(run.ID), "")
		var missing *serviceerror.NotFound
		neverStarted := errors.As(e, &missing) && (admissionErr != nil || !run.Deadline.After(time.Now()))
		if e != nil && !neverStarted {
			if !errors.As(e, &missing) {
				failures = append(failures, e)
			}
			continue
		}
		if !neverStarted && execution.WorkflowExecutionInfo.Status == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
			continue
		}
		revisions, e := store.OperationRevisions(ctx, run.ID)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		scopes := []string{}
		for _, revision := range revisions {
			scopes = append(scopes, computerRun(Step{run.ID, revision}))
		}
		stopErr := StopComputer(ctx, config, scopes)
		status, detail := "failed", "Interrupted: Temporal execution ended before its terminal result was persisted; work was not retried."
		if neverStarted {
			detail = "Interrupted: task admission closed before Temporal execution started."
		}
		if run.CancelRequested && stopErr == nil {
			status = "cancelled"
			detail = "Cancelled by Austin."
		}
		if stopErr != nil {
			status = "failed"
			detail = "Interrupted: computer stop could not be confirmed during terminal recovery."
			failures = append(failures, stopErr)
		}
		if _, e = store.Publish(ctx, run.ID, run.Revision, status, detail, ""); e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}

// Drain keeps the sole poller alive so cancellation, guest Stop ACK and terminal
// publication can run before process exit. Cloud pool scaling remains operator
// infrastructure work; exiting this function does not scale a pool to zero.
func Drain(ctx context.Context, store state.TaskStore, c client.Client, config computer.Config) error {
	session, e := store.Session(ctx)
	if e != nil {
		return e
	}
	for _, run := range session.Runs {
		if run.Temporal && run.Status == "running" {
			if e = c.CancelWorkflow(ctx, WorkflowID(run.ID), ""); e != nil {
				var missing *serviceerror.NotFound
				if !errors.As(e, &missing) {
					return e
				}
			}
		}
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if e = RepairClosed(ctx, store, c, config); e != nil {
			return e
		}
		session, e = store.Session(ctx)
		if e != nil {
			return e
		}
		active := false
		for _, run := range session.Runs {
			if run.Temporal && run.Status == "running" {
				active = true
			}
		}
		if !active {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("task cleanup drain incomplete: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
