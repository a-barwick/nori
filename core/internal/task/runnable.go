package task

import (
	"context"
	"fmt"
	"strings"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"nori/internal/state"
)

// Runnable inspects Temporal, not the UI's question/status projection. In
// particular a terminal task may still have Spend/CloseBrowser work queued.
// known contains every workflow observed by the worker before stopping polling.
func Runnable(ctx context.Context, store state.TaskStore, c client.Client, build string, known []string) ([]string, error) {
	var runnable []string
	ids := map[string]bool{}
	cleanupIDs := map[string]bool{}
	for _, id := range known {
		ids[id] = true
	}
	runs, err := store.OpenTasks(ctx)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		cleanupIDs[WorkflowID(run.ID)] = true
	}
	cleanup, err := store.CleanupTasks(ctx)
	if err != nil {
		return nil, err
	}
	for _, run := range cleanup {
		cleanupIDs[WorkflowID(run.ID)] = true
	}
	for _, run := range append(runs, cleanup...) {
		ids[WorkflowID(run.ID)] = true
	}
	for id := range ids {
		d, err := c.DescribeWorkflowExecution(ctx, id, "")
		if _, missing := err.(*serviceerror.NotFound); missing {
			continue
		}
		if err != nil {
			return nil, err
		}
		closed := d.GetWorkflowExecutionInfo().GetStatus() != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING
		repair := closed && cleanupIDs[id]
		if closed && !repair {
			continue
		}
		if !repair && d.GetPendingWorkflowTask() == nil && len(d.GetPendingActivities()) == 0 && len(d.GetPendingNexusOperations()) == 0 {
			continue
		}
		if !strings.HasPrefix(id, "nori-task-") {
			return nil, fmt.Errorf("unexpected task workflow identity")
		}
		assigned, err := WorkerBuild(ctx, c, strings.TrimPrefix(id, "nori-task-"))
		if err != nil {
			return nil, err
		}
		if assigned == build {
			runnable = append(runnable, id)
		}
	}
	return runnable, nil
}
