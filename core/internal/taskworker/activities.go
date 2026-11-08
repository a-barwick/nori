package taskworker

import (
	"context"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"nori/internal/task"
)

type ActivityScope func(context.Context) (*task.Activities, error)

func registerTask(w worker.Worker, scope ActivityScope) {
	w.RegisterWorkflowWithOptions(task.Workflow, workflow.RegisterOptions{Name: task.WorkflowName, VersioningBehavior: workflow.VersioningBehaviorPinned})

	// Temporal history stores these names and input and result types. Each
	// handler resolves the workflow's user before it can touch their data.
	registerResult(w, scope, "AppendToolResult", (*task.Activities).AppendToolResult)
	registerAction(w, scope, "Ask", (*task.Activities).Ask)
	registerResult(w, scope, "CheckCompletion", (*task.Activities).CheckCompletion)
	registerAction(w, scope, "CloseBrowser", (*task.Activities).CloseBrowser)
	registerResult(w, scope, "Context", (*task.Activities).Context)
	registerResult(w, scope, "CurrentPosition", (*task.Activities).CurrentPosition)
	registerResult(w, scope, "InputRef", (*task.Activities).InputRef)
	registerResult(w, scope, "JobWaitRef", (*task.Activities).JobWaitRef)
	registerResult(w, scope, "Model", (*task.Activities).Model)
	registerResult(w, scope, "ModelRef", (*task.Activities).ModelRef)
	registerAction(w, scope, "Project", (*task.Activities).Project)
	registerResult(w, scope, "Publish", (*task.Activities).Publish)
	registerResult(w, scope, "PublishRef", (*task.Activities).PublishRef)
	registerResult(w, scope, "Reconcile", (*task.Activities).Reconcile)
	registerAction(w, scope, "Spend", (*task.Activities).Spend)
	registerAction(w, scope, "Stop", (*task.Activities).Stop)
	registerAction(w, scope, "StopJobs", (*task.Activities).StopJobs)
	registerResult(w, scope, "SyncHistory", (*task.Activities).SyncHistory)
	registerResult(w, scope, "ToolRef", (*task.Activities).ToolRef)
}

func registerResult[Input, Result any](w worker.ActivityRegistry, scope ActivityScope, name string, run func(*task.Activities, context.Context, Input) (Result, error)) {
	w.RegisterActivityWithOptions(func(ctx context.Context, in Input) (Result, error) {
		a, err := scope(ctx)
		if err != nil {
			var zero Result
			return zero, err
		}
		return run(a, ctx, in)
	}, activity.RegisterOptions{Name: name})
}

func registerAction[Input any](w worker.ActivityRegistry, scope ActivityScope, name string, run func(*task.Activities, context.Context, Input) error) {
	w.RegisterActivityWithOptions(func(ctx context.Context, in Input) error {
		a, err := scope(ctx)
		if err != nil {
			return err
		}
		return run(a, ctx, in)
	}, activity.RegisterOptions{Name: name})
}
