package task

import (
	"context"
	"errors"
	"strings"

	"go.temporal.io/api/serviceerror"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"nori/internal/dispatch"
)

// PendingWork lists the tasks whose running executions have a workflow step,
// activity or operation waiting for a worker. Explicit resume uses it to rebuild
// disposable queue notifications from Temporal's durable state. It is never a
// periodic controller and never changes an Activity retry policy.
func (e *Engine) PendingWork(ctx context.Context) ([]string, error) {
	query := "ExecutionStatus = 'Running' AND TaskQueue = '" + strings.ReplaceAll(Queue(), "'", "''") + "'"
	request := &workflowservice.ListWorkflowExecutionsRequest{Query: query, PageSize: 100}
	var ids []string
	var failures []error
	for {
		page, err := e.c.ListWorkflow(ctx, request)
		if err != nil {
			return ids, errors.Join(append(failures, err)...)
		}
		for _, item := range page.GetExecutions() {
			id := item.GetExecution().GetWorkflowId()
			runID, ok := dispatch.RunID(id)
			if !ok || item.GetType().GetName() != WorkflowName {
				continue
			}
			d, err := e.c.DescribeWorkflowExecution(ctx, id, item.GetExecution().GetRunId())
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
			ids = append(ids, runID)
		}
		if len(page.GetNextPageToken()) == 0 {
			break
		}
		request.NextPageToken = page.GetNextPageToken()
	}
	return ids, errors.Join(failures...)
}
