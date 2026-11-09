# Assistant integration coordination record

Compact record for the parallel implementation of [the assistant integration plan](assistant-integration-plan.md): who owns what, which contract revision is accepted, what is pending for Austin, and where acceptance evidence lives. Chat carries discussion; this file carries decisions.

## Baseline commits

| What | Commit |
| --- | --- |
| Application code the plan and baseline inspected | `c539313` |
| Plan, location map and architecture diagram | `0d7655c` |
| Contract baseline draft r0 (docs only) | `2a5d5bd` |
| Shared starting commit: r0 encoded as schemas, fixtures and checker, plus this record | `ed6b020` |

## Sessions

Three implementation sessions and one coordination session. Each implementation session has its own Git worktree and branch cut from the shared starting commit. Started 2026-11-09 from `ed6b020`. Model and effort below were read back from each session after start, not assumed.

| Role | Session | Branch | Model and effort |
| --- | --- | --- | --- |
| Coordination | this session, `claude/nori-assistant-integration-25ae45` | holds the starting commit and this record | Fable 5.1, high |
| Backend | `local_2b3a2cfd-108c-4e88-b9dc-d21728d7ce72`, name `nori-backend`, worktree `.claude/worktrees/nori-assistant-backend-9c520e` | `claude/nori-assistant-backend-9c520e` | Opus 5.5 (`claude-opus-5-5`), high, verified |
| Frontend | `local_cdc50588-45ab-4501-8a8d-e492526d9c73`, name `nori-frontend`, worktree `.claude/worktrees/nori-assistant-frontend-78149f` | `claude/nori-assistant-frontend-78149f` | Opus 5.5 (`claude-opus-5-5`), high, verified |
| Integration and verification | `local_99b4c3ec-3499-4da1-b738-f8de9cae938b`, name `nori-integration`, worktree `.claude/worktrees/nori-assistant-integration-bd1278` | `claude/nori-assistant-integration-bd1278`; this is the integration branch and the branch the final pull request comes from | Opus 5.5 (`claude-opus-5-5`), high, verified |

Sessions message each other directly by name for work coordination. The coordinator is a peer, not an approver. Disagreements that are not settled by evidence go to Austin through the coordination session; they are listed under Pending below until resolved.

## Ownership

| Stream | Owns | Default files |
| --- | --- | --- |
| Backend | Extension registry, Connections, Permission checks, Tool executor, provider adapters, plugins, MCP client and Nori MCP endpoint, durable operations and Temporal compatibility, Scheduler and delivery, persistence, application APIs. | `core/internal/**` except as noted, `core/cmd/**`. |
| Frontend | The complete assistant experience: navigation, account setup and repair, conversation and cards, Work, Apps, Library, Automations, goals, settings, interactions, mobile and recovery states; the web proxy routes new endpoints need. | `web/**`. |
| Integration | Integration branch, `contracts/assistant/**` edits once a change is agreed, `core/cmd/contracts-check`, cross-stream assembly, representative real workflows, recovery verification, acceptance evidence in `docs/assistant-acceptance.md`. | `contracts/**`, `docs/assistant-acceptance.md`, fixes anywhere after coordinating with the owner. |

Ownership is a default. Rebalance when a stream is the bottleneck; agree before editing another stream's active files.

## Contract revisions

| Revision | Status | Commit | Acknowledged by |
| --- | --- | --- | --- |
| r0 (docs) | draft, under independent review by all three streams | `2a5d5bd` | none |
| r0-draft encoding (`contracts/assistant`) | draft transcription of r0; the review's concrete object | `ed6b020` | none |
| r1 | not yet proposed; will carry the compatible recommendations from the three reviews | | |

A revision is accepted when its schema and fixture change is committed by one editor and each affected consumer records the commit it adopted here.

## Pending decisions for Austin

Mechanisms for all four are built configurable. No recommendation is adopted as a production default until Austin resolves it.

| ID | Question | Recommendation in r0 | Interim mechanism |
| --- | --- | --- | --- |
| D1 | Where connected-account credentials live | Deployment secret store for refresh tokens and app passwords; owner-scoped references in records; local development uses a separately agreed local store. | Credential store behind an interface with create, replace at expected version, read, delete and per-connection refresh locking. Local development uses an encrypted local file store so OAuth flows can be exercised. No production backend is written until D1 is resolved. |
| D2 | Standing authority for automations | A direct explicit request authorizes its actions without a second confirmation; an automation holds only what was shown at creation; expanded access returns to the user. | Authority recorded per automation as a permission rule with explicit selectors. Creation preview shows the exact standing scope. The default offered for unattended writes is configurable; until resolved, unattended `sensitive_send` requires an explicit rule and the preview says so. |
| D3 | Missed automation runs | Catch up the latest read or summary run; deliver reminders only within their useful window; expire unattended writes outside their window; show missed work. | `missed_run` is stored per definition (`skip`, `latest`, `all`). The creation default is configurable; the UI shows the chosen policy and missed occurrences are always visible in history. |
| D4 | What disconnect retains | Keep saved Library results and action history, stop future access, remove transient caches, offer separate deletion of imported content. | Disconnect always closes admission first. Cache cleanup and imported-content deletion are separate steps with a flag; saved items show `copied` provenance and the source connection's status. |

## Disagreements

None recorded. Format when one arises: question, each position with its strongest reasoning, evidence, consequences, affected contracts and workstreams, what is paused, what continues, guidance needed.

## Local stacks

The Compose stack in `core/infrastructure/local/compose.yaml` runs one Postgres and one Temporal dev server. Each session uses its own database, Temporal namespace, task queue and ports so stacks do not interfere, and reads project keys from the main checkout's `.env` through `NORI_ENV_FILE` without copying it. Port 8080 belongs to another local process; leave it alone. Signal only processes you started.

| Session | Core port | Web port | Postgres database | Temporal namespace | Task queue |
| --- | --- | --- | --- | --- | --- |
| Backend | 8081 | 3211 | `nori_backend` | `nori-backend` | `nori-tasks-backend` |
| Frontend | 8082 | 3212 | `nori_frontend` | `nori-frontend` | `nori-tasks-frontend` |
| Integration | 8083 | 3213 | `nori_integration` | `nori-integration` | `nori-tasks-integration` |

## Credentials

Present in the project `.env` (names only): OpenAI, Anthropic, OpenRouter, Google Calendar OAuth client, Brave Search, Temporal Cloud. Missing and needed for provider verification: Microsoft Graph app registration, iCloud app-specific password path for a test Apple account, HEY account access path, Slack and Teams apps, Notion, Dropbox, OneDrive, Linear and GitHub tokens or MCP OAuth, a push notification provider, and an email delivery provider. Report each gap in the acceptance record as blocked with the exact credential needed.

## Acceptance evidence

`docs/assistant-acceptance.md`, owned by integration, tracks A01–A44 with one of: implemented, locally verified, provider verified, blocked (with the concrete reason). Code compiling or a mock passing is not evidence.
