# Nori infrastructure

The application uses Cloud Run for the Next.js web client, Agent Runtime for
the Go API, Temporal Cloud for durable task orchestration, Firestore for saved
task state, and Cloud Storage for screenshots, attached files and saved results. Request-driven workers use
authenticated Cloud Run services, with retained legacy worker pools at zero and
native WCI scalers removed. Computer tools connect through IAP to
the retained Firecracker guest.

| Source | Owns |
| --- | --- |
| [App operations](app/README.md) | Existing web/API deployment, retained computer, operator pause/resume |
| [Temporal operations](temporal/README.md) | Immutable worker images, request services, wake queues, and retained legacy pools |
| `Dockerfile` | Go API image; build from `core/` |
| `temporal/Dockerfile.worker` | Go worker image; build from `core/` |
| `../../web/Dockerfile` | Next.js image; build from the repository root |
| `microvm/` | Original computer provisioning and retained-disk reference scripts |

Names that contain `poc` belong to existing resources and state: the `nori-poc` database, bucket and registry, the service accounts, and the state prefix `nori/poc`. They keep their names. Only the directory and script were renamed, from `poc` to `app` and `ops.py`.

Use the [repository's native local loop](local/README.md) for development.
The web health route is `GET /api/health`; the Go API process health route is
`GET /healthz`. A healthy process alone does not prove task execution or computer
readiness. Local preview runs on loopback at `http://127.0.0.1:3210`.

The current local Compose file starts loopback Postgres and Temporal. The old
Compose environment, container computer and GKE scaffold were retired. Their retained local data, including
`nori-dev_postgres-data`, `nori-dev_computer-workspace`, and
`nori-dev_computer-home`, has not been migrated or deleted.

Leave production available after live checks. Workers run on demand and the
browser VM stops when idle and wakes for the next tool. Use the local stack or
sandbox for checks that need a shutdown. Never delete or rebuild the retained
user disk, browser profile, or files as part of capacity cleanup.

Telemetry is optional. The app and Temporal Terraform stacks each accept the same
`observability` object for Sentry DSNs, AX space/project settings, capture consent,
sampling, and per-process limits. `arize_secret` is the resource name of an
existing Secret Manager secret, not its value. The app state grants the runtime
identity access for both the API and workers; its production state owns the shared
Agent Platform service-agent grant, so sandbox uses the same secret. Apply any
access and service changes through separately reviewed plans. The computer guest
does not receive these credentials.

## Delivery

Use `make check` for local code checks.
Deploy from the latest `main` on this Mac, only when requested, with Docker running
and `gcloud` logged in as the owner. Keep the Mac awake for the 10 or so minutes
it takes.

| Command | What it does |
| --- | --- |
| `make deploy-check` | Read-only rehearsal: Terraform plan, open work, runtime health |
| `make deploy` | Builds, pauses for the release, switches to the new worker, verifies, and reopens if Nori was open |
| `make unpause` | Opens a paused, verified release for use |
| `make pause` | Explicitly closes admission and stops paid compute |
| `make deploy-status` | Shows paused state, Current worker, and the last deploy |
| `make deploy-rollback` | Restores the previous release after an interrupted deploy |
| `make deploy-unlock` | Shows the lock owner and the generation needed to recover an abandoned lock |

`make deploy` refuses to start while a task is running or a message has not been
attempted. While paused, a failed turn's saved input can survive a repair deploy;
it still needs a user follow-up before another attempt.
The saved Terraform plan may include updates to existing resources as well as
the new worker. Resource lifecycle and deletion protections remain in force.
A new worker keeps the Current worker's model.

On release failure, rollback verifies the previous release before restoring
availability. If recovery fails, production may be partially changed: keep
admission closed, inspect `make deploy-status`, and retry the release or run
`make deploy-rollback` before reopening. If only reopening failed after
verification, fix the cause and retry `make unpause`. A paused service stays
paused through a release or rollback. Progress lives in
`~/.local/state/nori/bot-111549/deploy/`; an interrupted deploy can resume from
its last completed step, and images are reused by commit.

Production deploy, rollback, pause, and unpause share an exclusive lock.
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
`bot-111549-nori-ci-state`, with production prefixes `nori/poc` (the app stack,
whose directory is now `app/`) and `nori/temporal` and sandbox prefixes under `nori/sandbox`. The bucket is retained
because it owns the app's infrastructure state, despite its historical name.
Use the existing GCS backend configuration and `NORI_TF_BACKEND=gcs` for operator
commands. Do not initialize empty replacement state.

Production and sandbox app services, databases, artifact buckets, disks, and
application identities are retained. CI-only credentials, permission grants, and
GitHub federation are removed.

## Backups

`app/terraform/backups.tf` keeps seven days of daily copies of production's
Firestore records and the computer's data disk, `bot-computer-data`. Removing CI
doesn't affect them.

- **Restore records:** `gcloud firestore databases restore` from a backup into a new database, or
  `gcloud firestore import` from an export, then point the app at it deliberately.
- **Restore the disk:** `gcloud compute disks create NAME --source-snapshot SNAPSHOT` makes a new disk.
  Never overwrite `bot-computer-data` in place.

The first copies are kept until deleted by hand: snapshot
`nori-computer-data-first-20261031` and export
`gs://bot-111549-nori-poc/firestore-exports/nori-poc-20261031`. The boot disk and sandbox have no scheduled copies.

Production uses `gpt-6.1-sol` for replies and `gpt-6.1-terra` for tasks,
image inspection, and summary refinement. The runtime and new workers reference
`nori-openai-api-key` as `OPENAI_API_KEY`. Sol handles routing without Jev in
production. Local development and sandbox keep their own provider configuration.

## Core caller authorization

`nori-local` enforces route rules by default and identifies loopback
connections without browser-origin headers as local callers. Hosted Core
uses `NORI_ROUTE_AUTH_MODE`. Its default is `off` for existing hosted
callers. Set `observe` to log `nori.access.would_refuse` and `enforce` to
return a coded APIError. Core always verifies client credentials, ownership
and scopes. A client credential travels in `X-Nori-Client-Token` alongside
the platform's service authorization in `Authorization`.

Configure distinct keys of at least 32 bytes:

- `NORI_WEB_CALLER_KEY` in Core and the authenticated web proxy. The
  proxy signs the resolved user ID into its short-lived caller assertion.
- `NORI_INTERNAL_CALLER_KEY` in Core and the trusted operator or AWS
  wake consumer. On AWS, the existing service token continues to protect
  the transport. The additional assertion identifies the internal caller.
- `NORI_GATEWAY_CALLER_KEY` in Core and the public client gateway. The
  gateway signs its own caller assertion. Core verifies the client's token.
- `NORI_QUEUE_CALLER_KEY` in GCP Core and the workers that enqueue
  conversation wakes. Cloud Tasks still uses its OAuth service account.
  The additional assertion is scoped to the exact user's process route and
  survives delayed delivery. It cannot authorize resume or user routes.

On GCP, set `caller_key_secrets` in the application state and
apply the app state's IAM bindings before deploying any workers that use them.
Set `queue_caller_secret` in the Temporal state to the same Secret Manager ID
as `caller_key_secrets["NORI_QUEUE_CALLER_KEY"]`. Both states use the runtime
service account for Secret Manager access.
The release reads `route_auth_mode` and these secret references from the
application state. The operator running resume needs access to the internal
key. Use a sandbox to verify header forwarding through the deployed
Agent Runtime. Check wakes from every retained worker build and any queue
entries already scheduled before switching to enforcement. Production
observation and enforcement each require Austin's approval.

## GCP client gateway

Build the gateway from `core/` with
`docker build -f infrastructure/Gateway.Dockerfile .`. Set the application
Terraform variable `gateway_image` to the reviewed image digest to enable it.
Use `GET /health` to check the public gateway. Cloud Run reserves paths such
as `/healthz` at its frontend.
The Cloud Run service has its own identity and invocation rights on the
configured Agent Runtime. Supply the gateway key's Secret Manager ID in
`caller_key_secrets` and release the corresponding Core code. The private
`/gateway/` upstream namespace requires the gateway assertion. A Core version
that predates this namespace cannot serve a gateway request.
Agent Runtime rewrites HTTP statuses and drops application response headers.
Core sends them in a metadata line on signed gateway responses. The gateway
restores them before sending the original body to the client. Successful event
streams keep their native media type and stream directly.

Clients send `Authorization: Bearer <token>` to the gateway. The gateway
derives the scoped path from the token's owner and forwards the credential in
`X-Nori-Client-Token`. Core validates the credential and scopes on each call.
Public client paths are unscoped and include `/mcp` and the token routes in
the manifest. The two device authorization endpoints accept unsigned device
requests. They have a separate request bucket and their records expire.

The default limit is 120 requests per minute per token with a burst of 30.
A rate-limited request receives HTTP 429, `APIError.code = "rate_limited"`
and a `Retry-After` header in seconds. Rate buckets live in the gateway
process, and Cloud Run is configured for one instance. A new revision or
instance starts with empty buckets. A canonical UUID in `X-Request-ID`
passes through to Core. Otherwise the gateway creates and returns one.

For an isolated loopback test, set `NORI_GATEWAY_TRANSPORT=local`,
`NORI_GATEWAY_UPSTREAM_URL` to the `nori-local` loopback address, and
`NORI_GATEWAY_CALLER_KEY` to the same test key in both processes.
Use `NORI_GATEWAY_REQUESTS_PER_MINUTE` and `NORI_GATEWAY_BURST` to
exercise the limiter. The AWS public gateway infrastructure is follow-up
work. Hosted rollout requires Austin's approval.
