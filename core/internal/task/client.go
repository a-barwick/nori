package task

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	cloudrunid "go.temporal.io/sdk/contrib/gcp/cloudrun/id"
	"nori/internal/observability"
	"nori/internal/state"
	"nori/internal/workerwake"
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
		key, err := apiKey(ctx)
		if err != nil {
			return nil, err
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

var missingLegacyCommands sync.Map

// Deliver is called after acceptance and on worker/API startup. A crash before
// marking the durable intent delivered merely sends a duplicate on repair.
func Deliver(ctx context.Context, store state.TaskStore, c client.Client, wakes *workerwake.Cloud) (resultErr error) {
	pending, e := store.Pending(ctx)
	if e != nil {
		if !errors.Is(ctx.Err(), context.Canceled) {
			observability.RecordErrorCategory(ctx, "task.delivery", e)
		}
		return e
	}
	if len(pending) == 0 {
		return nil
	}
	ctx, span := taskTracer.Start(ctx, "task.deliver_pending", trace.WithAttributes(attribute.String("openinference.span.kind", "CHAIN"), attribute.String("session.id", observability.SessionID())))
	defer func() {
		if resultErr != nil && !errors.Is(ctx.Err(), context.Canceled) {
			observability.RecordErrorCategory(ctx, "task.delivery", resultErr)
		}
		span.End()
	}()
	span.SetAttributes(attribute.Int("nori.command.count", len(pending)))
	var failures []error
	for _, cmd := range pending {
		run, e := store.GetRun(ctx, cmd.RunID)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if run.Status != state.RunRunning {
			if run.CancelRequested && (cmd.Kind == state.CommandStop || cmd.Kind == state.CommandCancel) {
				// A paused Stop may already be persisted while its workflow waits.
				// Retain delivery intent until it receives a wake or is absent.
				e = c.SignalWorkflow(ctx, WorkflowID(cmd.RunID), "", Signal, cmd)
				var missing *serviceerror.NotFound
				if e != nil && !errors.As(e, &missing) {
					failures = append(failures, e)
					continue
				}
				if e == nil && wakes.Enabled() {
					control, err := store.Control(ctx)
					if err != nil {
						failures = append(failures, err)
						continue
					}
					// A paused Stop may have projected cancellation before its
					// parked workflow drains. Keep this existing command intent
					// until resume can durably enqueue the exact workflow wake.
					if control.Paused {
						continue
					}
					if err = wakeWorker(ctx, c, wakes, cmd.RunID); err != nil {
						failures = append(failures, err)
						continue
					}
				}
			}
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
		if run.ProtocolVersion < state.TaskProtocolVersion {
			// Existing v1 executions keep their pinned release. A legacy intent
			// without an execution cannot safely inherit the current v2 worker.
			e = c.SignalWorkflow(ctx, WorkflowID(cmd.RunID), "", Signal, cmd)
			var missing *serviceerror.NotFound
			if errors.As(e, &missing) {
				// Keep the run and its intent pending for operator restoration of
				// the retained v1 execution, or a targeted Stop. Retrying the
				// entire outer request cannot repair this compatibility gap.
				if _, reported := missingLegacyCommands.LoadOrStore(cmd.ID, true); !reported {
					log.Printf("legacy task %s command %s remains pending: restore its retained v1 execution or stop the task", cmd.RunID, cmd.ID)
				}
				continue
			}
		} else if e = control.Check(); e != nil {
			// Stop can signal an existing workflow after pause; it must not start one.
			if cmd.Kind == state.CommandCancel || cmd.Kind == state.CommandStop {
				e = c.SignalWorkflow(ctx, WorkflowID(cmd.RunID), "", Signal, cmd)
			} else {
				continue
			}
		} else {
			options := client.StartWorkflowOptions{ID: WorkflowID(cmd.RunID), TaskQueue: Queue(), WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}
			if run.WorkflowVersion < state.TaskWorkflowVersion {
				// A retained v2 start intent keeps its original execution lane and
				// argument shape, even when its first delivery follows an upgrade.
				_, e = c.SignalWithStartWorkflow(ctx, WorkflowID(cmd.RunID), Signal, cmd, options, PriorWorkflowName, cmd.RunID)
			} else {
				_, e = c.SignalWithStartWorkflow(ctx, WorkflowID(cmd.RunID), Signal, cmd, options, WorkflowName, cmd.RunID, Checkpoint{})
			}
		}
		if e != nil {
			failures = append(failures, fmt.Errorf("command %s delivery: %w", cmd.ID, e))
			continue
		}
		if control.Paused && wakes.Enabled() {
			continue
		}
		if !control.Paused {
			if e = wakeWorker(ctx, c, wakes, cmd.RunID); e != nil {
				failures = append(failures, e)
				continue
			}
		}
		if e = store.Delivered(ctx, cmd.ID); e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}

// Wake signals existing durable tasks after admission reopens. User-input waits
// reconcile and remain waiting when their revision has not changed. No workflow
// or computer is started by this function.
func Wake(ctx context.Context, store state.TaskStore, c client.Client, wakes *workerwake.Cloud) error {
	control, err := store.Control(ctx)
	if err != nil {
		return err
	}
	if err = control.Check(); err != nil {
		return err
	}
	runs, err := store.OpenTasks(ctx)
	if err != nil {
		return err
	}
	var failures []error
	seen := map[string]bool{}
	for _, run := range runs {
		if !run.Temporal || run.Status != state.RunRunning {
			continue
		}
		if err = c.SignalWorkflow(ctx, WorkflowID(run.ID), "", Signal, state.Command{RunID: run.ID, Kind: "resume"}); err != nil {
			var missing *serviceerror.NotFound
			if !errors.As(err, &missing) {
				failures = append(failures, err)
			}
		} else if err = wakeWorker(ctx, c, wakes, run.ID); err != nil {
			failures = append(failures, err)
		} else {
			seen[WorkflowID(run.ID)] = true
		}
	}
	if wakes.Enabled() {
		failures = append(failures, recoverExecutionWakes(ctx, c, wakes, seen))
	}
	return errors.Join(failures...)
}

// WakePublications delivers one durable wake for each held result. The generation
// fence preserves a newer hold if another user input arrives during delivery.
func WakePublications(ctx context.Context, store state.TaskStore, c client.Client, wakes *workerwake.Cloud) error {
	runs, err := store.PublicationTasks(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, run := range runs {
		if err = c.SignalWorkflow(ctx, WorkflowID(run.ID), "", Signal, state.Command{RunID: run.ID, Kind: "routing-resolved"}); err != nil {
			failures = append(failures, err)
			continue
		}
		if err = wakeWorker(ctx, c, wakes, run.ID); err != nil {
			failures = append(failures, err)
			continue
		}
		if err = store.PublicationWoken(ctx, run.ID, run.PublicationGeneration); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
