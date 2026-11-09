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
| r0 (docs) | reviewed independently by all three streams; superseded by r1 once published | `2a5d5bd` | none |
| r0-draft encoding (`contracts/assistant`) | the review's concrete object | `ed6b020` | none |
| r1 | published 2026-11-09 by integration as single editor: 151 fixtures, 0 failures; checker run by the coordinator against that tree | `a449fe3` on `claude/nori-assistant-integration-bd1278` | backend: merged as `2ffce04`, acknowledged; frontend: merged as `f0dc31d`, types regenerated at `cd1dfcd`, acknowledged. r1 is the accepted contract. |
| r2 | published by integration as single editor: 175 fixtures, 0 failures, `make check` green; checker reproduced by the coordinator against that tree. Content: F12 `LibraryItemChange`, `LibraryAction` revise/export/delete with scope, collection, preview text, delete effect; F14 `MemoryFact` with change and forget, `InboxItem` with acknowledge; F15 upload extensions; F16 `CatalogEntry`, `InstallationCreateChange`, `InstallationReview`; F17 `MCPServerChange` with command and environment references only, `MCPServerAction` including set_exposure, `MCPServerTool`, observed diagnostics; F18 `SkillChange`, each change a new version; F19 computer session waiting tasks and task title; F20 `WorkDetail.result`, events capped at 50, `events_truncated`; the `ErrorCode` list and `APIError.current`; the route and status table plus scenario and history formats in the contracts README. Recorded deviation from the agreed text: the rule that `current` is present on `revision_conflict` and `interaction_stale` and absent on `idempotency_conflict` is enforced by the live scenarios, not the schema, because the generated TypeScript collapsed the conditional into a single-code union that rejected existing codes such as `wake_unavailable`; `APIError.code` stays an open string with `ErrorCode` as the list for new endpoints. | `3a99237` on `claude/nori-assistant-integration-bd1278` | backend: fast-forwarded to it, acknowledged; frontend: merged as `75e7aec`, types regenerated, `pending.ts` retired at `bc69dba` except the combined capabilities payload, acknowledged. r2 is the accepted contract. |
| r2.1 | published by integration with fixtures; `make check` green; checker reproduced by the coordinator. Content: `LibraryItem.revision` and `InboxItem.revision` as resource revisions distinct from content version; `CatalogEntry.requested_access` reuses the manifest shape including network and filesystem. Hazard noted in the schema for backend: `state.Result` already serializes `revision` as the task instruction revision, so the Library projection must map its resource revision explicitly rather than embed the result record. | `afdf830` on `claude/nori-assistant-integration-bd1278` | backend: accepted, merged as `9fd69df`, Library revision mapped from the Library resource rather than the result record; frontend: merged `a662f0f`, adopted at `aa8bca2` (Library and inbox mutations send the resource revision; legacy results keep revision 0 and stay read-only). r2.1 is the accepted contract. |
| r2.2 | published by integration: `Connection.account` optional until first sign-in. Checker reproduced by the coordinator. | `ce40471` on `claude/nori-assistant-integration-bd1278` | backend: merged as `ce9909d`, acknowledged; frontend: pending |

Reviews: backend `8c22c80` (`docs/assistant-r0-review-backend.md`), frontend `061cd76` (`docs/assistant-r0-review-frontend.md`), integration `3f49ccd` (`docs/assistant-r0-review-integration.md`), each on its own branch.

r1 content as agreed. From backend: B1 tool model names match `^[A-Za-z0-9_-]{1,64}$` with `__` namespacing because production calls OpenAI; B2 `Policy` carries `location`, `interrupted_cost_usd`, `interrupted_cost_unknown`, `cancellation`, the metadata the workflow already branches on; B3 execution metadata pinned on `CallRef` from the model activity, no workflow version gate, old histories replay the name-lookup path (integration produces the replay evidence); B4 reuse `OperationRecord.Action.Digest` and add immutable `tool`, `binding_digest`, `connection_id`, `authority_ref`, `parent_operation_id`, `provider_refs`; B5 `authority_rule_ids` replaces `authority_ref` on Automation and Goal projections, `goal_id` selector on rules; B6 closed health codes and recovery, `missing_scopes`, `capabilities`; B7 connect returns a 202 receipt with an auth attempt, callback at `GET /v1/connections/oauth/callback` redirects 303 to a relative `return_to`; B8 `account_defaults` live in preferences under one revision, `Connection.defaults` derived; B9 `AvailableTool` gains `capability`, `integration_id`, `effect`, `exposure`; B10 model names unique in the effective set, built-ins win, reserved set extended. From frontend: F1 integrations catalog at `/v1/integrations`; F2 work projection over `state.Run` at `/v1/work`; F3 assistant summary for badges; F4 per-area feature flags and `upload_accept` in capabilities; F5 `snoozed_until`, `last_occurrence`; F6 `POST /v1/automations/preview` returning next runs and proposed rules; F7 occurrence `skipped` with reasons `missed_window`, `paused`, `superseded`, `max_active_runs`; F8 and F9 card read routes and card fields on `state.Part`; F10 interaction `superseded_by`, `resolution`, `task_title`; F11 health codes plus `credential_store_unavailable` and `retry_after`; F13 `SearchHit.jump` as a union; the Preferences shape; `PermissionRule.summary` rendered by the server. From integration: I1 scenario fixtures run offline and live against a core; I2 Go round-trip of fixtures inside the checker; I3 generated TypeScript fixture literals so the compiler catches drift; I4 invalid fixtures for owner forgery, authority leaks and secret leaks; Overlaps resolved in r1: Automation and Goal carry `authority_rule_ids` plus readable rule summaries and the preview returns `proposed_rules` and `authority_summary`; health uses F11's enum with B6's `missing_scopes` and `capabilities`; `action_digest` lives only on `Interaction`; action values keep the key `action`; disconnect carries `purge_cache` and `delete_imported_content`; `return_to` must be relative, with open-redirect fixtures. I5 `OperationProjection` extends the existing operation wire shape (embedded `state.OperationRecord` plus `owner`) with optional fields only, since no current response carries `task_id` or `instruction_revision`; I6 a correction supersedes pending interactions in the transaction that advances the instruction revision; I7 scenario steps name the resource schema behind each receipt.

