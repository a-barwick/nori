package main

// Replay checks historical workflow compatibility (A40): every Temporal
// history retained under contracts/assistant/histories must replay against
// the task workflow compiled into this checker. A history captured from an
// older build that no longer replays means a change altered the command
// sequence for existing executions and needs a version gate.

import (
	"fmt"
	"path/filepath"
	"sort"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"nori/internal/task"
)

func replayHistories(dir string) (int, int) {
	files, _ := filepath.Glob(filepath.Join(dir, "histories", "*.json"))
	sort.Strings(files)
	failures := 0
	for _, f := range files {
		r := worker.NewWorkflowReplayer()
		r.RegisterWorkflowWithOptions(task.Workflow, workflow.RegisterOptions{Name: task.WorkflowName})
		if err := r.ReplayWorkflowHistoryFromJSONFile(nil, f); err != nil {
			failures++
			fmt.Printf("FAIL replay %s: %v\n", filepath.Base(f), err)
		}
	}
	return len(files), failures
}
