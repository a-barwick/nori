// Package dispatch delivers saved task commands to the task orchestration
// engine and repairs the gaps between Firestore and that engine.
//
// Firestore is the source of truth. A command is saved with a delivery intent,
// delivered to the engine, then marked delivered, so a crash between the last
// two steps only sends a duplicate. The Orchestrator contract keeps every
// caller of this package free of the engine behind it.
package dispatch

import (
	"context"
	"errors"
	"strings"

	"nori/internal/state"
)

// ErrNoExecution means the engine has no execution for the task.
var ErrNoExecution = errors.New("no execution for task")

// Orchestrator is what dispatch needs from the engine that runs task workflows.
// Temporal implements it in package task. Methods take task IDs, never engine
// identifiers.
type Orchestrator interface {
	// Signal delivers cmd to the task's running execution. It returns
	// ErrNoExecution when the engine has none.
	Signal(ctx context.Context, runID string, cmd state.Command) error
	// SignalOrStart delivers cmd to the task's execution and starts it if it does
	// not exist. An execution that already closed is not restarted.
	SignalOrStart(ctx context.Context, cmd state.Command) error
	// Inspect describes the task's execution. It returns ErrNoExecution when the
	// engine has never seen the task.
	Inspect(ctx context.Context, runID string) (Execution, error)
	// WorkerBuild names the immutable worker build that must run the task.
	WorkerBuild(ctx context.Context, runID string) (string, error)
	// PendingWork lists the tasks whose running executions have work waiting for
	// a worker. A non-nil error can accompany a partial list.
	PendingWork(ctx context.Context) ([]string, error)
	Close()
}

// Execution is the state of one task execution as the engine reports it.
type Execution struct {
	// Closed is true once the execution finished, failed or was cancelled.
	Closed bool
	// NeedsWorker is true while the execution has a workflow step, activity or
	// operation waiting for a worker.
	NeedsWorker bool
}

// Recovery repairs task state after an execution ended or cannot run. It needs
// the engine and the computer, so package task implements it.
type Recovery interface {
	// RepairClosed publishes the terminal state of tasks whose execution closed
	// before the task did, and finishes pending computer cleanup.
	RepairClosed(ctx context.Context) error
	// StopPaused completes an accepted Stop without starting any compute.
	StopPaused(ctx context.Context, runID string) (state.Run, error)
}

const workflowPrefix = "nori-task-"

// WorkflowID is the engine-facing identity of a task. Wake requests carry it,
// so it is part of the wire contract with workers.
func WorkflowID(runID string) string { return workflowPrefix + runID }

// RunID returns the task for a workflow ID, or false for a foreign identity.
func RunID(workflowID string) (string, bool) {
	return strings.TrimPrefix(workflowID, workflowPrefix), strings.HasPrefix(workflowID, workflowPrefix)
}
