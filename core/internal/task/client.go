package task

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	cloudrunid "go.temporal.io/sdk/contrib/gcp/cloudrun/id"
	"nori/internal/dispatch"
	"nori/internal/secret"
	"nori/internal/settings"
	"nori/internal/state"
)

var configured settings.Temporal

// Configure sets the Temporal settings this process uses. Every process that dials Temporal calls it once at startup.
func Configure(s settings.Temporal) { configured = s }

// Queue is the task queue workflows and workers use.
func Queue() string { return configured.TaskQueue }

// Dial connects to Temporal. Outside local mode it needs the Temporal Cloud API
// key, from TEMPORAL_API_KEY or from the secret named by
// TEMPORAL_API_KEY_SECRET_VERSION, which secrets resolves. A deployed API can
// carry a secret reference instead of a plaintext credential in its runtime
// configuration or Terraform state. Workers may continue using their platform's
// native TEMPORAL_API_KEY secret injection. Local mode never calls secrets.
func Dial(ctx context.Context, secrets secret.Lookup) (client.Client, error) {
	if err := configured.Validate(); err != nil {
		return nil, err
	}
	address, namespace := configured.Address, configured.Namespace
	options := client.Options{HostPort: address, Namespace: namespace}
	if !configured.Local {
		key, err := secret.Resolve(ctx, secrets, configured.APIKey, configured.APIKeySecret, "TEMPORAL_API_KEY_SECRET_VERSION")
		if err != nil {
			return nil, err
		}
		if key == "" {
			return nil, errors.New("TEMPORAL_API_KEY or TEMPORAL_API_KEY_SECRET_VERSION is required for Temporal Cloud")
		}
		options.ConnectionOptions.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
		options.Credentials = client.NewAPIKeyStaticCredentials(key)
	}
	if configured.CloudRunID {
		options.Plugins = append(options.Plugins, cloudrunid.NewCloudRunIDPlugin())
	}
	return client.DialContext(ctx, options)
}

func (e *Engine) workflowID(id string) string { return dispatch.WorkflowID(e.UserID, id) }

// Engine runs the dispatch contract on a Temporal client.
type Engine struct {
	c      client.Client
	UserID string
}

var _ dispatch.Orchestrator = (*Engine)(nil)

func NewEngine(c client.Client, userID string) *Engine { return &Engine{c: c, UserID: userID} }

// Close closes the Temporal client the Engine was built on.
func (e *Engine) Close() { e.c.Close() }

func (e *Engine) Signal(ctx context.Context, runID string, cmd state.Command) error {
	return noExecution(e.c.SignalWorkflow(ctx, e.workflowID(runID), "", Signal, cmd))
}

func (e *Engine) SignalOrStart(ctx context.Context, cmd state.Command) error {
	options := client.StartWorkflowOptions{
		ID: e.workflowID(cmd.RunID), TaskQueue: Queue(), WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}
	// An offer's prep task starts below the default budget. Budgets only move up later, so it is set here.
	start := Checkpoint{}
	if cmd.Offer != "" {
		start.StartBudget = state.ProactivePrepBudgetUSD
	}
	_, err := e.c.SignalWithStartWorkflow(ctx, e.workflowID(cmd.RunID), Signal, cmd, options, WorkflowName, cmd.RunID, start)
	return err
}

func (e *Engine) Inspect(ctx context.Context, runID string) (dispatch.Execution, error) {
	d, err := e.c.DescribeWorkflowExecution(ctx, e.workflowID(runID), "")
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
