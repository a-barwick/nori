# Nori infrastructure

The application uses Cloud Run for the Next.js web client, Agent Runtime for
the Go API, Temporal Cloud for durable task orchestration, Firestore for saved
task state, and Cloud Storage for screenshots. The request-driven worker
candidate uses authenticated Cloud Run services; its demonstration and rollout
approval are pending. Production is paused, with retained legacy worker pools
at zero and native WCI scalers removed. Computer tools connect through IAP to
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

The release bootstrap's optional `sentry_source_maps` setting references an
existing token secret and the public Sentry organization/project names. Cloud
Build reads the token into a short-lived, owner-only file outside the web build
context, supplies it through a BuildKit secret mount, and removes the file. No
token value belongs in Terraform, image arguments, or source. Builds use the
source commit as the release ID; the promoted web image obtains its Sentry
environment from the deployed service at runtime.
