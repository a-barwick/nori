# Assistant acceptance record

Completion record for [the assistant integration plan](assistant-integration-plan.md): one row per acceptance case A01–A44 with its status, evidence and what blocks it. Owned by the integration stream on branch `claude/nori-assistant-integration-bd1278`.

Statuses: **not started**; **implemented** (code merged into the integration branch, not yet exercised); **locally verified** (exercised end to end on the local integration stack, evidence linked); **provider verified** (exercised against the real provider account); **blocked** (the concrete missing thing named). A passing mock, a compile, or a model saying "done" is not evidence. Rows verified under an interim D1–D4 mechanism say so.

## Summary

| Status | Count |
| --- | --- |
| Not started | 44 |
| Implemented | 0 |
| Locally verified | 0 |
| Provider verified | 0 |
| Blocked (beyond implementation) | listed per row under "Needs" |

## Matrix

| ID | Scenario | Status | Evidence | Needs (credential, account or decision) |
| --- | --- | --- | --- | --- |
| A01 | New user connects several accounts | not started | | Google: blocked on (1) Austin signing in with a Google account at the browser on `http://localhost:3213` and (2) the Google OAuth client listing `http://localhost:3213/api/v1/connections/oauth/callback` as an authorized redirect URI in the Google Cloud console. Microsoft Graph app registration. iCloud app-specific password for a test Apple account. D1 for storing real credentials beyond the interim local store. |
| A02 | Cancel, repeat, replay sign-in callback | not started | | Local verification possible with a recorded callback; provider leg blocked on (1) Austin signing in with a Google account at the browser on `http://localhost:3213` and (2) the Google OAuth client listing `http://localhost:3213/api/v1/connections/oauth/callback` as an authorized redirect URI in the Google Cloud console. |
| A03 | Expired/revoked/insufficient access | not started | | Provider leg blocked on (1) Austin signing in with a Google account at the browser on `http://localhost:3213` and (2) the Google OAuth client listing `http://localhost:3213/api/v1/connections/oauth/callback` as an authorized redirect URI in the Google Cloud console; then Austin revokes the grant at myaccount.google.com. |
| A04 | Capability-only request forbids private reads | not started | | None beyond implementation; verified from operation records showing no read calls. |
| A05 | Install/configure a plugin | not started | | None. |
| A06 | Update/pin/rollback/uninstall | not started | | None. |
| A07 | Failing executable plugin | not started | | Isolated runner on the local computer image. |
| A08 | Skill creation/import and execution | not started | | None. |
| A09 | Remote HTTP MCP with OAuth | not started | | An independent OAuth-capable MCP server; a local one is enough for local verification. Linear or GitHub MCP OAuth for provider verification. |
| A10 | Managed stdio MCP | not started | | Local computer image running managed processes. |
| A11 | Large/changing MCP catalog | not started | | None (local server with a mutable catalog). |
| A12 | MCP resources/prompts/interactive requests | not started | | None for local. |
| A13 | Nori MCP endpoint | not started | | An external MCP client (local). |
| A14 | Nested calls/programmatic composition | not started | | None. |
| A15 | Multi-calendar agenda and natural follow-up | not started | | Google: blocked on (1) Austin signing in with a Google account at the browser on `http://localhost:3213` and (2) the Google OAuth client listing `http://localhost:3213/api/v1/connections/oauth/callback` as an authorized redirect URI in the Google Cloud console, with two calendars on the account; Microsoft and iCloud as in A01. |
| A16 | Availability/conflicts | not started | | As A15. |
| A17 | Create/update/cancel/invite/RSVP | not started | | As A15, plus a second test account to receive invitations. |
| A18 | Recurrence and time semantics | not started | | As A15. |
| A19 | Calendar synchronization and ICS | not started | | As A15; push channels need a public HTTPS callback, which the local stack lacks, so local uses polling/incremental sync and records that limit. |
| A20 | Email search/read/attachments | not started | | HEY: access path for the user's HEY account (not Gmail as a substitute). Gmail: blocked on (1) Austin signing in with a Google account at the browser on `http://localhost:3213` and (2) the Google OAuth client listing `http://localhost:3213/api/v1/connections/oauth/callback` as an authorized redirect URI in the Google Cloud console, with mail scopes. Outlook: Microsoft Graph registration. |
| A21 | Draft/reply/forward/send | not started | | As A20; sends go only to test recipients Austin names. |
| A22 | Email organization and contacts | not started | | As A20. |
| A23 | Cross-app reservation workflow | not started | | Calendar and email providers above. |
| A24 | Files/projects/messaging connectors | not started | | Google Drive (blocked on (1) Austin signing in with a Google account at the browser on `http://localhost:3213` and (2) the Google OAuth client listing `http://localhost:3213/api/v1/connections/oauth/callback` as an authorized redirect URI in the Google Cloud console), OneDrive, Dropbox, Notion, Linear, GitHub, Slack, Teams: a token, app registration or MCP OAuth for each. |
| A25 | One-time and recurring reminders | not started | | Push provider credentials for closed-app delivery; in-app delivery verifiable locally. D2 interim for unattended writes. |
| A26 | Edit/cancel/snooze and duplicate wakes | not started | | None. |
| A27 | Event-relative reminders | not started | | Calendar provider (A15). |
| A28 | Notification channels and deep links | not started | | Push provider and email delivery provider credentials. |
| A29 | Goal with failed and successful attempts | not started | | None for local. |
| A30 | Conditional monitor/briefing | not started | | Source providers for a real briefing; D2 interim. |
| A31 | Concurrent tasks and pending questions | not started | | None. |
| A32 | Side-chat capabilities and context | not started | | None. |
| A33 | Late correction and approval change | not started | | None. |
| A34 | Browser takeover and resume | not started | | Local computer image (`computerd`) on the integration stack. |
| A35 | File input and voice | not started | | Voice is out of scope for current work per Austin's standing instruction; recorded as such unless he brings it back. |
| A36 | Library/search/revision/export | not started | | None. |
| A37 | Memory and preferences | not started | | None. |
| A38 | Wrong-account and cross-user attacks | not started | Existing conversation routes only: scenario `conversations-owner-isolation` passes live (user B gets 404 on read, history, operations, message, cancel and delete of user A's conversation; A's conversation survives). New route families, cached results, plugin calls and search are not built yet. | None (two local users, see runbook). |
| A39 | Uncertain external write | not started | | Fault injection locally; provider leg needs a writable provider account. |
| A40 | Upgrade/replay/rollback | implemented | W1 pinning (`8de4158`) merged as `4143e92`. `contracts-check` replays `contracts/assistant/histories/ed6b020-web-search-task.json` (captured before pinning; calls carry no `Exec`, so the workflow takes the name-lookup path) and `4143e92-pinned-exec-computer-unavailable.json` (every call pins tool ref, binding and policy) against the merged workflow: both replay. A tampered copy fails with TMPRL1100, so the check bites. Remaining for locally verified: a dynamic (non-built-in) call history once W2/W3 land; rollback of a worker build with saved receipts and schedules intact. | None. |
| A41 | Budget exhaustion and feedback | not started | | None. |
| A42 | Physical mobile and accessibility | not started | | A real phone on the same network as the local web server, operated by Austin, or a reachable test deployment (not production). |
| A43 | Provider failure and performance | not started | | Provider accounts for real failure paths; latency baseline runs locally. |
| A44 | Full everyday workflow | not started | | All calendar, email, files, task and delivery providers above. |

