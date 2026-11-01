package task

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	cloudrunid "go.temporal.io/sdk/contrib/gcp/cloudrun/id"
	"nori/internal/dispatch"
	"nori/internal/secret"
	"nori/internal/state"
)

func Queue() string {
	if v := os.Getenv("TEMPORAL_TASK_QUEUE"); v != "" {
		return v
	}
	return "nori-tasks"
}

// Dial connects to Temporal. Outside local mode it needs the Temporal Cloud API
// key, from TEMPORAL_API_KEY or from the secret named by
// TEMPORAL_API_KEY_SECRET_VERSION, which secrets resolves. A deployed API can
// carry a secret reference instead of a plaintext credential in its runtime
// configuration or Terraform state. Workers may continue using their platform's
// native TEMPORAL_API_KEY secret injection. Local mode never calls secrets.
func Dial(ctx context.Context, secrets secret.Lookup) (client.Client, error) {
	address, namespace := os.Getenv("TEMPORAL_ADDRESS"), os.Getenv("TEMPORAL_NAMESPACE")
	if address == "" || namespace == "" {
		return nil, errors.New("TEMPORAL_ADDRESS and TEMPORAL_NAMESPACE are required")
	}
	options := client.Options{HostPort: address, Namespace: namespace}
	if os.Getenv("TEMPORAL_LOCAL") != "true" {
		key, err := secret.FromEnv(ctx, secrets, "TEMPORAL_API_KEY", "TEMPORAL_API_KEY_SECRET_VERSION")
		if err != nil {
			return nil, err
		}
		if key == "" {
			return nil, errors.New("TEMPORAL_API_KEY or TEMPORAL_API_KEY_SECRET_VERSION is required for Temporal Cloud")
		}
		options.ConnectionOptions.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
		options.Credentials = client.NewAPIKeyStaticCredentials(key)
	}
	if os.Getenv("TEMPORAL_CLOUD_RUN_ID") == "true" {
		options.Plugins = append(options.Plugins, cloudrunid.NewCloudRunIDPlugin())
	}
	return client.DialContext(ctx, options)
}

func WorkflowID(id string) string { return dispatch.WorkflowID(id) }

// Engine runs the dispatch contract on a Temporal client.
type Engine struct{ c client.Client }

var _ dispatch.Orchestrator = (*Engine)(nil)

func NewEngine(c client.Client) *Engine { return &Engine{c: c} }

// Close closes the Temporal client the Engine was built on.
func (e *Engine) Close() { e.c.Close() }

func (e *Engine) Signal(ctx context.Context, runID string, cmd state.Command) error {
	return noExecution(e.c.SignalWorkflow(ctx, WorkflowID(runID), "", Signal, cmd))
}

func (e *Engine) SignalOrStart(ctx context.Context, cmd state.Command) error {
	options := client.StartWorkflowOptions{
		ID: WorkflowID(cmd.RunID), TaskQueue: Queue(), WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}
	_, err := e.c.SignalWithStartWorkflow(ctx, WorkflowID(cmd.RunID), Signal, cmd, options, WorkflowName, cmd.RunID, Checkpoint{})
	return err
}

func (e *Engine) Inspect(ctx context.Context, runID string) (dispatch.Execution, error) {
	d, err := e.c.DescribeWorkflowExecution(ctx, WorkflowID(runID), "")
	if err = noExecution(err); err != nil {
		return dispatch.Execution{}, err
	}
	return dispatch.Execution{
		Closed: d.GetWorkflowExecutionInfo().GetStatus() != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
		NeedsWorker: d.GetPendingWorkflowTask() != nil || len(d.GetPendingActivities()) > 0 ||
			len(d.GetPendingNexusOperations()) > 0,
	}, nil
}

// noExecution maps Temporal's not-found error onto the contract's, keeping the
// original error reachable.
func noExecution(err error) error {
	var missing *serviceerror.NotFound
	if errors.As(err, &missing) {
		return fmt.Errorf("%w: %w", dispatch.ErrNoExecution, err)
	}
	return err
}
