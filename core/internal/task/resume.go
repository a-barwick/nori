package task

import (
	"context"
	"errors"
	"strings"

	"go.temporal.io/api/serviceerror"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"nori/internal/state"
)

// Explicit resume reconstructs disposable queue notifications from Temporal's
// durable running executions. This also finds terminal Firestore projections
// whose deferred activities outlived a long pause and Cloud Tasks retention.
// It is never a periodic controller and never changes an Activity retry policy.
func recoverExecutionWakes(ctx context.Context, c client.Client, seen map[string]bool) error {
	query := "ExecutionStatus = 'Running' AND TaskQueue = '" + strings.ReplaceAll(Queue(), "'", "''") + "'"
	request := &workflowservice.ListWorkflowExecutionsRequest{Query: query, PageSize: 100}
	var failures []error
	for {
		page, err := c.ListWorkflow(ctx, request)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		for _, item := range page.GetExecutions() {
			id := item.GetExecution().GetWorkflowId()
			if seen[id] || !strings.HasPrefix(id, "nori-task-") || item.GetType().GetName() != WorkflowName && item.GetType().GetName() != LegacyWorkflowName {
				continue
			}
			d, err := c.DescribeWorkflowExecution(ctx, id, item.GetExecution().GetRunId())
			var missing *serviceerror.NotFound
			if errors.As(err, &missing) {
				continue
			}
			if err != nil {
				failures = append(failures, err)
				continue
			}
			version := d.GetWorkflowExecutionInfo().GetVersioningInfo()
			deployment := version.GetDeploymentVersion().GetDeploymentName()
			if override := version.GetVersioningOverride().GetPinned().GetVersion(); override != nil {
				deployment = override.GetDeploymentName()
			} else if deployment == "" {
				if old := version.GetDeployment(); old != nil {
					deployment = old.GetSeriesName()
				} else {
					deployment, _, _ = strings.Cut(version.GetVersion(), ".")
				}
			}
			if deployment != "" && deployment != deploymentName() {
				continue
			}
			if d.GetPendingWorkflowTask() == nil && len(d.GetPendingActivities()) == 0 && len(d.GetPendingNexusOperations()) == 0 {
				continue
			}
			runID := strings.TrimPrefix(id, "nori-task-")
			if err = c.SignalWorkflow(ctx, id, item.GetExecution().GetRunId(), Signal, state.Command{RunID: runID, Kind: "resume"}); err != nil {
				if !errors.As(err, &missing) {
					failures = append(failures, err)
				}
				continue
			}
			if err = wakeWorker(ctx, c, runID); err != nil {
				failures = append(failures, err)
			}
			seen[id] = true
		}
		if len(page.GetNextPageToken()) == 0 {
			break
		}
		request.NextPageToken = page.GetNextPageToken()
	}
	return errors.Join(failures...)
}