## Recovery checks

Each is run on the local integration stack and recorded here with what could not be exercised.

| Check | Status | Evidence |
| --- | --- | --- |
| Kill and restart core mid-task | not started | |
| Lost wake and duplicate wake | not started | Duplicate submission only: scenario `conversation-message-retry` passes live (same message id twice gives the same active turn and one user message). A real lost wake (wake failure after acceptance) is not exercised yet. |
| Stale approval after a correction | not started | |
| Revoke during execution | not started | |
| Registry update after a pinned call | not started | |
| Replay of a retained Temporal history against the new worker | locally verified | See A40: ed6b020 and 4143e92 histories replay against the merged workflow in `contracts-check`. |
| Scheduler race with two due-slot observers | not started | |
| Computer takeover and lease expiry | not started | |
| Disconnect retention (D4 interim flags) | not started | |

## Merges into the integration branch

| Date | Source | Commit | `make check` | `contracts-check` | Exercised | Result |
| --- | --- | --- | --- | --- | --- | --- |
| 2026-11-08 | coordinator record | `d1bf5bf` | n/a (docs) | pass | n/a | merged |
| 2026-11-08 | backend W1 (Extension registry, CallRef.Exec pinning) | `8de4158` via `2ffce04` | pass | pass, 2 histories replay | two web-search tasks on the local stack; pinned `Exec` present on every call | merged; found defect D-001 (pre-existing, below) |
| 2026-11-08 | frontend shell, Work, interaction cards, proxy routes, generated types | `dc6d7d2` | pass, including fixture type-check | pass | web on 3213 as user A: chat history and Work list render from core 8083 through the proxy | merged |

