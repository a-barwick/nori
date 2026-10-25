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

Build and push the web and runtime images independently to the private `us-central1-docker.pkg.dev/bot-111549/nori-poc` repository. Pass their immutable image URIs, the computer binary SHA, and `python3 infrastructure/poc/poc.py deadline` as Terraform variables `web_image`, `runtime_image`, `computer_release_sha256`, and `provisioning_deadline`. The configured local backend keeps plan and apply on the same external state. Build the web image from `../web` and the runtime image with this directory's `infrastructure/Dockerfile`.

Before the full apply, upload the Linux binary to `gs://bot-111549-nori-poc/releases/computer-agent-SHA256`. Its object name and checksum must match. New GCE instances initially boot as part of insertion before Terraform stops them; `computer-paused=true` makes that first startup exit without booting the guest. The VM has a cloud STOP deadline, automatic restart disabled, and desired state `TERMINATED`. Its data attachment never auto-deletes.

Terraform uses native `container_spec` for Agent Runtime and `iap_enabled` for Cloud Run. It leaves separately configured owner-only invocation grants unchanged and sets both services' minimum instances to zero. The runtime uses the named `nori-poc` Firestore database. The application uses explicit `NORI_PROJECT_ID=bot-111549`, independent of the
platform project; model selection uses `MODEL_LOCATION=global`
so the runtime region does not override the Vertex model endpoint. The web receives the native Agent Runtime invoke root ending in `/api`; its application routes append `/api/session`, `/api/run`, `/api/cancel`, and `/api/artifact`.

The native IAP gate uses a custom OAuth client for Austin's consumer Gmail account. Configure that through project-level native IAP settings at
`https://iap.googleapis.com/v1/projects/8273801370:iapSettings`, using the monorepo-root `.env`; credentials must stay out of Terraform, VM metadata, and images. `resume` reads these project-level settings because the child Cloud Run service settings do not return the inherited OAuth client. The sole owner is `austinbarwickdavis@gmail.com`. Initial access uses Cloud Run's `run.app` URL; `noribot.chat` mapping is optional later.

## Deploy independently

After the first full apply, these commands use native service APIs and preserve Terraform ownership of the infrastructure:

```sh
python3 infrastructure/poc/poc.py publish-computer /tmp/nori-computer-agent
python3 infrastructure/poc/poc.py deploy-runtime IMAGE_URI
python3 infrastructure/poc/poc.py deploy-web IMAGE_URI
```

`deploy-runtime` updates the image and enables Temporal execution with the namespace, task queue, address, and `TEMPORAL_API_KEY_SECRET_VERSION=projects/bot-111549/secrets/nori-temporal-api-key/versions/latest`. It preserves other runtime environment entries. The runtime service account reads the existing Secret Manager secret; no credential payload enters Terraform or the image. Deploy workers independently through the Temporal release manifest while paused.

The computer release installs on its next stopped-host start. The host uses Nori
paths and services. Its POC helper keeps the existing `bot-guest-data` attachment,
`BOT_GUEST_DATA` filesystem label, and original READY marker; these identify the
retained image and are not renamed. Guest user `bot`, its browser profile,
`bot-browser.service`, and the single `bot-computer-agent.service` remain in place. Startup checks the object SHA, mounts the existing guest offline with `nodev,nosuid,noexec`, checks every destination parent for symlinks, and installs the binary and a systemd service running as `bot`. It executes no guest programs as host root. Guest/browser data is preserved. No application source or cloud credentials enter the guest.

Terraform ignores independent image changes, computer release metadata, and native lifecycle status/deadline updates. Run Terraform plans from this directory and retain the external state; never create a second state that owns these resources.

## Resume and pause

```sh
python3 infrastructure/poc/poc.py resume --build-id BUILD_ID --shutdown-verified
python3 infrastructure/poc/poc.py status
python3 infrastructure/poc/poc.py pause
```

Resume requires configured IAP OAuth, paused durable control, a stopped computer, and an immutable worker build already deployed through the [Temporal module](../temporal/README.md). `--shutdown-verified` attests the documented live Scheduler scale-to-zero proof; it does not skip the per-session checks. Resume installs and reads back a cloud-side VM STOP deadline three hours from session start. It arms every retained pool's shutdown job with WCI disabled, verifies all pools have reconciled to zero and effective WCI update denial, then enables WCI for the chosen build alone.

