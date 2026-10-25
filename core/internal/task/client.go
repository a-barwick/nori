package task

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	cloudrunid "go.temporal.io/sdk/contrib/gcp/cloudrun/id"
	"nori/internal/state"
)

func Queue() string {
	if v := os.Getenv("TEMPORAL_TASK_QUEUE"); v != "" {
		return v
	}
	return "nori-tasks"
}
func Dial(ctx context.Context) (client.Client, error) {
	address, namespace := os.Getenv("TEMPORAL_ADDRESS"), os.Getenv("TEMPORAL_NAMESPACE")
	if address == "" || namespace == "" {
		return nil, errors.New("TEMPORAL_ADDRESS and TEMPORAL_NAMESPACE are required")
	}
	options := client.Options{HostPort: address, Namespace: namespace}
	if os.Getenv("TEMPORAL_LOCAL") != "true" {
		key := strings.TrimSpace(os.Getenv("TEMPORAL_API_KEY"))
		if key == "" {
			return nil, errors.New("TEMPORAL_API_KEY is required for Temporal Cloud")
		}
		options.ConnectionOptions.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
		options.Credentials = client.NewAPIKeyStaticCredentials(key)
	}
	if os.Getenv("TEMPORAL_CLOUD_RUN_ID") == "true" {
		options.Plugins = append(options.Plugins, cloudrunid.NewCloudRunIDPlugin())
	}
	return client.DialContext(ctx, options)
}
func WorkflowID(id string) string { return "nori-task-" + id }

// Deliver is called after acceptance and on worker/API startup. A crash before
// marking the durable intent delivered merely sends a duplicate on repair.
func Deliver(ctx context.Context, store state.TaskStore, c client.Client) error {
	pending, e := store.Pending(ctx)
	if e != nil {
		return e
	}
	var failures []error
	for _, cmd := range pending {
		run, e := store.GetRun(ctx, cmd.RunID)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if run.Status != "running" {
			if e = store.Delivered(ctx, cmd.ID); e != nil {
				failures = append(failures, e)
			}
			continue
		}
		control, e := store.Control(ctx)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if _, e = control.Deadline(); e != nil {
			// Stop can signal an existing workflow after pause; it must not start one.
			if cmd.Kind == "cancel" {
				e = c.SignalWorkflow(ctx, WorkflowID(cmd.RunID), "", Signal, cmd)
			} else {
				continue
			}
		} else {
			_, e = c.SignalWithStartWorkflow(ctx, WorkflowID(cmd.RunID), Signal, cmd, client.StartWorkflowOptions{ID: WorkflowID(cmd.RunID), TaskQueue: Queue(), WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE, WorkflowExecutionTimeout: time.Until(run.Deadline) + state.CleanupGrace}, WorkflowName, cmd.RunID)
		}
		if e != nil {
			failures = append(failures, fmt.Errorf("command %s delivery: %w", cmd.ID, e))
			continue
		}
		if e = store.Delivered(ctx, cmd.ID); e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}