## Defects found in integration

| ID | Found | Owner | Reproduction | Status |
| --- | --- | --- | --- | --- |
| D-001 | 2026-11-08 at `4143e92`, also present at `ed6b020` | backend | Run core and worker with `NORI_COMPUTER_ENABLED=false`, ask for a background web lookup. The model is still offered `browser_navigate`; the call fails, then the cleanup activity fails non-retryably with "computer provider is not configured", the workflow closes FAILED, and the run stays `running` with "Waiting for owned work and cleanup to finish." The dispatcher logs "terminal recovery: computer provider is not configured" every 30 s forever, and the UI shows the task in progress. Workflow `nori-task-11111111-…:dab0dd7b-…` on namespace `nori-integration`. | reported to backend |

## Runbook

Local integration stack. Postgres and Temporal come from `make deps` (Compose, `127.0.0.1:15432` and `127.0.0.1:17233`). Project keys are read from the main checkout's `.env` through `NORI_ENV_FILE`; nothing is copied.

One-time setup:

```bash
docker exec nori-local-postgres-1 psql -U nori -d nori -c 'CREATE DATABASE nori_integration'
```

```bash
temporal operator namespace create --address 127.0.0.1:17233 nori-integration
```

Core (from `core/`). Process environment wins over the `.env` file, so the Temporal Cloud key in `.env` is blanked explicitly:

```bash
NORI_CLOUD=local NORI_STORE_BACKEND=postgres NORI_POSTGRES_URL=postgres://nori@127.0.0.1:15432/nori_integration TEMPORAL_ADDRESS=127.0.0.1:17233 TEMPORAL_NAMESPACE=nori-integration TEMPORAL_LOCAL=true TEMPORAL_TASK_QUEUE=nori-tasks-integration TEMPORAL_API_KEY= TEMPORAL_API_KEY_SECRET_VERSION= PORT=8083 NORI_COMPUTER_ENABLED=false NORI_LOCAL_ARTIFACT_DIR=$HOME/.nori-integration/artifacts NORI_ENV_FILE=/Users/austinbarwick/Documents/dev/agents/nori/.env go run ./cmd/nori-local
```

Test users (two, for isolation checks), created with the same environment:

```bash
go run ./cmd/nori-user -id 11111111-1111-4111-8111-111111111111 -email integration-a@nori.local -name "Integration A" -timezone America/Los_Angeles
```

```bash
go run ./cmd/nori-user -id 22222222-2222-4222-8222-222222222222 -email integration-b@nori.local -name "Integration B" -timezone UTC
```

Task worker (from `core/`, same environment without `PORT`). `nori-local` does not run Temporal workers, so tasks sit at `WorkflowTaskScheduled` until one polls. Workers use Worker Deployment versioning: give each build its own id and make it current:

```bash
TEMPORAL_BUILD_ID=int-$(git rev-parse --short HEAD) go run ./cmd/nori-worker
```

```bash
temporal worker deployment set-current-version --address 127.0.0.1:17233 -n nori-integration --deployment-name nori-task --build-id int-<sha> --yes
```

Running workflows stay pinned to the build that started them; only new tasks move to the new current version.

Core is addressed per user as `http://127.0.0.1:8083/users/<id>/v1/...`; requests carrying `Origin` or `Sec-Fetch-Site` are refused, so browsers go through the web server.

Web (from `web/`):

```bash
bun install && NORI_LOCAL_AGENT_URL=http://127.0.0.1:8083 node ../scripts/run-with-env.mjs /Users/austinbarwick/Documents/dev/agents/nori/.env bun --no-env-file run dev --port 3213
```

Checks:

```bash
make check
```

```bash
cd core && go run ./cmd/contracts-check
```
