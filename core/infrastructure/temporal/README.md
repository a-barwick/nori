# Temporal task workers

Nori keeps one pinned Temporal workflow per task. New worker releases run bounded
HTTP slices on authenticated Cloud Run services; Firestore stores task state,
commands and receipts, and Cloud Storage stores artifacts. Temporal Cloud owns
durable waiting and sequencing. Retained worker pools and images remain in place
at zero, with native WCI compute configurations removed.

The request-driven implementation awaits its native candidate demonstration and
rollout approval. Production is paused; retained worker pools remain at zero
without WCI compute configurations.

The production request-worker target uses namespace `quickstart-nori.qtjow`,
deployment `nori-task`, task queue `nori-tasks`, and worker wake queue `nori-worker`
in `us-west1`. Terraform
uses only the existing external state at
`~/.local/state/nori/bot-111549/temporal/terraform.tfstate`.

## Request slices and pause

Each immutable build has a service named `nori-task-BUILD`. Service-level scaling
is min **0**, max **1**, with concurrency **1**, request-based CPU billing, and a
**660s** request timeout. Cloud Tasks sends OIDC-authenticated `POST /run`
requests with a **720s** dispatch deadline. `GET /healthz` does not start polling.
The worker polls for at most **240s**, stops after **15s** idle, and drains the
SDK for up to **300s** before responding. Accepted commands require a queued
wake; runnable work discovered after drain is handed to a successor request.
Waiting for user input leaves the workflow durable without a polling request.

`NORI_WORKER_SERVICES` is the complete immutable build-to-root-URL map for one
Temporal deployment/task-queue/wake-queue group. `NORI_WORKER_WAKE_QUEUE` is that
group's full native queue path. Services use the existing runtime identity and
`NORI_WAKE_SERVICE_ACCOUNT`; only queue-scoped enqueue and service-scoped invoker
grants are added. The caller's existing `actAs` permission remains owned by the
POC state. Hosted Temporal TLS and Secret Manager authentication are unchanged.

