# Nori native POC

The Next.js web app runs on Cloud Run with native IAP. An ordinary Go container runs on Vertex AI Agent Runtime, calls Gemini on Vertex AI, stores sessions and run events in Firestore, and writes screenshots to a private GCS bucket. Its computer tools use an official `gcloud compute start-iap-tunnel` connection to the retained Firecracker computer. The host exposes only port 8090 through IAP; the guest holds the persistent browser profile and `/workspace/tasks/<run-id>`.

Run the commands below from `core/`. The source lives in this monorepo; build
the web image from `../web` and the runtime image from `core/` with
`infrastructure/Dockerfile`.

## Provision once

Terraform here owns the new POC resources. The existing `nori-computer` network/subnet and original 20 GiB `bot-computer-data` disk are read as **data sources**. Their old foundation state remains their owner. Never import that disk into this state, format it, or run the older disposable-host lifecycle against this retained VM.

Use the existing operator state at `/Users/austinbarwick/.local/state/nori/bot-111549/poc/terraform.tfstate`. `poc.py` reads this same file. Keep the external state owner-only (mode 600). The registry, bucket, and required APIs have already been provisioned there. Review a full saved plan before applying the remaining resources.

```sh
terraform -chdir=infrastructure/poc/terraform init -backend-config=path=/Users/austinbarwick/.local/state/nori/bot-111549/poc/terraform.tfstate
python3 infrastructure/poc/poc.py bootstrap-identities
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/nori-computer-agent ./cmd/computer-agent
shasum -a 256 /tmp/nori-computer-agent
```

Build and push the web and runtime images independently to the private `us-central1-docker.pkg.dev/bot-111549/nori-poc` repository. Pass their immutable image URIs and the computer binary SHA as Terraform variables `web_image`, `runtime_image`, and `computer_release_sha256`. The configured local backend keeps plan and apply on the same external state. Build the web image from `../web` and the runtime image with this directory's `infrastructure/Dockerfile`.

Before the full apply, upload the Linux binary to `gs://bot-111549-nori-poc/releases/computer-agent-SHA256`. Its object name and checksum must match. New GCE instances initially boot as part of insertion before Terraform stops them; `computer-paused=true` makes that first startup exit without booting the guest. The VM has automatic restart disabled and desired state `TERMINATED`; the host proxy stops it after 15 minutes without tool activity. Its data attachment never auto-deletes.

Terraform uses native `container_spec` for Agent Runtime and `iap_enabled` for Cloud Run. It leaves separately configured owner-only invocation grants unchanged and sets both services' minimum instances to zero. The runtime uses the named `nori-poc` Firestore database. The application uses explicit `NORI_PROJECT_ID=bot-111549`, independent of the
platform project; model selection uses `MODEL_LOCATION=global`
so the runtime region does not override the Vertex model endpoint. The web receives the native Agent Runtime invoke root ending in `/api`; its application routes append `/api/session`, `/api/run`, `/api/cancel`, and `/api/artifact`.

The native IAP gate uses a custom OAuth client for Austin's consumer Gmail account. Configure that through project-level native IAP settings at
`https://iap.googleapis.com/v1/projects/8273801370:iapSettings`, using the monorepo-root `.env`; credentials must stay out of Terraform, VM metadata, and images. The sole owner is `austinbarwickdavis@gmail.com`. Initial access uses Cloud Run's `run.app` URL; `noribot.chat` mapping is optional later.

## Deploy independently

After the first full apply, these commands use native service APIs and preserve Terraform ownership of the infrastructure:

```sh
python3 infrastructure/poc/poc.py publish-computer /tmp/nori-computer-agent
python3 infrastructure/poc/poc.py deploy-runtime IMAGE_URI
python3 infrastructure/poc/poc.py deploy-web IMAGE_URI
```

`deploy-runtime` updates the image and enables Temporal execution with the namespace, task queue, address, and `TEMPORAL_API_KEY_SECRET_VERSION=projects/bot-111549/secrets/nori-temporal-api-key/versions/latest`. It preserves other runtime environment entries. The runtime service account reads the existing Secret Manager secret; no credential payload enters Terraform or the image. Prepare and deploy workers independently through the [Temporal release procedure](../temporal/README.md).

The computer release installs on its next stopped-host start. The host uses Nori
paths and services. Its POC helper keeps the existing `bot-guest-data` attachment,
`BOT_GUEST_DATA` filesystem label, and original READY marker; these identify the
retained image and are not renamed. Guest user `bot`, its browser profile,
`bot-browser.service`, and the single `bot-computer-agent.service` remain in place. Startup checks the object SHA, mounts the existing guest offline with `nodev,nosuid,noexec`, checks every destination parent for symlinks, and installs the binary and a systemd service running as `bot`. It executes no guest programs as host root. Guest/browser data is preserved. No application source or cloud credentials enter the guest.

Terraform ignores independent image changes, computer release metadata, and native lifecycle status updates. Run Terraform plans from this directory and retain the external state; never create a second state that owns these resources.

## Pause and idle stop

