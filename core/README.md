# Nori core

The Go service runs Nori's bounded, single-user agent loop using the official
Google Gen AI SDK with Vertex AI Application Default Credentials. Conversation,
run state, events, and cancellation requests live in Firestore; screenshots live
in a private GCS bucket. The existing persistent Firecracker computer supplies
shell commands and Chromium navigation/screenshots through a keyless IAP tunnel.

## Local development and API

Requires Go 1.26, Google Cloud CLI, and ADC for the selected Google project.
Run commands in this section from `core/`. `make run` starts the service on
port 8080; `make build` builds `bin/nori`. No model API
keys are used. The optional monorepo-root `.env` loads without overriding existing
environment variables; keep it gitignored and mode 600.

| Environment | Default / purpose |
| --- | --- |
| `BIND_HOST` | `127.0.0.1` locally; container explicitly sets `0.0.0.0` behind runtime IAM |
| `PORT` | `8080` |
| `NORI_PROJECT_ID` | `bot-111549` (application project, independent of platform variables) |
| `MODEL_LOCATION` | `global` (Vertex model location) |
| `MODEL_NAME` | `gemini-3.1-flash-lite` |
| `FIRESTORE_DATABASE_ID` | `nori-poc` |
| `ARTIFACT_BUCKET` | Required private screenshot bucket |
| `COMPUTER_ZONE` | Required zone of the existing computer VM |
| `COMPUTER_INSTANCE` | `nori-computer` |

The fixed owner is `austinbarwickdavis@gmail.com`. Deploy the Go service behind
Agent Runtime IAM with invocation granted only to the private Next.js web service
account. The browser calls its same-origin web proxy; it does not select an owner
or send cloud credentials. Local startup binds loopback. The container binds all interfaces for the managed
runtime contract; runtime IAM must guard it. The service listener must not be
exposed publicly.

| Route | Request / response |
| --- | --- |
| `POST /api/healthz` or `POST /healthz` | Safe process/configuration health; no database reads |
| `GET /healthz` | `{"status":"ok"}` (process health) |
| `POST /api/session` | `{}` → `{messages,runs,paused,expires_at}` |
| `POST /api/run` | `{id: UUID,message}` → `{run}` after execution stops |
| `POST /api/cancel` | `{id: UUID}` → `{run}` with `cancel_requested` |
| `POST /api/artifact` | `{run_id,artifact_id}` → private PNG bytes |

Create the run UUID in the UI before submitting. The run request stays open while
Nori works; session polling and Stop use separate requests. One Firestore
transaction admits one active run across instances. Runs finish as `completed`,
`failed`, or `cancelled`; cancelled means model/tool execution stopped and any
computer work acknowledged stopping. Failed stop verification is reported as
interrupted. No background execution or automatic durable recovery is promised.
Expired `running` records become `failed` when read or when a new run starts.

Firestore document `system/control` must contain `paused` (boolean) and
`expires_at` (absolute RFC3339 string). Missing, paused, or expired control rejects
new runs; an active run is capped by that expiry. Each run also has a two-minute
execution deadline, six model calls, twelve tool calls, 1024 output tokens per
model response, 32 KiB text tool output, and 2 MiB screenshot limit. A separate
bounded cleanup window confirms stop and persists the terminal record.

`infrastructure/Dockerfile` builds the agent container with the official Cloud
SDK 584.0.0 IAP helper. The current deployment target is GCP Agent Runtime for Go
and Cloud Run for the Next.js app in `../web`. The older Compose stack and
GKE manifests are historical prototypes; the agent does not use Temporal or
Postgres. The retained computer must be provisioned once separately; agent
execution can start that existing VM during an active session but never creates,
replaces, or deletes it.

## Historical boot smoke

A disposable Firecracker trial on 2026-10-24 verified nested KVM, two guest boots
and orderly shutdowns, rootfs-marker survival, and complete resource cleanup.
Historical source remains in `infrastructure/microvm/run.sh`, which now refuses
`--execute`: current cloud lifecycle belongs to Terraform. Operational evidence
is retained in the Bot workspace at `.artifacts/microvm/`, outside this checkout.

## Persistent shell and browser microVM

`infrastructure/microvm/computer.sh` manages one bounded host and a separately
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
