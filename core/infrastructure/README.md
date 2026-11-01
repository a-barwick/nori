# Nori infrastructure

The application uses Cloud Run for the Next.js web client, Agent Runtime for
the Go API, Temporal Cloud for durable task orchestration, Firestore for saved
task state, and Cloud Storage for screenshots. Request-driven workers use
authenticated Cloud Run services, with retained legacy worker pools at zero and
native WCI scalers removed. Computer tools connect through IAP to
the retained Firecracker guest.

| Source | Owns |
| --- | --- |
| [POC operations](poc/README.md) | Existing web/API deployment, retained computer, operator pause/resume |
| [Temporal operations](temporal/README.md) | Immutable worker images, request services, wake queues, and retained legacy pools |
| `Dockerfile` | Go API image; build from `core/` |
| `temporal/Dockerfile.worker` | Go worker image; build from `core/` |
| `../../web/Dockerfile` | Next.js image; build from `web/` |
| `microvm/` | Original computer provisioning and retained-disk reference scripts |

Use the [repository's native local loop](../../README.md) for development.
The web health route is `GET /api/health`; the Go API process health route is
`GET /healthz`. A healthy process alone does not prove task execution or computer
readiness. Local preview runs on loopback at `http://127.0.0.1:3210`.

The old Docker Compose, self-hosted Postgres/Temporal, container computer, and
GKE scaffold have been removed. Removing their source does not remove any
existing Docker containers or volumes. Their retained local data, including
`nori-dev_postgres-data`, `nori-dev_computer-workspace`, and
`nori-dev_computer-home`, has not been migrated or deleted.

Keep paid workers and the computer off after live work. Application deadlines
bound individual work; cloud capacity policy and operator lifecycle are described
in the POC and Temporal guides. Never delete or rebuild the retained user disk,
browser profile, or files as part of capacity cleanup.

Telemetry is optional. The POC and Temporal Terraform stacks each accept the same
`observability` object for Sentry DSNs, AX space/project settings, capture consent,
sampling, and per-process limits. `arize_secret` is the resource name of an
existing Secret Manager secret, not its value. The POC state grants the runtime
identity access for both the API and workers; its production state owns the shared
Agent Platform service-agent grant, so sandbox uses the same secret. Apply any
access and service changes through separately reviewed plans. The computer guest
does not receive these credentials.

## Delivery

Changes are verified locally and merged through pull requests. There is no CI/CD.
Deploy from the latest `main` on this Mac, only when requested, with Docker running
and `gcloud` logged in as the owner. Keep the Mac awake for the 10 or so minutes
it takes.

| Command | What it does |
| --- | --- |
| `make deploy-check` | Read-only rehearsal: local check, real Terraform plan, open work, runtime health |
| `make deploy` | Checks, builds, pauses, deploys, registers and switches to the new worker, verifies, and stays paused |
| `make unpause` | Opens Nori for use. Nothing runs on new code before this |
| `make smoke` | Sends one real browser task through production, about 7¢, into Austin's conversation. Run it only when asked |
| `make pause` | Closes admission and stops paid compute |
| `make deploy-status` | Shows paused state, Current worker, and the last deploy |
| `make deploy-rollback` | Restores the previous release after an interrupted deploy |
| `make deploy-unlock` | Shows the lock owner and the generation needed to recover an abandoned lock |

`make deploy` refuses to start while a task is running or a message has not been
attempted. While paused, a failed turn's saved input can survive a repair deploy;
it still needs a user follow-up before another attempt.
It also refuses any Terraform plan that does more than add the new worker and give
retained workers its route. A new worker keeps the Current worker's model.

After a mutating step starts, failure recovery reconciles pause and verifies the
previous release if a release switch was attempted. If recovery fails, production
may be partially changed: keep admission closed, run `make pause`, inspect
`make deploy-status`, and run `make deploy-rollback` before reopening.
Run `make deploy` again after fixing the
cause; finished steps are reused, and images are reused by commit. If the process
is killed, `make deploy` resumes it instead. Progress lives in
`~/.local/state/nori/bot-111549/deploy/`.

Every deploy and rollback takes an exclusive lock, even for the same commit.
After a killed process, run `make deploy-unlock` to inspect the owner. Confirm it
has stopped, including on another host, then use the displayed generation with
`python3 core/infrastructure/release/deploy.py unlock --lock-generation GENERATION`.
A running local owner is refused, and recovery cannot delete a replacement lock.

The web health page sits behind IAP, so the deploy checks the service's readiness
and image instead of loading the page. Production web builds require source-map
uploads to Sentry's `austin-barwick/nori-web` project using the existing
`nori-sentry-auth-token` Secret Manager credential. Upload failure stops the build.
The read-only operator OAuth credential is not used or changed. Sandbox deploys
are not covered.

Application Terraform state remains in the existing GCS bucket
`bot-111549-nori-ci-state`, with production prefixes `nori/poc` and
`nori/temporal` and sandbox prefixes under `nori/sandbox`. The bucket is retained
because it owns the app's infrastructure state, despite its historical name.
Use the existing GCS backend configuration and `NORI_TF_BACKEND=gcs` for operator
commands. Do not initialize empty replacement state.

Production and sandbox app services, databases, artifact buckets, disks, and
application identities are retained. CI-only credentials, permission grants, and
GitHub federation are removed.

## Backups

`poc/terraform/backups.tf` keeps seven days of daily copies of production's
Firestore records and the computer's data disk, `bot-computer-data`. Removing CI
doesn't affect them.

- **Restore records:** `gcloud firestore databases restore` from a backup into a new database, or
  `gcloud firestore import` from an export, then point the app at it deliberately.
- **Restore the disk:** `gcloud compute disks create NAME --source-snapshot SNAPSHOT` makes a new disk.
  Never overwrite `bot-computer-data` in place.

The first copies are kept until deleted by hand: snapshot
`nori-computer-data-first-20261031` and export
`gs://bot-111549-nori-poc/firestore-exports/nori-poc-20261031`. On October 31, 2026,
the export restored into a temporary database with matching counts for runs, steps,
inputs, attempts, and usage, and the snapshot restored to a ready disk. Files inside
the restored disk were not checked. The boot disk and sandbox have no scheduled copies.

The outer runtime uses `gpt-6.1-sol` with low reasoning through the OpenAI
platform API. Before a future deployment, provision the selected environment's
`nori-openai-api-key` or `nori-sandbox-openai-api-key` Secret Manager version.
The deployment config references it as `OPENAI_API_KEY`; it does not copy the
local key or replace the inner worker's OpenRouter credential.
