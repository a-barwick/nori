# Nori core

The Go API owns durable conversation and task admission in the configured database. Both
`cmd/nori-local` and `cmd/nori` use the same conversation runner, OpenRouter
model, and advisory Jev hints. Temporal owns inner task execution; a separate Go
worker calls OpenRouter Responses and the retained Firecracker computer. Screenshots
live in a private GCS bucket. The browser uses a same-origin Next.js proxy and
verified sign-in; hosted runtime IAM admits the web and queue caller
service accounts.

Storage sits behind contracts. `internal/state` holds the records, the
`ConversationStore` interface and the write rules. `internal/state/records` applies
them through Firestore or Postgres transactions; `internal/state/gcs` or `internal/state/localfs`
stores screenshot bytes; AWS selects `internal/state/s3`. `internal/usage` calculates reported model costs. Only `internal/platform` names a
backend: `platform.OpenStore(ctx, settings, user)` returns an immutable user-scoped `state.ConversationStore`. Every data operation keeps that scope, including detached cleanup and spend recording. The operator pause remains global.

## Local development

Requires Go 1.26, the web dependencies, and the configured provider credentials.
Run `make dev` from the repository root for loopback web and Go servers. The
local Go entrypoint recovers pending conversation work at startup and processes
it on its application lifecycle, independently of browser requests. `make deps`
starts isolated Postgres and Temporal; see [local setup](infrastructure/local/README.md).
Firestore stays the default unless `NORI_STORE_BACKEND=postgres` is set.

Commands load the monorepo-root `.env` without overriding existing environment
variables. Keep it gitignored and mode 600. `NORI_ENV_FILE` selects an existing
file; `NORI_TASK_ENV_FILE` can select distinct existing task configuration.
Credentials are used in place. Required names are in
[`.env.example`](../.env.example).

## Users

The `users` collection holds the sign-in email, display name, time zone
and computer for each user. Sign-in verifies Google IAP by default,
or the configured OIDC issuer on AWS,
then the web server resolves that email through `/api/users/resolve` using its own
credentials. It prefixes upstream routes with `/users/<id>/`; browser-supplied IDs
never select a user. Loopback development uses `NORI_LOCAL_EMAIL` (or the existing
`OWNER_EMAIL` as a local fallback) and requires a matching row.

From `core/`, provision a user in the configured database:

```sh
go run ./cmd/nori-user --email user@example.com --name 'User' --timezone America/Los_Angeles
# Optional computer flags: --computer-instance, --computer-zone, --local-computer-url.
```

The `owner` row maps to the existing owner document path, unprefixed files and
`nori-task-<task>` executions. Nothing is moved or rewritten. Other users get separate
document roots, `users/<id>/` file prefixes and `nori-task-<user>:<task>` executions.
IDs can repeat across users without exposing records. Old conversation wakes resolve
only to `owner`; new wakes include the user. Worker reconciliation
enumerates rows; each operation uses one immutable scope. API processing
passes and task activities read the current row, so changes apply on the next pass or
activity without mutating work already in flight.

## Task ownership

A top-level task belongs to its user and has an independent lifetime. A subtask
belongs to its `parent_id`; it cannot create another level of subtasks. A shell
job belongs to the task that started it, across questions and instruction
revisions. Job responses include `task_id` and `lifetime: "task"`. Work that must
outlive another task must be dispatched as a separate top-level task; jobs and
subtasks cannot detach themselves.

Completion, failure, and Stop first save a finishing intent. While a task is
finishing, execution and new subtasks are fenced; Stop and follow-up holds remain
available. The stored answer, evidence, and cost survive a worker crash, but the
terminal event and answer are published only after owned children have ended
and computer operations, jobs, and browser cleanup have been confirmed. Trying
to finish with running subtasks stops them and produces an incomplete outcome.
Child cost is rolled up when recorded, including during finishing.

Worker cleanup, closed-workflow recovery, and paused Stop use the same cleanup
path. Cleanup errors keep the task open with `cleanup_error`; recovery retries
cleanup without repeating model or tool execution. Successful cleanup can finalize
the saved result even after the original Temporal execution has closed. The
durable task record owns this finalization; a closed Temporal execution alone
does not mean the task has finished. Terminal telemetry follows the committed
outcome as well.