Facts the reviews established: no built-in tool emits `reversible_write` or `sensitive_send` today, so every write permission and approval path is new code; the web proxy forwards only conversation event and message reads, so every new `/v1` family needs a proxy entry; the retired Google integration stored refresh tokens in plaintext on the user row.

## Pending decisions for Austin

Mechanisms for all four are built configurable. No recommendation is adopted as a production default until Austin resolves it.

| ID | Question | Recommendation in r0 | Interim mechanism |
| --- | --- | --- | --- |
| D1 | Where connected-account credentials live | r0: deployment secret store for refresh tokens and app passwords with owner-scoped references in records. Backend recommends instead envelope-encrypted credential rows in the existing database with a KMS key in production, because refresh compare-and-swap, authorization epoch and disconnect must be atomic with the connection row and the secret store cannot add versions conditionally; integration notes the database option has fewer failure modes to prove. Both positions are open for Austin. | Credential store behind an interface with create, replace at expected version, read, delete and per-connection refresh locking. Local development uses an encrypted local file store so OAuth flows can be exercised. No production backend is written until D1 is resolved. |
| D2 | Standing authority for automations | A direct explicit request authorizes its actions without a second confirmation; an automation holds only what was shown at creation; expanded access returns to the user. | Creation materializes permission rules with explicit selectors and `max_operations`; occurrence tasks carry only that authority. `NORI_AUTOMATION_UNATTENDED=require_rule\|confirm_writes`, default `confirm_writes` until resolved, which turns unattended writes into interactions that expire at the execution window. Acceptance rows A25, A30 and A44 will say "interim D2" until then. |
| D3 | Missed automation runs | Catch up the latest read or summary run; deliver reminders only within their useful window; expire unattended writes outside their window; show missed work. | `missed_run` is required on every definition (`skip`, `latest`, `all`), so the API has no default; the creation screen shows the choice in plain words, preselected once Austin answers. Skipped and expired occurrences carry a reason. |
| D4 | What disconnect retains | Keep saved Library results and action history, stop future access, remove transient caches, offer separate deletion of imported content. | Disconnect always closes admission first; `purge_cache` and `delete_imported` flags default to false; saved items show `copied` provenance and the source connection's status. Backend, frontend and integration all support r0 here. |

## Landed work

| Stream | Commit | Contents | Status |
| --- | --- | --- | --- |
| Backend | `8de4158` | W1: `internal/extensions` registry (definitions, digests, canonical schema validation, model projection, reserved names, snapshots, search); every built-in migrated as a `nori/builtin/*` definition; `CallRef.Exec` pinning with name-based fallback for old histories; additive `OperationRecord` references. Coordinator spot-check: no registry reference in `task/workflow.go`; `history.go` imports only the `Exec` type and `finishModelRound` runs on the activity side. | merged into the integration branch as `4143e92`; `make check` green; replay evidence recorded at `381d8ad` under A40: histories from `ed6b020` (no pinned execution metadata) and `4143e92` (pinned on every call) both replay against the compiled workflow, a tampered copy fails as expected |
| Frontend | `dc6d7d2` | Generated TypeScript types from the r1 schemas, fixture mode, navigation shell; web on port 3213 renders chat and Work against the integration core through the proxy as a test user. | merged into the integration branch |
| Integration | `381d8ad` | `contracts-check` runs live scenarios with `-live` (owner isolation and same-id retry pass against core 8083 with two users), replays every history under `contracts/assistant/histories`, emits every valid fixture as a typed TS literal; `make check` runs all of it plus the generated-type freshness check. Acceptance runbook fixed: `nori-local` does not host the task worker, which needs `TEMPORAL_BUILD_ID` and a set-current-version call. | landed |
| Integration | after `9983f4d` | Recovery checks locally verified at `e680c6f`: worker stopped mid-task for 30 s and restarted (task completed, six operations each at attempt 1, no duplicates); core stopped mid-task (worker finished the workflow, result delivered once after core restart, four operations each at attempt 1). | landed |

