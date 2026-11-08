package taskworker

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"nori/internal/task"
)

// Register makes Temporal create a fresh, non-current worker deployment version
// by polling its task queues, then stops. It never enters the normal worker's
// recovery or delivery loop and never constructs a model or computer client.
// The version must not be current or ramping, and must not run any execution.
func Register(ctx context.Context, c client.Client, taskQueue, deploymentName, build string) (result error) {
	handle := c.WorkerDeploymentClient().GetHandle(deploymentName)
	_, err := handle.DescribeVersion(ctx, client.WorkerDeploymentDescribeVersionOptions{BuildID: build})
	var missing *serviceerror.NotFound
	if !errors.As(err, &missing) {
		if err != nil {
			return err
		}
		return fmt.Errorf("registration requires a fresh deployment version")
	}
	guard := &registrationGuard{}
	w := worker.New(c, taskQueue, worker.Options{
		Interceptors: []interceptor.WorkerInterceptor{guard}, WorkerStopTimeout: 5 * time.Second,
		DeploymentOptions: deployment(deploymentName, build),
	})
	registerTask(w, func(context.Context) (*task.Activities, error) { return nil, errRegistrationActivity })
	if err = w.Start(); err != nil {
		return err
	}
	defer func() {
		w.Stop()
		if guard.unexpected.Load() {
			result = errors.Join(result, fmt.Errorf("registration refused unexpected task execution"))
		}
	}()
	for {
		version, err := handle.DescribeVersion(ctx, client.WorkerDeploymentDescribeVersionOptions{BuildID: build})
		if err == nil {
			if !version.Info.CurrentSinceTime.IsZero() || !version.Info.RampingSinceTime.IsZero() {
				return fmt.Errorf("registration version became current or ramping")
			}
			workflowQueue, activityQueue := false, false
			for _, queue := range version.Info.TaskQueuesInfos {
				if queue.Name == taskQueue {
					workflowQueue = workflowQueue || queue.Type == client.TaskQueueTypeWorkflow
					activityQueue = activityQueue || queue.Type == client.TaskQueueTypeActivity
				}
			}
			if workflowQueue && activityQueue {
				return nil
			}
		} else if !errors.As(err, &missing) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

type registrationGuard struct {
	interceptor.WorkerInterceptorBase
	unexpected atomic.Bool
}

func (g *registrationGuard) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &registrationWorkflow{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}, guard: g}
}

func (g *registrationGuard) InterceptActivity(_ context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return &registrationActivity{ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: next}, guard: g}
}

type registrationWorkflow struct {
	interceptor.WorkflowInboundInterceptorBase
	guard *registrationGuard
}

func (r *registrationWorkflow) ExecuteWorkflow(workflow.Context, *interceptor.ExecuteWorkflowInput) (any, error) {
	r.guard.unexpected.Store(true)
	// Fail only this workflow task if routing is unexpectedly violated; returning
	// an error here would permanently fail the user's workflow execution.
	panic("registration cannot execute workflows")
}

type registrationActivity struct {
	interceptor.ActivityInboundInterceptorBase
	guard *registrationGuard
}

var errRegistrationActivity = errors.New("registration cannot execute activities")

func (r *registrationActivity) ExecuteActivity(context.Context, *interceptor.ExecuteActivityInput) (any, error) {
	r.guard.unexpected.Store(true)
	return nil, errRegistrationActivity
}
