# Nori core

The Go API owns durable conversation and task admission in Firestore. Both
`cmd/nori-local` and `cmd/nori` use the same conversation runner, OpenRouter
model, and advisory Jev hints. Temporal owns inner task execution; a separate Go
worker calls OpenRouter Responses and the retained Firecracker computer. Screenshots
live in a private GCS bucket. The browser uses a same-origin Next.js proxy and
owner authentication; hosted runtime IAM admits the web and queue caller
service accounts.

## Local development

Requires Go 1.26, the web dependencies, and the configured provider credentials.
Run `make dev` from the repository root for loopback web and Go servers. The
local Go entrypoint recovers pending conversation work at startup and processes
it on its application lifecycle, independently of browser requests. Firestore
and Temporal remain the configured backing services; select their emulators
explicitly for an isolated local run.

Commands load the monorepo-root `.env` without overriding existing environment
variables. Keep it gitignored and mode 600. `NORI_ENV_FILE` selects an existing
file; `NORI_TASK_ENV_FILE` can select distinct existing task configuration.
Credentials are used in place. Required names are in
[`.env.example`](../.env.example).

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

Sandbox deployments use `nori-sandbox-openrouter-api-key` and the existing
feature lease issued by `infrastructure/spend/run.py` for the same logical chat.
Pass that scope as `sandbox_spend_scope` to Terraform and set new worker targets
to `spend_profile = "feature"`; retained workers keep their original closed lease.
Use `run.py --sandbox` to publish the same key and supply its version. Operator deployment and
admission require its numeric `NORI_SPEND_KEY_SECRET_VERSION`; never restore a
CI secret or substitute the production model key. Closing the lease stops
inference while health and cleanup remain available.

| Configuration | Purpose |
| --- | --- |
| `OPENAI_API_KEY`, `JEV_API_KEY` | Required conversation provider credentials for both entrypoints |
| `NORI_AGENT_MODEL` | Conversation model; default `gpt-6.1-sol`. It runs through OpenAI's platform API at a fixed endpoint, with low reasoning and the standard tier |
| `BIND_HOST`, `PORT` | Hosted entrypoint defaults `127.0.0.1:8080`; managed container binds `0.0.0.0` behind IAM. Local entrypoint always binds loopback |
| `NORI_PROJECT_ID`, `FIRESTORE_DATABASE_ID` | Required, with no defaults; a process without them fails at startup. Google APIs use Application Default Credentials |
| `FIRESTORE_EMULATOR_HOST`, `TEMPORAL_LOCAL` | Explicit local Firestore endpoint and local Temporal mode; absent/false uses configured services |
| `NORI_WAKE_QUEUE`, `NORI_WAKE_SERVICE_ACCOUNT`, `NORI_RUNTIME_URL` | Hosted entrypoint's Cloud Tasks queue, OAuth caller, and native Agent Runtime invoke root ending in `/api` |
| `OPENROUTER_API_KEY` | Inner worker credential; development uses the spend wrapper's scoped key |
| `MODEL_NAME` | Inner worker model; default `openai/gpt-6.1-terra` through OpenRouter Responses |
| `ARTIFACT_BUCKET` | Private screenshot bucket |
| `COMPUTER_ZONE`, `COMPUTER_INSTANCE` | Existing retained computer |
| `TEMPORAL_ADDRESS`, `TEMPORAL_NAMESPACE`, `TEMPORAL_TASK_QUEUE` | Shared API/worker connection and task queue |
| `TEMPORAL_API_KEY` | TLS connection key loaded from the selected env file |
| `TEMPORAL_API_KEY_SECRET_VERSION` | Existing deployed alternative: ADC reads a Secret Manager version |
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
built from `web/`. This source revision has not been deployed. Rollout requires
the queue/IAM Terraform changes, the existing Runtime invoke root supplied as
`runtime_invoke_root`, and server OpenRouter/Jev credentials provisioned through
an approved credential path. Native Runtime status/deadline forwarding and
Cloud Tasks OAuth delivery still need cloud verification. Existing deployment,
worker registration, pause/resume, and retained-data operations are covered by
[app operations](infrastructure/app/README.md) and
[Temporal operations](infrastructure/temporal/README.md).

## API protocol

| Route | Behavior |
| --- | --- |
| `GET /healthz` | Process health only |
| `POST /api/healthz` or `/healthz` | Safe configuration health; no database reads |
| `POST /api/session` | `{before?: message-ID}`; saved conversation/task snapshot and older-message cursor; the initial latest-page load queues one real introduction if history is empty |
| `POST /api/turn` | `{id: UUID,message}`; saves input and confirms wake scheduling before HTTP 202, without waiting for a model answer |
| `POST /api/process` | Internal queue handler; drains conversation and repairs task delivery, returning retryable HTTP 503 while work cannot finish |
| `POST /api/trace` | `{run_id: task-UUID}`; recorded task steps loaded on demand |
| `POST /api/input` | `{id: new-UUID,run_id: task-UUID,message}`; ordered correction, answer, or explicit `continue` |
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

The live conversation E2E suite now checks this async contract, a single saved
introduction, UUID replay/conflict handling, ordered follow-ups and no dispatch
for hypothetical work. Use a fresh isolated database and run from the repo root:
`NORI_E2E_API_URL=http://127.0.0.1:PORT bun test core/e2e/conversation.test.js`.
`NORI_E2E_URL` instead selects the local web proxy. These calls use the selected
real models. The older durable-task suite targets the retired direct-run route
and is not validation of this revision's conversation contract.

The Milestone 3 journeys cover concurrent browser tasks, screenshot retrieval,
answer replay/staleness, targeted Stop, and a detailed trip correction during a
real model request. Start the local API, Temporal worker and browser computer
against a fresh isolated database, then run from the repo root:
`NORI_M3_E2E_API_URL=http://127.0.0.1:PORT bun test core/e2e/milestone3.test.js`.
Use `NORI_M3_E2E_URL` for the local web proxy instead. This separate opt-in suite
uses real providers, rejects preexisting tasks, and stops only tasks it creates.

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

A durable task can wait for input, budget approval, or project resume without a
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

Model/tool activities have one attempt. Each admitted computer operation gets a
stable ID, request digest, and retained receipt under the task directory's
`.nori-receipts`. Identical completed operations can return their recorded result;
started or uncertain receipts refuse automatic re-execution. Cancellation scopes
include task revisions, while working directories remain
`/workspace/tasks/<task-UUID>` across corrections.

Operator cloud capacity is separate from durable task state. Pause/resume can
stop/start workers and the computer without deleting saved work. Keep service
minimums at zero, maximums at one, and stop paid capacity after live work. Follow
the linked operations guides for the exact current lifecycle and its limits.

## Historical computer provisioning reference

These notes describe the original disposable-host trial and retained-disk
foundation. The current retained computer uses the [app lifecycle](infrastructure/app/README.md).
Do not apply the old disposable-host lifecycle against that VM.

### Boot smoke

A disposable Firecracker trial on 2026-10-24 verified nested KVM, two guest boots
and orderly shutdowns, rootfs-marker survival, and complete resource cleanup.
Historical source remains in `infrastructure/microvm/run.sh`, which now refuses
`--execute`: current cloud lifecycle belongs to Terraform. Operational evidence
is retained in the Bot workspace at `.artifacts/microvm/`, outside this checkout.

## Original retained-disk microVM reference

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