Acceptance status at this point: A40 implemented (old and pinned histories replay; dynamic-call history and rollback still to do). Every other row is not started, pending backend W2 and W3 routes and the remaining frontend screens. Backend reports W2 and W3 written and compiling (resource store with receipts, Connections with sign-in attempts, callback, refresh compare-and-swap, account choice and disconnect, the local encrypted credential store, Permission checks and Interactions with corrections superseding pending interactions inside the command-acceptance transaction, the executor branch, the Google connector) and is wiring the `/v1` routes; integration receives the commit and live endpoint list when connections, interactions and operations answer on port 8081.
| Integration | `18a1740` | `scripts/assistant-latency.py` and a first warm-path latency and cost baseline under A43: acknowledgement carries 5–8 s of outer-model time as the largest fixed cost, tools 1–4 s, delivery 3–4 s. | landed |
| Integration | after `afdf830` | Local computer on the integration stack: `computer-agent` built from the branch for linux/arm64 inside the `bot-computer:local-v1` image as container `nori-integration-computer` on `127.0.0.1:8097`, wired to a third test user; a browser task completed end to end for that user while the other two users stay without a computer, so both configurations are covered. One-line glue edit in frontend's `web/src/lib/assistant/sources.ts` (legacy results display revision 0), frontend informed. | landed; unblocks local A34 takeover and A10 managed stdio work |
| Backend | `0e0c152` | W2 and W3: `internal/connections` (sign-in attempts, callback, refresh compare-and-swap, account choice, disconnect), `internal/permissions`, Interactions, the registry-backed Tool executor in `internal/invoke`, Google Calendar, Gmail and Contacts adapters in `internal/connectors`, the `/v1` routes; handed to integration with the endpoint list. Coordinator spot-check: no registry, connection or permission reference in `task/workflow.go`; no token or credential fields in API projections; the only credential store is `secret/localvault`, opened only when `NORI_CREDENTIAL_STORE=local` and `NORI_CLOUD=local`, and every other deployment gets the `Unavailable` store that reports D1 as unresolved. | merged into the integration branch (at `2d2a5b6` with frontend `888b924`), `make check` green. Live scenarios against backend's handlers on core 8083: connection update obligation passes except the account shape (D-003); conversation and connection owner isolation pass after D-002. Apps renders live through the proxy on 3213. Google authorize URL requests only calendar read and identity scopes with PKCE and state; Google accepted the client and the `127.0.0.1:3213` redirect, so the redirect-URI prerequisite is met. |

## Defects

| ID | Found by | Owner | Description | Status |
| --- | --- | --- | --- | --- |
| D-001 | integration, pre-existing at `ed6b020` | backend | With the computer disabled, browser tools are still offered to the model, and a failed cleanup leaves the run open forever. | fixed by backend at `67e01b3`, merged as `e680c6f`, verified by integration: the stuck run settles to failed on the next terminal-recovery pass |
| D-002 | integration, live owner-isolation scenario | backend | Foreign or missing ids with `expected_revision` above zero answered 409 instead of 404, leaking existence across owners. | fixed at `2264300`, merged, isolation scenarios pass |
| D-003 | integration, live connection scenario | backend | A `not_connected` connection still sends an empty `account` object instead of omitting it (r2.2). | fixed by backend at `cdaf81e`, awaiting integration merge |
| D-004 | integration, Apps on live core | frontend | A misleading loading state on the Apps screen. | open, reported to frontend |

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

Google sign-in: the OAuth client accepts the `127.0.0.1:3213` redirect, so the console prerequisite is met. What remains is Austin's decision: completing a real sign-in writes his real Google refresh token into the interim development store (`secret/localvault` under `NORI_LOCAL_ARTIFACT_DIR`, encrypted, opened only with `NORI_CREDENTIAL_STORE=local` and `NORI_CLOUD=local`). The assignment forbids choosing account-credential storage before D1 is resolved, so integration will not ask him to sign in until he either approves that local-only store for local provider verification or resolves D1. If approved, the steps are: open `http://127.0.0.1:3213/apps`, choose Google, Sign in, pick the account, consent. Every provider-verified row (A01–A03, A15–A23) waits on this.

Present in the project `.env` (names only): OpenAI, Anthropic, OpenRouter, Google Calendar OAuth client, Brave Search, Temporal Cloud. Missing and needed for provider verification: Microsoft Graph app registration, iCloud app-specific password path for a test Apple account, HEY account access path, Slack and Teams apps, Notion, Dropbox, OneDrive, Linear and GitHub tokens or MCP OAuth, a push notification provider, and an email delivery provider. Report each gap in the acceptance record as blocked with the exact credential needed.

## Acceptance evidence

`docs/assistant-acceptance.md` on the integration branch (created at `3f49ccd`, 44 rows, all not started), owned by integration, tracks A01–A44 with one of: implemented, locally verified, provider verified, blocked (with the concrete reason). Code compiling or a mock passing is not evidence.