The predictable service URL uses Cloud Run's documented
[`SERVICE_NAME-PROJECT_NUMBER.REGION.run.app` format](https://docs.cloud.google.com/run/docs/triggering/https-request#deterministic_url).
The 40-character build limit keeps its DNS segment below 64 characters. The same
root URL is configured as an accepted audience, avoiding a Terraform cycle.

From `core/`, use the explicit cost-control switch:

```sh
python3 infrastructure/poc/poc.py pause
python3 infrastructure/poc/poc.py unpause
```

Pause first fences dispatch on all retained worker wake queues, then closes
application admission, removes retained pool compute configurations, and
reconciles those pools to zero. Paused handlers can retain successor wakes in
the paused queue without starting paid work.
It waits for dispatched worker requests to finish before stopping the computer.
A failed or ambiguous Cloud Tasks delivery requires inspection of matching native
worker `stopped`/`response` logs; pause reports incomplete and withholds computer
stop when drain is uncertain. It preserves queued wakes and waiting tasks.
Request completion proves SDK drain in the normal successful flow; it does not
prove that Cloud Run has physically removed every idle container. Request-based
billing also includes startup and shutdown. Explicit task Stop remains separate.

Unpause requires a production request-service route for Current, any ramping
version, and every open pinned workflow. Unknown pins or overrides fail closed;
there is no implicit migration or cancellation. It keeps candidate queues paused,
opens admission, asks the runtime to deliver pending commands, then resumes only
the production worker queue. It never reinstates WCI compute configurations.
The next computer tool starts the retained VM on demand.

The helper reads `TEMPORAL_API_KEY` from the existing root `.env` without logging
it. `NORI_ENV_FILE` selects that existing file from a worktree. Credentials are
never copied into the worktree or Terraform. The retained VM must have its old
termination schedule cleared in place with `poc.py clear-deadline` while stopped;
unpause verifies that migration remains complete.

## Immutable releases

`make deploy` adds production workers from the Terraform state ([Delivery](../README.md#delivery)). `worker-release.py` below only prepares isolated candidates against local state.

`terraform/worker-releases.json` retains all deployed pool and service image
digests, service routing targets, and billing-alert settings. Pass the complete
manifest to every plan. Never change an image behind a retained build ID or
remove a retained target while pinned workflows or queued wakes need it.
`worker_images` and `scalable_build_ids` preserve old pool resources and IAM;
new releases are added only to `request_worker_images` and
`request_worker_targets`. Neither preparation nor unpause enables old scalers.

Prepare a local image archive and an exact Terraform plan from `core/`:

```sh
python3 infrastructure/temporal/worker-release.py --build-id BUILD_ID
```

For an isolated native candidate, select separate names together:

```sh
python3 infrastructure/temporal/worker-release.py --build-id BUILD_ID \
  --temporal-deployment nori-task-candidate \
  --temporal-task-queue nori-tasks-candidate \
  --wake-queue nori-worker-candidate --poll-window-seconds 2 \
  --firestore-database nori-worker-candidate
```

Candidate targets remain retained when later production releases are added.
All production versions share `nori-worker` and the fixed `nori-poc` database.
Candidates use separate routing and the retained native `nori-worker-candidate`
Firestore database in `us-central1`; the production database is unchanged.
The existing runtime identity receives only a candidate-database-scoped
conditional datastore binding. Existing store queries need only automatic
single-field indexes; no composite indexes or data migration are added.
Terraform validates complete targets and rejects groups sharing only part of a
routing identity. Production polling stays 240s. The operator injects only
`production_worker_services` and `worker_wake_queue` outputs into the production
runtime; `request_worker_services`, `request_worker_targets` and
`worker_wake_queues` expose all retained resources for candidate verification.
New queues start **PAUSED**; applying infrastructure never opens admission.

Candidate worker Notify requests go to `nori-worker-candidate-events`, a separate
queue retained **PAUSED** with a queue-scoped enqueue grant. It exercises native
enqueue without dispatching to the inherited production runtime URL or changing
the production conversation queue. The local candidate API's LocalLoop consumes
the candidate database; native outer delivery is untested in this demonstration.
`candidate_firestore_database` and `candidate_event_queue` outputs identify these
resources for candidate-only cleanup. Fence candidate queues, close only the
candidate database's control, verify request drain, and restore the retained
computer's paused/stopped state. Do not use the global POC pause helper for an
isolated candidate: it deliberately writes production control.

Preparation requires an OCI-capable Docker Buildx builder; select an existing
`docker-container` builder with `--builder NAME` if necessary. It exports one
Linux/amd64 archive without pushing, records its immutable digest and source
fingerprint, preserves the full manifest, and saves `release.json`,
`worker-releases.json`, `release.plan` and `plan.txt` in an owner-only directory
beside the external state (or `--output PATH`). It fails if worker sources change
during preparation. `--var-file PATH` preserves additional reviewed Terraform
settings. `--image URI` accepts an already-built immutable registry digest;
the operator must verify that digest corresponds to the intended source.

Review `plan.txt` before publishing or applying. Publish the exact OCI archive
with digest preservation, then apply the reviewed saved plan only after approval.
Preparation never publishes, applies or promotes. After application, retain the
candidate manifest as the repository's complete release manifest. Retain the
existing `runtime_invoke_root`, conversation queue/caller, Secret Manager
container and existing IAM ownership; no secret payload enters a plan or image.

The service SDK registers its deployment version when a request starts polling.
Register or promote only the reviewed build in its exact deployment, without
adding WCI compute configuration. Candidate registration or demonstration never
promotes that build to production; a production build must name the production
Temporal deployment and queue. Keep old pools, service targets, immutable images,
workflow histories, browser profile, disks and task files through rollout and
rollback. Native verification must confirm queue fences, request drain, all old
pools at zero/no scalers, and the retained computer stopped before finishing.

## Credentials and measurements

The existing Secret Manager container `nori-temporal-api-key` supplies the worker
credential by reference. Its payload never enters Terraform, state, images,
plans or logs. The root `.env` remains the local source. Selected models:
intent `jev-latest`, outer `gpt-6.1-sol` with low reasoning through the
OpenAI platform API, and task worker `openai/gpt-6.1-terra` through OpenRouter Responses.

New release targets enable `jev_completion`, which passes `JEV_API_KEY` from
the existing `nori-jev-api-key` secret to the worker. Retained targets default
to disabled so adding a release does not change their credential configuration.

Measure acceptance to first activity, active activity completion across polling
cutoff, response after SDK drain, automatic successor delivery, durable input
wait/Answer, and targeted Stop with retained browser ownership. Record actual
model token usage separately from observed compute duration and delayed billing.
Local checks do not establish native OIDC, Cloud Tasks delivery, service billing,
or GCE stop/restart. Do not infer physical container zero from a response.

## Optional daily spend alert

`spend-alert.py --check` generates or reads the current UTC billing month's
Temporal account report, downloads the provider CSV, and reports whether the
configured threshold has been reached. The default threshold is $10 USD. It sums
all reported `ContractedCost` charge records across this account's namespaces
and charge categories; it does not include GCP costs or claim to be a finalized
invoice. [Temporal's billing schema](https://docs.temporal.io/cloud/billing-api)
labels USD values as cents, so the configurable default divides by 100. Current
month data is provisional and lags at least 24 hours, rounded to daily granularity.

Read the existing key in place for a local check (no metrics or email):

```sh
python3 core/infrastructure/temporal/spend-alert.py --check \
  --env-file /absolute/path/to/the/existing/root/.env
# Reuse a requested report instead of generating another overlapping report:
python3 core/infrastructure/temporal/spend-alert.py --check \
  --env-file /absolute/path/to/the/existing/root/.env --report-id REPORT_ID
```

The account-level `TEMPORAL_BILLING_API_KEY` requires billing-report access. The
namespace execution key is a different credential. No key or signed download URL
is logged or persisted. JSON output includes the report ID, generation/check
times, charge coverage when available, unit basis, total, and threshold. Empty
reports, unsupported currency, unparseable costs, and provider failures produce
unknown spend instead of $0; exit 2 means a generated report had no interpretable
charge total, while exit 1 means a request or processing failed. Only a complete
report returns exit 0. A nonempty report also checks cost against unit price and
quantity before conversion. The actual account report inspected during local
validation had headers and no charge rows; nonzero cents-to-USD conversion
therefore remains unverified against this account's real values.

`terraform/spend-alert.tf` creates nothing unless `spend_alert` is configured with
a reviewed immutable image digest and an **existing** Secret Manager billing-key
reference. It never creates or uploads a key payload. `Dockerfile.alert` builds
from `core/`; its default command is read-only `--check`. The deployed Job alone
passes `--publish`, using its own service account to write native Cloud Monitoring
metrics. Publication requires a newly generated report; `--report-id` remains
read-only inspection so an old report cannot become a fresh successful check. It has one task, parallelism one, no retries, a six-minute timeout, and
no resident capacity. Scheduler runs it once daily at 17:00 UTC by default; the
cron schedule, USD threshold, cost-unit override, and secret version are configurable.
It has no permission to wake task workers or the computer.

Native Monitoring emails Austin's configured GCP account address when the daily
report reaches the threshold or the report fails/is unknown. The threshold and failure conditions retain the preceding daily
point for up to 24 hours; a month reset or repaired check can therefore take a
day to clear. A Job that never runs or cannot publish any metric needs inspection in
Job/Scheduler diagnostics; deployment acceptance must verify the schedule, initial
metric, and email channel. No notification or deployment was performed during local validation.
This monitor adds normal Cloud Run Job, Scheduler, and Monitoring usage. It is an
alert, not a charge cap, and does not pause execution automatically.