Task admission expires four minutes before the worker shutdown; WCI expires two minutes before shutdown, and worker shutdown is at least ten minutes before the VM deadline. These margins allow admitted work to cancel and clean up while a worker is available. `system/control` holds the task admission expiry. Owner IAP access, IAP service-agent Cloud Run invocation, and web runtime-query access remain available so Austin can open the website and saved history after pause or expiry. Native IAP and the application's exact owner-email check still enforce access. The VM stays stopped until a computer tool starts it. Expiry never resumes anything; a new session requires an explicit operator command. There is no fixed time-of-day cutoff.

Pause writes `paused=true`, marks the active run for cancellation, calls the runtime cancellation endpoint, and waits for a terminal run status. It preserves all three website invocation bindings, disables the WCI invoker account, removes its pool grant, reconciles every retained worker pool to zero, and stops the retained computer. Shutdown jobs remain armed until zero capacity and effective WCI denial are verified, then are paused. It reports success only after the VM is `TERMINATED`, every pool is reconciled to zero, and native IAM Policy Troubleshooter verifies denial for WCI updates. Website and history requests remain available; durable paused or expired control rejects new task execution. API errors, inherited grants, or unknown decisions leave an explicit incomplete-pause error. Check those failures before claiming shutdown. Runtime requests have a two-minute limit and watch durable pause/expiry during work.

The operator needs the project's normal provisioning/IAM permissions and Policy Troubleshooter access. The running web service account can only query the runtime. The runtime can access the named Firestore database, its artifacts prefix, Vertex model prediction, and the single computer's get/start and IAP port 8090. The computer service account only reads computer release binary objects.

## Spend and retained costs

Native monthly service spend caps already exist on billing account `010FFA-6C77F1-18FF6D`: Cloud Run $10 (`ec91d69c-af6f-4582-8a57-fc5d74df3922`) and Agent Platform $20 (`c288b003-c839-4ef0-9327-7ae10d3e762b`). The existing $50 alert budget remains. Do not create duplicates. `python3 infrastructure/poc/poc.py spend-caps` reads the existing budgets through the native Billing Budgets REST API; this provider version does not model the spend-cap field.

Pause retains the VM boot disk, original guest disk, GCS objects, images, and Firestore data, so their storage charges continue. Check the project's paid-service inventory by day end. The separate Temporal module owns shutdown-only Scheduler jobs. No unattended start or automatic restart is provisioned.

## Computer RPC

The guest binds `172.30.0.2:8090`; the host proxy binds its primary NIC on the same port. The firewall permits only IAP's `35.235.240.0/20`, and tunnel IAM permits the runtime service account only on destination port 8090.

| Request | Result |
| --- | --- |
| `GET /healthz` | Readiness JSON only after a valid Chromium CDP page connection succeeds; otherwise HTTP 503 |
| `POST /exec` with `run_id`, `command`, `timeout_seconds` | `stdout`, `stderr`, `exit_code`; each output capped at 64 KiB, timeout capped at 120 seconds |
| `POST /navigate` with `run_id`, `url` | `url`, `title`, `text`; HTTP/HTTPS only, 45-second deadline |
| `POST /screenshot` with `run_id` | PNG, at most 2 MiB |
| `POST /cancel` with `run_id` | `{ "stopped": true }` only after registered shell/browser work has finished cleanup |

Shell work runs as `bot` in the run's persistent task directory. Every command starts atomically in its own delegated Linux cgroup v2; completion and cancellation use `cgroup.kill` and wait for `populated 0`, including detached/session descendants. Shell execution fails closed without delegation. Browser operations serialize against the existing Chromium CDP listener on loopback port 9222, use its persistent profile, and stop loading on cancellation. The runtime waits up to 90 seconds for computer and browser readiness within the same two-minute run deadline. Transport or tool errors fail the run immediately; ordinary nonzero shell exit codes remain available to the model. No tool-server secret is needed behind the IAP tunnel.