```sh
python3 infrastructure/poc/poc.py status
python3 infrastructure/poc/poc.py pause
python3 infrastructure/poc/poc.py unpause
```

Tasks normally start without an operator resume ceremony or execution-session
expiry. Pause closes `system/control.paused`, removes native Temporal compute
configurations before reconciling all retained pools to zero, and stops the
retained computer. It preserves waiting tasks and website/history access.
Unpause restores min-zero/max-one worker scaling, opens admission, and wakes
existing tasks through the runtime. Explicit task Stop remains separate.
The helper reads the existing root `.env` for `TEMPORAL_API_KEY`; use
`NORI_ENV_FILE` to select that existing file from a worktree.

The host proxy defaults to a 15-minute idle timeout. Health checks do not extend
it. It counts active RPCs and checks the guest's internal activity snapshot,
which covers tool cleanup and receipt persistence even after the caller
disconnects. The proxy closes admission before requesting idle shutdown. A
root-owned systemd helper runs the existing graceful guest stop, flushing the
browser and unmounting the retained filesystem, before powering off the host.
The proxy remains unprivileged; the guest receives no cloud credentials.

The next tool starts the existing computer on demand. Connection setup waits
through an in-progress `STOPPING` state and reacquires IAP after restart. A sent
tool is never automatically replayed after a transport failure. A request that
arrives exactly as idle admission closes gets a host-owned rejection before
guest execution. The runtime reconnects and sends once more with the same
operation ID only for that rejection; ambiguous transport failures are not
replayed. The proxy strips the host admission header from guest responses.

Unexpected proxy failures are supervised by systemd. Intentional idle exit 75
is not restarted. If the guest activity check is unavailable, the proxy logs one
warning per failure episode and leaves the host on. If graceful guest shutdown
or filesystem flush fails, the helper reports incomplete shutdown and preserves
the host for reconciliation. Idle stop is therefore not a hard spend cap during
guest or flush failures; explicit operator pause/reconciliation is required.
Inspect `journalctl -u nori-computer-proxy` before retrying those failures.

For the one-time session-removal migration, run `poc.py clear-deadline` while
paused to clear the old VM schedule in place. Google provider 8.8.1 would replace
the retained VM if Terraform removed that field, so native scheduling owns this
migration; unpause verifies the schedule is absent. Run the new pause command
before applying stable WCI IAM,
as described in the [Temporal migration procedure](../temporal/README.md).
The operator needs native Compute/Firestore and Temporal deployment-management
permissions. No IAM Policy Troubleshooter or per-session shutdown proof is
required. The web service account only queries the runtime; the runtime retains
its scoped Firestore, artifact, Vertex prediction and computer get/start/IAP
access. The computer service account only reads release objects.

## Spend and retained costs

Native monthly service spend caps already exist on billing account `010FFA-6C77F1-18FF6D`: Cloud Run $10 (`ec91d69c-af6f-4582-8a57-fc5d74df3922`) and Agent Platform $20 (`c288b003-c839-4ef0-9327-7ae10d3e762b`). The existing $50 alert budget remains. Do not create duplicates. `python3 infrastructure/poc/poc.py spend-caps` reads the existing budgets through the native Billing Budgets REST API; this provider version does not model the spend-cap field.

Pause retains the VM boot disk, original guest disk, GCS objects, images, and Firestore data, so their storage charges continue. Check the project's paid-service inventory by day end. Worker pools scale on Temporal demand; the computer starts only for a tool. No scheduled starts or automatic VM restart are provisioned.

## Computer RPC

The guest binds `172.30.0.2:8090`; the host proxy binds its primary NIC on the same port. The firewall permits only IAP's `35.235.240.0/20`, and tunnel IAM permits the runtime service account only on destination port 8090.

| Request | Result |
| --- | --- |
| `GET /healthz` | Readiness JSON only after a valid Chromium CDP page connection succeeds; otherwise HTTP 503 |
| `POST /exec` with `run_id`, `command`, `timeout_seconds` | `stdout`, `stderr`, `exit_code`; each output capped at 64 KiB, timeout capped at 120 seconds |
| `POST /navigate` with `run_id`, `url` | `url`, `title`, `text`; HTTP/HTTPS only, 45-second deadline |
| `POST /screenshot` with `run_id` | PNG, at most 2 MiB |
| `POST /cancel` with `run_id` | `{ "stopped": true }` only after registered shell/browser work has finished cleanup |

Shell work runs as `bot` in the run's persistent task directory. Every command starts atomically in its own delegated Linux cgroup v2; completion and cancellation use `cgroup.kill` and wait for `populated 0`, including detached/session descendants. Shell execution fails closed without delegation. Browser operations serialize against the existing Chromium CDP listener on loopback port 9222, use its persistent profile, and stop loading on cancellation. The runtime waits up to 180 seconds for computer and browser readiness within the bounded tool activity. Transport or tool errors fail the run immediately; ordinary nonzero shell exit codes remain available to the model. No tool-server secret is needed behind the IAP tunnel.
