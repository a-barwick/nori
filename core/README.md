# Nori core

The Go API accepts task commands and persists them in Firestore. Temporal Cloud
owns durable task execution; a separate Go worker calls Vertex Gemini and the
persistent Firecracker computer. Screenshots live in a private GCS bucket. The
browser talks to a same-origin Next.js proxy; runtime IAM restricts the Go API to
the private web service account.

## Local development

Requires Go 1.26, Google Cloud CLI, and Application Default Credentials for the
selected project. From `core/`, `make run` starts the task API on loopback port
8080 and `make build` builds `bin/nori`. The [repository local loop](../README.md)
adds the OpenRouter conversation runner and Jev intent hints.

Commands load the monorepo-root `.env` without overriding existing environment
variables. Keep it gitignored and mode 600. `NORI_ENV_FILE` can select an existing
file from a worktree; the local conversation runner additionally supports
`NORI_TASK_ENV_FILE` for a distinct existing task configuration. These settings
use credentials in place, without moving or copying them. Required names are in
[`.env.example`](../.env.example).

| Configuration | Purpose |
| --- | --- |
| `BIND_HOST`, `PORT` | Defaults `127.0.0.1:8080`; the managed container explicitly binds `0.0.0.0` behind IAM |
| `NORI_PROJECT_ID`, `FIRESTORE_DATABASE_ID` | Defaults `bot-111549`, `nori-poc` |
| `MODEL_LOCATION`, `MODEL_NAME` | Vertex model; defaults `global`, `gemini-3.1-flash-lite` |
| `MODEL_INPUT_USD_PER_MTOK`, `MODEL_OUTPUT_USD_PER_MTOK` | Model list prices; required for task spend accounting |
| `ARTIFACT_BUCKET` | Private screenshot bucket |
| `COMPUTER_ZONE`, `COMPUTER_INSTANCE` | Existing retained computer |
| `NORI_EXECUTION_MODE` | Deployed API uses `temporal`; command default retains `inline` compatibility |
| `TEMPORAL_ADDRESS`, `TEMPORAL_NAMESPACE`, `TEMPORAL_TASK_QUEUE` | Shared API/worker connection and task queue |
| `TEMPORAL_API_KEY` | Local TLS connection key, loaded from the selected env file |
| `TEMPORAL_API_KEY_SECRET_VERSION` | Deployed API alternative: ADC reads the existing Secret Manager version |
| `TEMPORAL_DEPLOYMENT_NAME`, `TEMPORAL_BUILD_ID` | Worker deployment and immutable release version |

Run the worker and API independently after loading configuration:

```sh
go run ./cmd/nori-worker
go run ./cmd/nori
# Repair pending delivery and terminal projections explicitly:
go run ./cmd/nori-deliver
```

The API image is `infrastructure/Dockerfile`, built from `core/`. The worker image
is `infrastructure/temporal/Dockerfile.worker`; the web image is `../web/Dockerfile`,
built from `web/`. Deployment, worker registration, pause/resume, and retained data
ownership are covered by [POC operations](infrastructure/poc/README.md) and
[Temporal operations](infrastructure/temporal/README.md). Source changes do not
update those deployed resources.

## Task protocol

| Route | Behavior |
| --- | --- |
| `GET /healthz` | Process health only |
| `POST /api/healthz` or `/healthz` | Safe configuration health; no database reads |
| `POST /api/session` | Saved messages, tasks, and paused state |
| `POST /api/run` | `{id: UUID,message}`; Temporal acceptance returns HTTP 202 without waiting for execution |
| `POST /api/input` | `{id: new-UUID,run_id: task-UUID,message}`; ordered correction, answer, or explicit `continue` |
| `POST /api/cancel` | `{id: task-UUID}`; records Stop and its delivery intent |
| `POST /api/resume` | `{}`; repairs delivery and wakes retained workflows after operator admission is resumed |
| `POST /api/artifact` | `{run_id,artifact_id}`; private PNG bytes |

One Firestore transaction admits one active task. Repeating an identical command
UUID returns its recorded acceptance; reusing that ID with different content is
rejected. Accepted commands advance an ordered task revision. Effects and final
publication check that revision so an unincorporated correction cannot receive a
stale answer. Durable `system/control.paused` gates work; old `expires_at` fields
are ignored. Missing or paused control rejects new task execution.

A durable task can wait for input, budget approval, or project resume without a
wall-clock task deadline. It survives normal worker shutdown and project pause.
Model requests and tool execution keep per-call limits; the legacy inline API
retains its two-minute request bound. Interrupted or uncertain tool operations
can fail the task rather than risk replaying a side effect. Stop requires an
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
foundation. The current retained POC computer uses the [POC lifecycle](infrastructure/poc/README.md).
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

## Research

- [Assistant features](docs/research/assistant-features-research.md)
- [Grok Bot](docs/research/grokbot-research.md)
- [Muse](docs/research/muse-research.md)
- [GCP execution infrastructure notes](docs/research/gcp-agent-infrastructure-notes.txt)

These documents are research and proposals, not an implementation checklist.
Go `core` and Next.js `web` share this monorepo and retain independent images
and releases. Their current POC integration is described above; historical plans do
not expand its scope.