The guest saves a task-wide job fence before final cleanup and retains failed
process-group stops across retries and restarts. A provisional stop before
publication does not close that lifetime, since a concurrent correction may
still win. Deploy the updated computer agent before the updated worker: final
job cleanup requires its explicit `closed` acknowledgement. An older guest's
acknowledgement leaves cleanup pending. A Temporal version marker preserves the
cleanup activity order when replaying older histories. Runs pinned to older
worker binaries retain that build's behavior until they drain.

This adopts Pi Durable's durable ownership intent, held completion, and recovery
from committed state, reviewed in its
[scheduler at `6e2222a`](https://github.com/earendil-works/pi/blob/6e2222a8dda06e3d69fad46d630f152f5873567a/packages/durable/src/harness/scheduler.ts#L1022)
and [task graph](https://github.com/earendil-works/pi/blob/6e2222a8dda06e3d69fad46d630f152f5873567a/packages/durable/src/harness/task-graph.ts).
Nori keeps its bounded parent/child relationship, existing database transactions,
and Temporal execution rather than adding a second scheduler or a general task graph.

## Execution coordination

Committed records determine what work needs to advance. Queue messages and
Temporal signals only notify the process that can advance it.

| Authority | Responsibility |
| --- | --- |
| Conversation inbox and leased turn | Order user input and task events; fence stale replies and commands. |
| Task commands and run revision | Accept instructions atomically; retain delivery intent until the engine signal and required worker wake succeed. |
| Temporal history | Own runnable task execution, activity retries, durable waits, and the pinned worker build. |
| Publication transaction | Save an outcome and its conversation event together, after routing and owned-work conditions permit it. |
| Shared dispatcher | Repair closed executions or paused Stops, relay commands, release held publications, and repair API wakes. |

The API reconciles before and after assistant processing. Workers use the same
dispatcher during startup and polling. Task acceptance retains a short delivery
attempt for responsiveness; it does not own separate recovery rules. The command
outbox is scanned in bounded pages through a stable cursor, including past failed
deliveries. Each page sends one notification per task. The workflow reads saved
command positions, so transport ordering and duplicate signals cannot reorder
instructions. Acknowledgement follows successful delivery; a crash may repeat a
notification but cannot erase the accepted command.

`WorkVersion` derives wake demand from existing records: unleased assistant or
chat turns, pending commands, eligible publications, terminal cleanup, and paused
Stops. It does not depend on the client's current conversation or recent history.
Snapshots, stream reconnects/heartbeats, and worker polling use one wake-repair
policy. Only a confirmed wake is throttled, and newly discovered work bypasses
that throttle. Chat-only installations inspect only chats.

Activities commit outcomes without queue I/O. The worker coordinator confirms
the conversation wake during polling and again after activities drain. A failed
reconciliation or final wake keeps the incoming queue request retryable; it cannot
turn an already saved success or question into a task failure. The API also checks
for work arriving during its processing pass before acknowledging its wake.

This applies Pi Durable's [stored commit publication](https://github.com/earendil-works/pi/blob/6e2222a8dda06e3d69fad46d630f152f5873567a/packages/durable/src/session/session.ts#L1297),
[ordered inbox boundaries](https://github.com/earendil-works/pi/blob/6e2222a8dda06e3d69fad46d630f152f5873567a/packages/durable/src/harness/inbox.ts#L63),
and [wake demand including reconciliation](https://github.com/earendil-works/pi/blob/6e2222a8dda06e3d69fad46d630f152f5873567a/packages/durable/src/harness/scheduler.ts#L520).
Nori retains its database/Temporal boundary: consolidating their histories into
Pi's single-session scheduler would require replacing distributed leases, worker
versioning, and hosted execution. No additional journal or scheduler is needed
for this change. If a process dies between acceptance and enqueue, the saved work
is repaired by a retry, reconnect, local processing loop, or active worker; there
is no new always-on sweep while all compute is idle. Older pinned workers retain
their existing notification behavior until they drain.

## Managed model context

Saved history is authoritative. Model requests use a bounded view of it, maintained
at execution boundaries without deleting or rewriting messages, task history,
provider items, or source inputs.

* **Assistant:** Before a reply, oldest-first compaction catches up with the recent
  window without calling a model. It commits a partial index of user and assistant
  source excerpts separately from the semantic summary, so indexing cannot evict
  an existing constraint. Idle refinement reads the original turns using its own
  source watermark, reserved before provider I/O. A canceled or crashed refinement
  leaves the index usable and is not automatically replayed. Replacement compares
  the original summary and context cutoff, so stale maintenance cannot overwrite newer context.
* **Chats:** Under the active turn lease, an extractive index advances through every
  omitted message, including history older than the snapshot page. It retains the
  opening request and recent excerpts. The current user message stays whole, recent
  user/reply pairs stay together, and `history_read` can page the original messages
  or read a specific message's full JSON (including attachments and operations) in
  chunks. Older read results give way to their arguments when necessary.
* **Tasks:** Large tool outputs first become operation references. If the view is
  still too large, complete old call/result rounds give way to an evidence index,
  while user instructions, complete user answers with their questions, loaded
  capabilities, and a recent suffix remain. Delegated results retain source-linked
  excerpts. Boundaries are chosen using the size of the entire retained view.
  `task_history_read` reads the immutable source, `operation_read`
  reads full saved tool results, and `conversation_read` resolves summary input IDs
  and visible user/assistant message IDs under the conversation cutoff.
  Derived views are reproducible from the task's revision and immutable history
  reference; they need no separate scheduler or paid summarization activity.

Application input budgets are conservative byte limits: 96 KiB for chats and
128 KiB for tasks, with a 192 KiB final encoded-request cap including schemas and
instructions. The existing assistant budget is 40 KiB. These are application
budgets, not advertised model window sizes. Output tokens retain their separate
caps. An explicit provider context rejection permits at most one smaller request;
ordinary provider errors and uncertain interrupted calls do not authorize retries.
Existing leases and model receipts fence that recovery. A refusal without provider
cost evidence remains unknown-cost; the runtime does not invent a zero charge.
A request whose required instructions still do not fit ends with an explicit
failure, retaining its sources.

Extractive indexes are partial views, not semantic summaries or permission. A model
must retrieve missing source details before relying on them. Existing task archive
limits still apply. No model, provider, production service, or deployment setting
changes with this implementation.

The reference is Pi Durable at [`6e2222a`](https://github.com/earendil-works/pi/tree/6e2222a8dda06e3d69fad46d630f152f5873567a/packages/durable):
[`context.ts`](https://github.com/earendil-works/pi/blob/6e2222a8dda06e3d69fad46d630f152f5873567a/packages/durable/src/harness/context.ts)
separates immutable history from derived views, and
[`compaction.ts`](https://github.com/earendil-works/pi/blob/6e2222a8dda06e3d69fad46d630f152f5873567a/packages/durable/src/harness/compaction.ts)
uses safe message boundaries and transactional summary placement. Nori adopts
those properties with its existing database leases and Temporal receipts. It uses
bounded extractive maintenance where no additional model call is needed; Pi's
single-session scheduler and token estimation are not introduced.

## Observability

Sentry and Arize export are optional and fail open. Set `SENTRY_DSN` for
operational errors, sampled request traces, logs, and metrics; set both
`ARIZE_SPACE_ID` and `ARIZE_API_KEY` for OpenInference AI spans. The shared
OpenTelemetry provider uses the same trace ID across HTTP, Cloud Tasks, and
durable work. Successful health checks and polling are excluded; failures are captured. `SENTRY_RELEASE` and
`SENTRY_ENVIRONMENT` identify the deployed version and environment.

Set `ARIZE_PROJECT_URL` to the project's canonical HTTPS page on
`app.arize.com` to add `nori.arize.url` to Sentry model spans. Each link selects
that span using the shared trace and span IDs. A failed export may mean the span is unavailable there; local diagnostics identify the destination.

Sentry receives operational attributes only, without request bodies, headers,
cookies, or AI prompt/reply content. Arize receives metadata by default;
`NORI_ARIZE_CAPTURE_CONTENT=true` explicitly enables best-effort redacted,
bounded latest-user excerpts and model replies. These excerpts are not the complete prompt; release IDs, message counts, truncation flags, and byte counts describe the omitted context. Redaction cannot guarantee removal of every
piece of personal information. Tool arguments, screenshots, and URLs are not
intended for export. The computer host and guest receive only a public Sentry DSN and release/environment metadata, never Arize or model credentials.

`SENTRY_TRACES_SAMPLE_RATE` defaults to 0.05. Both destinations export the same
sampled traces without independently dropping individual spans. Go and web
errors/logs/metrics have per-process burst limits; these are not account quotas.
Metrics carry bounded model/route/status dimensions and an explicit sampled
flag. They are diagnostic distributions, not exact usage accounting.
Sentry's Developer plan has no payment method configured; its account quota is
the spending boundary. Keep Arize on its free plan and OpenRouter Broadcast
disabled: only Nori's redacted exporter should send model excerpts.
Export queues and flush deadlines remain bounded. Destination-specific local
diagnostics report export failures and rate-limit drops without recursively
sending them through the failed exporter. Browser latency requires a completed
attempt; failures and timeouts are separate outcomes.

Sandbox deployments use their existing sandbox provider secrets, separate from
production credentials. New background tasks record `NORI_TASK_BUDGET_USD`
(default $1) at acceptance and finish incomplete at that ceiling, without a
budget question. Unknown costs are reported without failing successful work.

`internal/settings` reads every setting once at startup; the root `.env.example` describes each one.
The task worker validates its Temporal connection settings, release identity,
and polling windows before starting its listener. User time zones and tool
connections are read from rows when processing work. Request workers also require a deployment name and a wake route for
their build. This validates configuration without contacting Temporal, the
computer or a model; `/healthz` does not verify those services are reachable.

| Configuration | Purpose |
| --- | --- |
| `OPENAI_API_KEY`, `JEV_API_KEY` | Required conversation provider credentials for both entrypoints |
| `NORI_AGENT_MODEL` | Conversation model; default `gpt-6.1-sol`. It runs through OpenAI's platform API at a fixed endpoint, with low reasoning and the standard tier |
| `BIND_HOST`, `PORT` | Hosted entrypoint defaults `127.0.0.1:8080`; managed container binds `0.0.0.0` behind IAM. Local entrypoint always binds loopback |
| `NORI_STORE_BACKEND`, `NORI_POSTGRES_URL` | Firestore by default; `postgres` selects a Postgres connection and applies numbered migrations at startup |
| `NORI_PROJECT_ID`, `FIRESTORE_DATABASE_ID` | Required for Firestore, with no defaults; a process without them fails at startup. Google APIs use Application Default Credentials |
| `FIRESTORE_EMULATOR_HOST`, `TEMPORAL_LOCAL` | Explicit local Firestore endpoint and local Temporal mode; absent/false uses configured services |
| `NORI_WAKE_QUEUE`, `NORI_WAKE_SERVICE_ACCOUNT`, `NORI_RUNTIME_URL` | Hosted entrypoint's Cloud Tasks queue, OAuth caller, and the API base URL that wakes POST to at `/users/<id>/api/process`. Production passes the native Agent Runtime invoke root, which ends in `/api`. Any `https` host is accepted, or loopback `http` when `TEMPORAL_LOCAL=true` |
| `NORI_WORKER_SERVICES`, `NORI_WORKER_WAKE_QUEUE`, `CLOUD_TASKS_EMULATOR_HOST` | Optional request-driven workers: JSON map of immutable build to service root URL, the Cloud Tasks queue that wakes them (OIDC, same caller), and a loopback Cloud Tasks emulator for local runs. Without `NORI_WORKER_SERVICES`, workers poll on their own |
| `OPENROUTER_API_KEY` | Inner worker credential from the project `.env` |
| `MODEL_NAME` | Inner worker model; default `openai/gpt-6.1-terra` through OpenRouter Responses |
| `NORI_CLOUD` | `gcp` (default), `aws`, or `local`; see [AWS adapters](infrastructure/aws/README.md) |
| `ARTIFACT_BUCKET` | Private Cloud Storage or S3 bucket, selected by cloud |
| `NORI_STATE_EMULATOR_HOST` | App-data Firestore emulator on a loopback address; refused when `FIRESTORE_EMULATOR_HOST` is also set. |
| `NORI_LOCAL_ARTIFACT_DIR` | Absolute private folder for screenshots instead of the bucket. Needs a loopback database and `TEMPORAL_LOCAL=true` |
| User row `computer` | Existing retained computer, or a loopback development URL |
| `TEMPORAL_ADDRESS`, `TEMPORAL_NAMESPACE`, `TEMPORAL_TASK_QUEUE` | Shared API/worker connection and task queue |
| `TEMPORAL_API_KEY` | TLS connection key loaded from the selected env file |
| `TEMPORAL_API_KEY_SECRET_VERSION` | Secret reference: GCP Secret Manager version or AWS Secrets Manager name/ARN |
| `TEMPORAL_DEPLOYMENT_NAME`, `TEMPORAL_BUILD_ID` | Worker deployment and immutable release version |

From `core/`, run the local API and inner worker independently:

```sh
go run ./cmd/nori-local
go run ./cmd/nori-worker
# Repair pending task delivery and terminal projections explicitly:
go run ./cmd/nori-deliver
```

`make run` starts `cmd/nori`, which requires the hosted queue configuration;
`make build` builds `bin/nori`. Hosted processing runs only within
`POST /api/process` requests. Cloud Tasks sends OAuth-authenticated notifications
through the native Runtime invoke URL, retries failures, and limits concurrent
dispatch to one. Firestore leases and input revisions govern processing order.
An interrupted enqueue leaves accepted input saved; retrying the same input or
reconnecting to the session repairs its wake. Active processing suppresses
redundant wake creation during session polling.

The API image is `infrastructure/Dockerfile`, built from `core/`. The worker image
is `infrastructure/temporal/Dockerfile.worker`; the web image is `../web/Dockerfile`,
built from `web/`. Deployment, worker registration, pause/resume, and retained-data operations are covered by
[app operations](infrastructure/app/README.md) and
[Temporal operations](infrastructure/temporal/README.md).

## Code layout

Processes are assembled in `internal/platform` and `internal/app`. Everything
that touches infrastructure sits behind a small contract with one adapter, so a
different queue, workflow engine or secret store means writing one adapter.

| Package | Role |
| --- | --- |
| `internal/api` | HTTP routes; no infrastructure imports |
| `internal/settings` | The one reader of environment variables; each process passes on only what a package needs |
| `internal/prompts` | Embedded model instructions, tool descriptions and authored runtime text, including classifier criteria, capability catalogs and tool-result notes; dynamic facts stay render inputs |
| `internal/wake` | `Waker` contract for waking the API and workers; `wake/cloudtasks` is the Cloud Tasks adapter |
| `internal/dispatch` | `Orchestrator` contract for the task engine, and the `Dispatcher` that runs the one reconciliation pass shared by the API, worker and `nori-deliver` |
| `internal/task` | Temporal workflow and activities, plus the Temporal adapter of `Orchestrator` |
| `internal/secret` | Credential lookup contract; `secret/secretmanager` is the Secret Manager adapter |
| `internal/workerservice`, `internal/taskworker` | Request-bound worker slice lifecycle, and the Temporal worker it runs |

`conversation`, `api` and `dispatch` do not import Temporal.

## API protocol

| Route | Behavior |
| --- | --- |
| `GET /healthz` | Process health only |
| `POST /api/healthz` or `/healthz` | Safe configuration health; no database reads |
| `POST /api/session` | `{before?: message-ID}`; saved conversation/task snapshot and older-message cursor; the initial latest-page load queues one real introduction if history is empty |
| `POST /api/status` | `{}`; returns `{version, busy}`. `version` is an opaque digest of the conversation counters, active turn, pause flag and open tasks, so it changes whenever the session would; `busy` is true while chat input is waiting or processing, or an open task can progress. Poll it and fetch the session when `version` changes. It also repairs a missed conversation wake, like the session route |
| `POST /api/turn` | `{id: UUID,message}`; saves input and confirms wake scheduling before HTTP 202, without waiting for a model answer |
| `POST /api/process` | Internal queue handler; drains conversation and repairs task delivery, returning retryable HTTP 503 while work cannot finish |
| `POST /api/trace` | `{run_id: task-UUID}`; recorded task steps loaded on demand |
| `POST /api/input` | `{id: new-UUID,run_id: task-UUID,message}`; ordered correction or answer |
| `POST /api/cancel` | `{id: task-UUID}`; records Stop and its delivery intent |
| `POST /api/resume` | `{}`; repairs delivery and wakes retained workflows after operator admission is resumed |
| `POST /api/artifact` | `{run_id,artifact_id}`; private PNG bytes |

`/api/run` has been retired. The browser proxy admits conversation and explicit
task controls, but does not expose `/api/process` or operator resume. Queue
failure after input or task-command acceptance returns HTTP 503 with
`accepted: true`, `retryable: true`, and the saved session or run; retry the same
request identity. Unknown JSON fields, trailing objects, and invalid IDs are
rejected.

Session snapshots include `messages`, `runs`, `dispatches`, `usage`, conversation
pending/processing state, and recorded outer attempt traces. New input can
supersede an unfinished turn up to three times; the fourth attempt fixes its
batch and later input stays queued. Native tool-call/result history survives
restart. Context is bounded to recent history and traces report truncation;
older transcript pages remain available separately.

The assistant checkpoints its draft before independent command planning and
checkpoints the final plan before command admission. Recovery reuses those
checkpoints and any accepted command receipts. A paid request interrupted before
its result was saved ends the attempt. A follow-up can pick up the saved input.
An interrupted advisory classification falls back to an unavailable hint.
An unfinished attempt from before checkpoints were introduced settles as
interrupted, leaving its input available to a follow-up.

Independent chat saves each attachment read and its outcome before generating
the next reply. An interrupted read can resume under the same operation ID for
up to two attempts. An interrupted model request retains the partial reply and
settles as failed so the user can send a follow-up.

A Firestore transaction admits a whole command batch atomically, including
commands for independent concurrent tasks. Repeating an identical command UUID
returns its recorded acceptance; reusing that ID with different content is
rejected. Every accepted command advances its task's command position; Start,
Steer and Stop also advance the instruction revision. Answer and Continue must
identify the current unresolved input or budget question, respectively. Effects
and final publication require the current revision and incorporated command
position; final publication also waits for accepted chat input to be routed.
Durable `system/control.paused` gates work; old `expires_at` fields are ignored.
Missing or paused control rejects new task execution.

A durable task can wait for input or project resume without a
wall-clock task deadline. It survives normal worker shutdown and project pause.
Model requests and tool execution keep per-call limits. Interrupted or uncertain
tool operations can fail the task rather than risk replaying a side effect. Stop requires an
actual guest stopped acknowledgement or an observed terminated VM; uncertainty
must not be reported as successful cancellation.

The inner task starts with a $1 model budget. Reported token usage is charged at
the configured model prices, including billed unusable responses; cached prompt
tokens use the full input price. Budget exhaustion persists a question without
another model call. Only exactly `continue` while waiting at that budget grants
another $1. This is a task command, separate from Temporal Continue-As-New.
Outer conversation and Jev calls have separate provider usage records and do not
consume the inner task's budget. An unknown provider price remains unknown.

Temporal allows up to three delivery attempts for a model or tool activity.
On retry, the worker waits for the original activity's receipt until its saved
deadline. Model outcomes commit with their transcript entries, and an expired
model attempt without a result settles as interrupted. One settlement wins for
each activity, even if an earlier worker returns at the same time.
Each admitted computer operation gets a stable ID, request digest, and receipt
under the task directory's `.nori-receipts`. Recovery uses the saved result or
checks the guest receipt for up to 15 seconds. When it cannot establish a
write's outcome, it keeps the uncertainty visible and can wait for the user to answer. Cancellation scopes
include task revisions, while working directories remain
`/workspace/tasks/<task-UUID>` across corrections.

Operator cloud capacity is separate from durable task state. Pause/resume can
stop/start workers and the computer without deleting saved work. Leave production
open after live checks. The services scale on demand and the computer stops on
idle. Follow the linked operations guides for the lifecycle and its limits.

## Original retained-disk microVM reference

The current retained computer uses the [app lifecycle](infrastructure/app/README.md).
Do not apply this older lifecycle against that VM.

The original `infrastructure/microvm/computer.sh` manages one bounded host and a separately
retained `bot-computer-data` disk. The 20 GiB standard disk costs about $0.80/month
while retained, including when compute is off. Its sparse 16 GiB writable Debian
rootfs contains installed tools, `/workspace`, the `bot` home, SSH server identity,
and Chromium profile. Existing disks/images are never reformatted or rebuilt
by these commands; incomplete initialization stops for operator recovery.
Each new host synchronizes the selected `--ssh-key` public key into the offline
guest before boot, replacing its operator authorized key. The private key stays
local; guest SSH host keys, installed tools, files, and browser state are retained.

Example shell wrapper (run from this checkout):

```sh
computer() {
  infrastructure/microvm/computer.sh "$1" --project bot-111549 \
    --disk bot-computer-data \
    --state-dir /Users/austinbarwick/Documents/dev/agents/nori/.artifacts/persistent-computer/state "${@:2}"
}
computer start --ssh-key "$HOME/.ssh/bot-111549"            # saved Terraform plan
computer start --ssh-key "$HOME/.ssh/bot-111549" --execute  # apply reviewed plan
computer exec --execute -- bash -lc 'whoami; chromium --version; ls /workspace'
computer exec --execute                           # interactive shell
computer tunnel --execute --port 9229             # foreground local CDP tunnel
computer restart-guest --execute                  # same writable disk
computer status
computer stop --execute                           # graceful guest stop, runtime OFF
# Plan and apply a new bounded host when needed; reuse the retained disk.
```

Host access uses IAP to SSH, followed by a local SSH proxy to the guest. Only
IAP's source range can reach host SSH; CDP binds guest loopback and the local
tunnel binds `127.0.0.1`. The operator private key stays local; no agent forwarding
is used. Known host keys persist in the state directory: first contact uses
`accept-new`, subsequent guest connections enforce the same disk's SSH identity.
The non-root browser uses its normal sandbox. Guest public IPv4 egress uses host
NAT; metadata, host services, private/link-local networks are rejected, and guest
IPv6 is disabled. This remains a trusted infrastructure prototype, not jailer or
production isolation validation.

`start` saves a concrete Terraform runtime plan; `--execute` consumes that exact
plan and rechecks its deadline without extending it. Existing runtime ownership
or an attached/replaced data disk blocks startup. `stop` closes Chromium through
guest shutdown, waits for the VMM, syncs/unmounts the retained disk, then applies
Terraform runtime OFF. Only the host and boot disk disappear; the protected data
disk and free foundation network/firewall remain managed. A console-log retrieval
failure warns and still applies runtime OFF after a successful guest shutdown.
A guest-shutdown failure reports an error and leaves runtime cleanup for an
explicit retry or operator recovery before the host deadline. The Terraform
wrapper handles credentials for both direct commands and `computer.sh`.

The data disk has no automatic deletion and no delete command here. Host errors
or the 30-minute daily-cutoff deadline preserve it. Incomplete initialization
requires operator recovery; never reformat or rebuild it silently. The initial
minbase image needed an explicit offline udev repair, now included in the normal
build. Live readbacks retained an exact uniquely valued file, a tool installed after
first boot, an expiry-bearing cookie, and localStorage through both a guest
restart and a fresh Terraform-managed GCE host using the same disk. CDP DOM and
screenshot checks also loaded a real public website; the non-root browser
reported active namespace and Seccomp-BPF sandboxing. Evidence lives in
`.artifacts/persistent-computer/` in the Bot workspace.

### Terraform ownership

`infrastructure/microvm/terraform/` separates durable infrastructure from the
disposable host:

- `foundation` owns six enabled APIs, the existing $50 monthly alert budget,
  retained data disk, and a dedicated `nori-computer` VPC/subnet/IAP SSH rule.
  Import blocks identify the existing APIs, budget, and disk; they do not create
  another data disk or budget. The imported disk ID is
  `3757334953750232874`, and its size/type remain 20 GiB `pd-standard`.
- `runtime` owns one host and its automatically deleted boot disk. It defaults
  to `enabled=false`; there is no scheduler, automatic start, or unattended
  Terraform apply. The free foundation network remains after a host is removed.
- `persistent-host.sh`, `build-guest.sh`, and `vm-control.sh` configure the host
  and guest. Terraform passes these files as metadata; it does not manage guest
  packages, files, Chromium state, or application configuration as cloud resources.

Use the wrapper from this checkout so both stacks use the intended local backend:

```sh
tf=infrastructure/microvm/terraform/tf.sh
"$tf" foundation init -input=false
"$tf" runtime init -input=false
"$tf" foundation plan
"$tf" runtime plan                 # OFF by default; no host creation
```

Terraform 1.14.6 or newer is required; the checked-in lockfiles pin Google provider
8.8.1 and External provider 2.4.2. The wrapper preserves explicit Google
credentials/impersonation settings and an existing local ADC file. When explicit
authentication and the local ADC file are both absent, provider operations capture
a transient `GOOGLE_OAUTH_ACCESS_TOKEN` from the active gcloud login. An expired or
revoked existing ADC must be refreshed or explicitly overridden with
`GOOGLE_OAUTH_ACCESS_TOKEN`. Local commands such as `init`, `version`, `validate`,
and `fmt` do not request a token. Never place an OAuth token or service-account key in Terraform
configuration, variable files, plans, or Git. The provider explicitly charges
API quota to `bot-111549`. Cloud Resource Manager and Service Usage were enabled
to bootstrap Terraform's API/project lookups and are tracked alongside Compute,
Billing Budgets, Cloud Asset, and IAP. Every tracked API has
`disable_on_destroy=false`.

The foundation has been imported and applied. A subsequent refreshed plan showed
no changes. Its initial disk update only claimed ownership of the already
present `purpose=bot-persistent-computer` label; no disk replacement, resizing,
formatting, or attachment change occurred. Review future plans before applying.

Runtime creation requires an explicit `enabled=true` and RFC3339
`termination_time`. The deadline guard rejects past deadlines, durations beyond
30 minutes, and times after that day's 22:00 America/Los_Angeles cutoff. GCE
receives that absolute deadline with action `DELETE` at creation. Automatic
restart is disabled, host maintenance terminates the host, and no service account
is attached. Only the public SSH key is passed to the host. SSH ingress is
limited to IAP's `35.235.240.0/20`; the host uses an ephemeral public IPv4 for
egress. Shutdown metadata requests a graceful guest stop and disk flush.

After a session, apply the runtime with `enabled=false`, including when GCE has
already deleted the host at its deadline. The disabled configuration does not
evaluate the deadline guard, so an expired deadline cannot prevent this cleanup.
Leaving `enabled=true` in a later explicit apply would recreate a deleted host;
keep it off between sessions. Runtime only attaches the retained data disk and
never requests its deletion. It checks both the foundation's recorded disk ID
and the live disk ID before creating a host.

Lifecycle commands share one local advisory lock at
`~/.local/state/nori/bot-111549/terraform/lifecycle.lock`, across operator state
directories and checkouts. `computer.sh` holds it through preflight, plan/apply,
readiness and failure cleanup; direct `tf.sh` lifecycle/state changes take the same
lock. Nested calls reuse the inherited lock, which releases automatically on
process exit. Status, ordinary Terraform readback, guest exec and tunnels do not
hold a session-wide lifecycle lock. Use these wrappers for lifecycle changes;
raw Terraform or commands from another machine bypass this local lock.

State, backend working files, and saved plans live under
`~/.local/state/nori/bot-111549/terraform/`, outside the checkout. The wrapper
uses owner-only permissions; `.gitignore` also excludes accidental in-checkout
state, plans, and variable files. There is no off-device state backup or paid
state bucket. Keep a private backup before moving machines; if state is lost,
restore it or import all existing resources before applying again. Never recover
ownership by replacing the data disk.

The retained disk and budget have `prevent_destroy`. This blocks Terraform
destruction while their resource configuration remains present; it does not
protect against deleting that configuration, out-of-band cloud operations, or
loss of the disk itself. The disk's numeric-ID check also rejects accidental
replacement. Retained data continues to incur storage charges while compute is
off; the daily shutdown policy never authorizes deleting it.

Go `core` and Next.js `web` share this monorepo and retain independent images
and releases. Their current POC integration is described above.
