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
app state. Hosted Temporal TLS and Secret Manager authentication are unchanged.

The predictable service URL uses Cloud Run's documented
[`SERVICE_NAME-PROJECT_NUMBER.REGION.run.app` format](https://docs.cloud.google.com/run/docs/triggering/https-request#deterministic_url).
The 40-character build limit keeps its DNS segment below 64 characters. The same
root URL is configured as an accepted audience, avoiding a Terraform cycle.

From `core/`, use the explicit cost-control switch:

```sh
python3 infrastructure/app/ops.py pause
python3 infrastructure/app/ops.py unpause
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
termination schedule cleared in place with `ops.py clear-deadline` while stopped;
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
computer's paused/stopped state. Do not use the global app pause helper for an
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

### Retiring old builds

Workers register only the `nori-task-v3` workflow. No workflow was running when
`nori-task-v1` and `nori-task-v2` were removed, so nothing is pinned to a build
from before that change. Those builds are still in the manifest and in
Terraform state; retiring them is a separate operator step that `make deploy`
cannot perform.

`make deploy` builds its Terraform variables from state, not from this
manifest, so editing `worker-releases.json` alone retires nothing. To retire
builds:

1. Read the Current build with `temporal worker deployment describe --name=nori-task`
   (the query `current_worker` in `release/deploy.py` makes; it requires no
   active ramp). That build is the only entry to keep in
   `request_worker_images` and `request_worker_targets`. Empty `worker_images`
   and `scalable_build_ids`.
2. Confirm no running workflow is pinned to a build being dropped.
3. Review the plan from the reduced manifest. It destroys retained pools and
   services, which carry `prevent_destroy` and `deletion_protection`, and it
   shrinks the `NORI_WORKER_SERVICES` route map of the surviving service.
   `check_plan` and `route_only_update` refuse that by design; do not loosen
   them. Apply it by hand, lifting the destroy protections only on the
   resources being retired and restoring them afterwards.
4. Keep the isolated candidate target unless retiring its data is a separate
   decision. It owns the `nori-worker-candidate` database, event queue and IAM
   binding, so removing it from the manifest also plans to destroy those.

After that apply, `make deploy` runs unchanged: it reads the smaller state and
its plan again adds only the new worker.

## Credentials and measurements

The existing Secret Manager container `nori-temporal-api-key` supplies the worker
credential by reference. Its payload never enters Terraform, state, images,
plans or logs. The root `.env` remains the local source. Selected models:
intent `jev-latest`, outer `gpt-6.1-sol` with low reasoning through the
OpenAI platform API, and task worker `openai/gpt-6.1-terra` through OpenRouter Responses.

New release targets leave `jev_completion` disabled, because workers no longer
call Jev to finish a task. Retained targets that enabled it keep `JEV_API_KEY`
until their tasks drain, so adding a release doesn't change their credentials.

New release targets also enable `capabilities` once `nori-brave-search-api-key`
and the two `nori-google-calendar-client-*` secrets each have a version. Without them
the release still deploys, and its tools report themselves unavailable. Add a
version from the root `.env` without printing it:

```bash
awk -F= '$1=="BRAVE_SEARCH_API_KEY"{printf "%s",substr($0,length($1)+2)}' .env | gcloud secrets versions add nori-brave-search-api-key --data-file=-
```

Run `go run ./cmd/nori-connect-calendar --user <id>` from `core/` to save
the calendar refresh token and account in that user's row. An OAuth app in
Google's testing state loses its refresh token after 7 days, and the calendar then
reports itself as disconnected until the command runs again and the row is updated.

Measure acceptance to first activity, active activity completion across polling
cutoff, response after SDK drain, automatic successor delivery, durable input
wait/Answer, and targeted Stop with retained browser ownership. Record actual
model token usage separately from observed compute duration and delayed billing.
Local checks do not establish native OIDC, Cloud Tasks delivery, service billing,
or GCE stop/restart. Do not infer physical container zero from a response.
