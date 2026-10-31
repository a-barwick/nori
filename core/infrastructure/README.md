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

The release bootstrap's optional `sentry_source_maps` setting references an
existing token secret and the public Sentry organization/project names. GitHub
Actions reads the token into a short-lived, owner-only file outside the web build
context, supplies it through a BuildKit secret mount, and removes the file. No
token value belongs in Terraform, image arguments, or source. Builds use the
source commit as the release ID; the promoted web image obtains its Sentry
environment from the deployed service at runtime.

## Delivery

GitHub Actions owns checks, merging, and deployment:

- `Checks` runs Go, web, and release-process checks in parallel on pull requests.
- `Auto merge` squash-merges a non-draft, same-repository `codex/*` PR authored by
  the repository owner after all three checks pass for its exact head commit.
  `NORI_AUTO_MERGE_USERS` can explicitly allow additional agent accounts. Forks
  and other authors never receive automatic merge or cloud credentials.
- `Release` runs after a push to `main`. Auto merge explicitly dispatches it
  because GitHub does not emit another push workflow for a `GITHUB_TOKEN` merge.
  Runtime, worker, web, and computer builds run in parallel with dependency/image
  caches. One serialized job deploys the immutable artifacts to sandbox, runs a
  real browser task there, then deploys those same artifacts to production and
  checks web/runtime health. A newer main commit supersedes a queued deployment.

Releases preserve databases, files, browser profiles, and retained worker
versions. A busy session stops deployment before any mutation; finish its work
and rerun `Release`. The deployment shuts down test compute and leaves production
paused to honor the personal deployment's cost policy. Use the normal operator
resume command when starting live work. No pipeline lock needs manual deletion.

The `release-manifest` and `deployment-reports` workflow artifacts identify the
commit, immutable images, binary checksum, verification, shutdown, and failure.
If failure occurs before execution admission, deployment attempts restoration of
the previous application version. After admission it preserves state and pauses;
it does not roll back task history or external actions. Use **Re-run all jobs**
to build a new release attempt; retrying only a deployment job would mix artifacts
from the previous attempt and is rejected. If auto-merge succeeds but release
dispatch fails, use **Run workflow** on `Release` for `main`.

Sandbox is dedicated to synthetic validation. Before a retry it may resume
outstanding validation work for up to two minutes, then pause and recheck. If it
still cannot settle, inspect the sandbox task and send a targeted Stop before
retrying; production work is never resumed by this recovery path. Cancelled or
failed deploy steps have a separate shutdown step only if that release began
changing the environment. A refused busy-production preflight leaves it alone.
[GitHub allows at most five minutes for normal cancellation cleanup](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-cancellation),
so a slow drain, force-cancelling the run, or losing its runner can bypass cleanup: inspect native queues, worker
requests, and the computer before resuming or retrying. Do not delete a Terraform
lock until its owning operation is confirmed stopped.

Bootstrap Terraform in `release/terraform` owns GitHub Workload Identity
Federation, scoped existing CI identities, and native backup schedules. Its GCS
backend is `bot-111549-nori-ci-state/nori/ci`. Set the non-secret repository
variable `NORI_WIF_PROVIDER` from its output. Optional source map variables are
`NORI_SENTRY_TOKEN_SECRET`, `NORI_SENTRY_TOKEN_VERSION`, `SENTRY_ORG`, and
`SENTRY_PROJECT`; they contain references/settings, never token values.
Cloud credentials are short-lived OIDC credentials restricted to this repository,
`main`, and `release.yml`. Application secrets stay with their existing runtime
configuration. Do not run bootstrap Terraform as part of an application release.

Production Firestore and the retained computer data disk have native daily
backup schedules with seven-day retention. Disk snapshots are crash-consistent;
the final snapshots survive deletion of the source disk. Schedules alone do not
prove restore works: restore into a separate database/disk for a recovery drill,
never over the retained source.
