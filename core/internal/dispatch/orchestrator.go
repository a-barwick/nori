// Package dispatch delivers saved task commands. It acknowledges a command
// after signaling the engine and scheduling any required worker wake. Recovery
// can resend the same command ID after a crash in that window.
package dispatch

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"nori/internal/state"
	"nori/internal/users"
)

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

type Execution struct {
	Closed bool
	// NeedsWorker is true while the execution has a workflow step, activity or
	// operation waiting for a worker.
	NeedsWorker bool
}

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
func WorkflowID(userID, runID string) string {
	if userID == users.LegacyID {
		return workflowPrefix + runID
	}
	return workflowPrefix + userID + ":" + runID
}

// WorkflowUser recognizes both retained legacy executions and explicitly scoped ones.
func WorkflowUser(id string) (userID, runID string, ok bool) {
	if !strings.HasPrefix(id, workflowPrefix) {
		return "", "", false
	}
	tail := strings.TrimPrefix(id, workflowPrefix)
	userID, runID, scoped := strings.Cut(tail, ":")
	if !scoped {
		userID, runID = users.LegacyID, tail
	}
	parsed, err := uuid.Parse(runID)
	return userID, runID, users.ValidID(userID) && err == nil && parsed.String() == runID
}

func RunID(userID, id string) (string, bool) {
	owner, run, ok := WorkflowUser(id)
	return run, ok && owner == userID
}
