package task

import (
	"context"
	"fmt"
	"strings"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
)

// WorkerBuild resolves the execution's actual pinned version. Only a workflow
// that has not completed its first workflow task can use the deployment route.
func (e *Engine) WorkerBuild(ctx context.Context, id string) (string, error) {
	c := e.c
	execution, err := c.DescribeWorkflowExecution(ctx, WorkflowID(id), "")
	if err != nil {
		return "", err
	}
	v := execution.GetWorkflowExecutionInfo().GetVersioningInfo()
	if override := v.GetVersioningOverride(); override != nil {
		if pinned := override.GetPinned().GetVersion(); pinned != nil {
			if pinned.GetDeploymentName() != deploymentName() || pinned.GetBuildId() == "" {
				return "", fmt.Errorf("unexpected pinned deployment override")
			}
			return pinned.GetBuildId(), nil
		}
		if pinned := override.GetPinnedVersion(); pinned != "" {
			deployment, build, ok := strings.Cut(pinned, ".")
			if !ok || deployment != deploymentName() || build == "" {
				return "", fmt.Errorf("unsupported pinned deployment version %q", pinned)
			}
			return build, nil
		}
	}
	if version := v.GetDeploymentVersion(); version != nil {
		if version.GetDeploymentName() != deploymentName() || version.GetBuildId() == "" {
			return "", fmt.Errorf("unexpected pinned worker deployment")
		}
		return version.GetBuildId(), nil
	}
	if version := v.GetVersion(); version != "" {
		deployment, build, ok := strings.Cut(version, ".")
		if !ok || deployment != deploymentName() || build == "" {
			return "", fmt.Errorf("unsupported retained worker version %q", version)
		}
		return build, nil
	}
	if version := v.GetDeployment(); version != nil {
		if version.GetSeriesName() != deploymentName() || version.GetBuildId() == "" {
			return "", fmt.Errorf("unexpected retained worker deployment")
		}
		return version.GetBuildId(), nil
	}
	// More than one signal may arrive before the first workflow task. History
	// length therefore cannot distinguish a new unassigned execution from an old
	// unversioned one. Inspect the first completed task instead of guessing.
	history := c.GetWorkflowHistory(ctx, WorkflowID(id), "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for history.HasNext() {
		event, err := history.Next()
		if err != nil {
			return "", err
		}
		if event.GetEventType() == enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED {
			return "", fmt.Errorf("existing workflow has no supported pinned worker version")
		}
	}
	route, err := c.WorkerDeploymentClient().GetHandle(deploymentName()).Describe(ctx, client.WorkerDeploymentDescribeOptions{})
	if err != nil {
		return "", err
	}
	if route.Info.RoutingConfig.RampingVersionPercentage != 0 {
		return "", fmt.Errorf("worker ramping requires an explicit wake route")
	}
	version := route.Info.RoutingConfig.CurrentVersion
	if version == nil || version.BuildID == "" {
		return "", fmt.Errorf("no current immutable worker version")
	}
	return version.BuildID, nil
}

func deploymentName() string {
	if configured.DeploymentName != "" {
		return configured.DeploymentName
	}
	return "nori-task"
}
