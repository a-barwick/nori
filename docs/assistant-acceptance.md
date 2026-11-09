# Assistant acceptance record

Completion record for [the assistant integration plan](assistant-integration-plan.md): one row per acceptance case A01–A44 with its status, evidence and what blocks it. Owned by the integration stream on branch `claude/nori-assistant-integration-bd1278`.

Statuses: **not started**; **locally verified (recorded provider)** (exercised end to end on the local stack with `NORI_PROVIDER_REPLAY` serving hand-written Google recordings from `contracts/assistant/recordings/google`; proves Nori's side of the flow, says nothing about the real provider); **implemented** (code merged into the integration branch, not yet exercised); **locally verified** (exercised end to end on the local integration stack, evidence linked); **provider verified** (exercised against the real provider account); **blocked** (the concrete missing thing named). A passing mock, a compile, or a model saying "done" is not evidence. Rows verified under an interim D1–D4 mechanism say so.

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
| A01 | New user connects several accounts | not started | | Google: blocked on (1) Austin signing in with a Google account at the browser on `http://127.0.0.1:3213` (Apps → Google → Sign in) and choosing the account and consenting; (2) the console registration of `http://127.0.0.1:3213/api/v1/connections/oauth/callback` appears satisfied: on 2026-11-08 Google accepted the client and redirect and showed its account chooser for "Nori" rather than `redirect_uri_mismatch`, but this is unconfirmed until a callback completes. Microsoft Graph app registration. iCloud app-specific password for a test Apple account. D1 for storing real credentials beyond the interim local store. |
| A02 | Cancel, repeat, or replay sign-in callback | locally verified (recorded provider) | Scenario `recorded-connection-signin` passes at `5719a31` (`contracts-check -live http://127.0.0.1:8083 -recorded`): connect returns a one-time state; user B presenting A's state does not connect A's connection; A's callback connects the recorded account (provider id and email from userinfo, scopes from the token response); replaying the same callback creates no second grant (one connected connection); a cancelled sign-in (`error=access_denied`) redirects with `result=cancelled` and leaves no account; disconnect revokes (recorded revoke called) and ends `disconnected`. Core log shows token, userinfo, calendarList and revoke served from recordings. | Real provider leg: as A01 (Austin's sign-in, pending his decision on interim credential storage). |
| A03 | Expired/revoked/insufficient access | not started | | Provider leg blocked on (1) Austin signing in with a Google account at the browser on `http://127.0.0.1:3213` (Apps → Google → Sign in) and choosing the account and consenting; (2) the console registration of `http://127.0.0.1:3213/api/v1/connections/oauth/callback` appears satisfied: on 2026-11-08 Google accepted the client and redirect and showed its account chooser for "Nori" rather than `redirect_uri_mismatch`, but this is unconfirmed until a callback completes; then Austin revokes the grant at myaccount.google.com. |
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
| A15 | Multi-calendar agenda and natural follow-up | not started | | Google: blocked on (1) Austin signing in with a Google account at the browser on `http://127.0.0.1:3213` (Apps → Google → Sign in) and choosing the account and consenting; (2) the console registration of `http://127.0.0.1:3213/api/v1/connections/oauth/callback` appears satisfied: on 2026-11-08 Google accepted the client and redirect and showed its account chooser for "Nori" rather than `redirect_uri_mismatch`, but this is unconfirmed until a callback completes, with two calendars on the account; Microsoft and iCloud as in A01. |
| A16 | Availability/conflicts | not started | | As A15. |
| A17 | Create/update/cancel/invite/RSVP | not started | | As A15, plus a second test account to receive invitations. |
| A18 | Recurrence and time semantics | not started | | As A15. |
| A19 | Calendar synchronization and ICS | not started | | As A15; push channels need a public HTTPS callback, which the local stack lacks, so local uses polling/incremental sync and records that limit. |
| A20 | Email search/read/attachments | not started | | HEY: access path for the user's HEY account (not Gmail as a substitute). Gmail: blocked on (1) Austin signing in with a Google account at the browser on `http://127.0.0.1:3213` (Apps → Google → Sign in) and choosing the account and consenting; (2) the console registration of `http://127.0.0.1:3213/api/v1/connections/oauth/callback` appears satisfied: on 2026-11-08 Google accepted the client and redirect and showed its account chooser for "Nori" rather than `redirect_uri_mismatch`, but this is unconfirmed until a callback completes, with mail scopes. Outlook: Microsoft Graph registration. |
| A21 | Draft/reply/forward/send | not started | | As A20; sends go only to test recipients Austin names. |
| A22 | Email organization and contacts | not started | | As A20. |
| A23 | Cross-app reservation workflow | not started | | Calendar and email providers above. |
| A24 | Files/projects/messaging connectors | not started | | Google Drive (blocked on (1) Austin signing in with a Google account at the browser on `http://127.0.0.1:3213` (Apps → Google → Sign in) and choosing the account and consenting; (2) the console registration of `http://127.0.0.1:3213/api/v1/connections/oauth/callback` appears satisfied: on 2026-11-08 Google accepted the client and redirect and showed its account chooser for "Nori" rather than `redirect_uri_mismatch`, but this is unconfirmed until a callback completes), OneDrive, Dropbox, Notion, Linear, GitHub, Slack, Teams: a token, app registration or MCP OAuth for each. |
| A25 | One-time and recurring reminders | implemented | One-time, in-app, locally at `268ea6b`: automation `auto_07b7…` due 01:28:27Z with the worker stopped from before the due time; core accepted one occurrence (`occ_2f1c…`) and one task at the due time; after the worker restarted the occurrence completed once, with one delivery `delivered` to `dest_in_app`, one inbox item linking to the task, summary `inbox_unread: 1`. No browser was open. Not yet: recurring rules and DST, push and email delivery (no transport credentials; backend records `failed`/`no_transport`), and the production due-time wake (D-010, fix merged at `11357e2`, not yet exercised; locally the core loop polls every 2 s, which hides it). | Push and email delivery providers. |
| A26 | Edit/cancel/snooze and duplicate wakes | implemented | Locally at `268ea6b`: a reminder edited from +40 s to +6 h and another cancelled before its +40 s due time both produced 0 occurrences after the old due time. Duplicate passes are inherent locally (the core loop ticks every 2 s and on every conversation wake) and A25's reminder still produced exactly one occurrence, task and delivery. Not yet: snooze, two schedulers racing on one slot, a lost delayed wake repaired via `/api/resume`. | None. |
| A27 | Event-relative reminders | not started | | Calendar provider (A15). |
| A28 | Notification channels and deep links | not started | | Push provider and email delivery provider credentials. |
| A29 | Goal with failed and successful attempts | not started | | None for local. |
| A30 | Conditional monitor/briefing | not started | | Source providers for a real briefing; D2 interim. |
| A31 | Concurrent tasks and pending questions | not started | | None. |
| A32 | Side-chat capabilities and context | not started | | None. |
| A33 | Late correction and approval change | implemented | Locally verified with the recorded provider: stale approval after a correction (see recovery checks); unchanged authority is no longer re-requested after D-009's fix (runs 5–7 approve once and write once); a changed action always gets a fresh approval (runs 2–7). Open: D-014 (an approval accepted before the task is waiting is lost). | Real provider leg as A01. |
| A34 | Browser takeover and resume | not started | | Local computer image (`computerd`) on the integration stack. |
| A35 | File input and voice | not started | | Voice is out of scope for current work per Austin's standing instruction; recorded as such unless he brings it back. |
| A36 | Library/search/revision/export | not started | | None. |
| A37 | Memory and preferences | not started | | None. |
| A38 | Wrong-account and cross-user attacks | not started | Scenarios pass live at `ce40471`: `conversations-owner-isolation` and `connections-owner-isolation` (user B gets 404 on read, resources, update, actions, receipt lookup and list for user A's ids; A's records unchanged). Request schemas reject `owner_id` in values (fixtures; backend returns 400). Permission rules: scenario `permissions-owner-isolation` passes. Work, operations and interactions, checked by hand at `e79c843` with user B against user A's task `827eb84b`: `GET /v1/work/{id}`, `/v1/work/{id}/operations` and `/v1/interactions/{id}` are 404; `/v1/operations?task_id=` and `/v1/interactions?task_id=` return empty lists; `POST /v1/work/{id}/actions` stop and the legacy `/api/command` stop are 404; A's task kept running with `cancel_requested: false`. Not yet covered: search, cached results, plugin calls, cross-account (same user, wrong connection) cases. | None (two local users, see runbook). |
| A39 | Uncertain external write | locally verified (recorded provider) | Run 5 at `6719ed8`: the provider applied the insert and the response was lost; Nori reconciled by reading back the deterministic event id, recorded the operation `confirmed`, made no second insert and completed. Before the D-011 fix (run 1) the operation stayed visibly `uncertain` and Nori asked the user rather than repeating the write. Not yet: a reconciliation that cannot establish the outcome (it should stay uncertain and block dependent writes). | Real provider leg needs a writable provider account. |
| A40 | Upgrade/replay/rollback | implemented | W1 pinning (`8de4158`) merged as `4143e92`. `contracts-check` replays `contracts/assistant/histories/ed6b020-web-search-task.json` (captured before pinning; calls carry no `Exec`, so the workflow takes the name-lookup path) and `4143e92-pinned-exec-computer-unavailable.json` (every call pins tool ref, binding and policy) against the merged workflow: both replay. A tampered copy fails with TMPRL1100, so the check bites. Remaining for locally verified: a dynamic (non-built-in) call history once W2/W3 land; rollback of a worker build with saved receipts and schedules intact. | None. |
| A41 | Budget exhaustion and feedback | not started | | None. |
| A42 | Physical mobile and accessibility | not started | | A real phone on the same network as the local web server, operated by Austin, or a reachable test deployment (not production). |
| A43 | Provider failure and performance | not started | Latency and cost baseline started, see Performance baseline below. Provider failure paths not exercised. | Provider accounts for real failure paths; latency baseline runs locally. |
| A44 | Full everyday workflow | not started | | All calendar, email, files, task and delivery providers above. |

## Recovery checks

Each is run on the local integration stack and recorded here with what could not be exercised.

| Check | Status | Evidence |
| --- | --- | --- |
| Kill and restart the worker mid-task | locally verified | 2026-11-08 at `e680c6f`: task `2321c723` (six sequential web searches) stopped after 4 confirmed operations, worker down 30 s (run stayed `running`, no progress), restarted on the same build `int-e680c6f`. The in-flight workflow task timed out once and was retried; the task completed with 6 operations, each `attempt: 1`, no duplicate operation ids, and the expected result. |
| Kill and restart core mid-task | locally verified | 2026-11-08 at `e680c6f`: task `aa217d42` (four sequential web searches) running with 2 operations done; core (`nori-local`) stopped at 00:56:45. The worker finished the workflow while core was down. After core restarted, the result was delivered to the assistant conversation once (one assistant message at 00:57:38), with 4 operations, each `attempt: 1`. |
| Lost wake and duplicate wake | not started | Duplicate submission only: scenario `conversation-message-retry` passes live (same message id twice gives the same active turn and one user message). A real lost wake (wake failure after acceptance) is not exercised yet. |
| Stale approval after a correction | locally verified (recorded provider) | At `268ea6b` with user A connected through the recorded Google flow: Nori asked to create an event inviting guest@example.com; the task opened approval `int_3742…` (instruction revision 1, digest `act_bbd0…`, account, calendar, recipient and "Google emails 1 attendee" effect). A correction (3:30–4:00 instead) superseded it in the same step (status `superseded`, revision 2); approving it at `expected_revision: 1` returned 409 `interaction_stale` with `current`. The replacement approval `int_3920…` bound the new time with a new digest at instruction revision 2; presenting the old digest to it returned 409 and left it pending. Defects found on the way: D-007, D-008, D-009. |
| Revoke during execution | not started | |
| Registry update after a pinned call | not started | |
| Replay of a retained Temporal history against the new worker | locally verified | See A40: ed6b020 and 4143e92 histories replay against the merged workflow in `contracts-check`. |
| Scheduler race with two due-slot observers | not started | |
| Computer takeover and lease expiry | not started | |
| Disconnect retention (D4 interim flags) | not started | |

## Performance baseline

Measured with `scripts/assistant-latency.py --user 11111111-1111-4111-8111-111111111111` on the local stack (core and worker on one laptop, OpenAI outer model, OpenRouter task model, Brave search) at `e680c6f`, 2026-11-08. Seconds; cost in USD from the usage records.

| run | condition | ack | dispatch | model | tools | other | deliver | total | calls | cost |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `ef41561d`, `aad1673f`, `972249a0` | warm; one turn started three parallel one-search tasks | 8.4 | 8.1 | 4.8–6.4 | 0.8–0.9 | 1.9–2.5 | 2.9–4.0 | 20.1 | 5–6 | 0.021–0.038 |
| `2321c723` | warm start, six sequential searches; worker stopped 30 s mid-task | 7.0 | 6.9 | 19.1 | 4.3 | 39.9 (includes the outage) | 3.8 | 74.1 | 11 | 0.063 |
| `aa217d42` | warm start, four sequential searches; core stopped mid-task | 5.5 | 5.4 | 14.1 | 2.9 | 3.2 | 46.2 (core down) | 71.7 | 9 | 0.061 |
| `ec27d94f` | cold: no worker polling until it was started 3 min later | 7.5 | 7.5 | 14.4 | 3.4 | 176.0 (no worker) | 3.9 | 205.1 | 10 | 0.028 |

Stages: ack = user message to acknowledgment; dispatch = user message to task start; model and tools = time in `ModelRef` and `ToolRef` activities; other = the rest of the task (history sync, spend, projection, cleanup, workflow-task queueing); deliver = task end to result message. Observations: the acknowledgment costs 5–8 s of outer-model time before any work starts, the largest fixed cost on the warm path; tool time is small next to model time; delivery adds about 3–4 s for the terminal outer turn. Limits: one run per condition; parallel tasks from one turn each count that turn's outer calls, so their costs overlap; every run has one call with unknown cost (the classification hint, `jev`); no true cold start (fresh core, worker and model connections on an idle machine) is measured yet, and no provider confirmation stage exists until provider writes land.

## Merges into the integration branch

| Date | Source | Commit | `make check` | `contracts-check` | Exercised | Result |
| --- | --- | --- | --- | --- | --- | --- |
| 2026-11-08 | coordinator record | `d1bf5bf` | n/a (docs) | pass | n/a | merged |
| 2026-11-08 | backend W1 (Extension registry, CallRef.Exec pinning) | `8de4158` via `2ffce04` | pass | pass, 2 histories replay | two web-search tasks on the local stack; pinned `Exec` present on every call | merged; found defect D-001 (pre-existing, below) |
| 2026-11-08 | backend D-001 fix | `67e01b3` | pass | pass | stuck run settled | merged as `e680c6f` |
| 2026-11-08 | backend W2/W3, C1, C5, Google connector | `0e0c152` | pass | pass | live scenarios on core 8083 with `NORI_CREDENTIAL_STORE=local`; Google authorize URL checked (calendar.readonly + identity scopes only, PKCE, state, 127.0.0.1:3213 redirect) | merged as `ff180f3`; D-002, D-003 found |
| 2026-11-08 | backend D-002 fix | `2264300` | pass | pass | `connections-owner-isolation` passes | merged |
| 2026-11-08 | frontend Apps, Routines, Goals, Library, Settings, Computer screens | `bc69dba` | pass | pass | fixture-backed until routes land; not evidence | merged |
| 2026-11-08 | frontend optional account, composer input, typed cards | `888b924` | pass | pass | not yet exercised | merged with r2.2 `ce40471` |
| 2026-11-08 | backend D-003 fix, frontend D-004 fix and composer/cards | `cdaf81e`, `5bd6373` | pass | pass, 6 of 6 live scenarios | scenarios on core 8083 at `e79c843` | merged |
| 2026-11-08 | frontend shell, Work, interaction cards, proxy routes, generated types | `dc6d7d2` | pass, including fixture type-check | pass | web on 3213 as user A: chat history and Work list render from core 8083 through the proxy | merged |


### Approval dependability runs (recorded provider)

Each run asks Nori to create an event inviting guest@example.com on user A's recorded Google connection and approves the approval once (`approval-runs.py`, kept in the session scratchpad; the steps are those in the table).

| Run | Build | Provider writes | Approvals | Outcome |
| --- | --- | --- | --- | --- |
| 1 | `11357e2` | 1 (applied, response lost) | 1 | operation `uncertain`; task asked the user without trying reconciliation (D-011) |
| 2, 3, 4 | `11357e2` | 1 each, confirmed | 2 each | after the confirmed insert, Nori read the event back, saw the recorded title "Recorded run" instead of "Recorded run N" (a recording artifact) and asked to fix it: a new action, so a fresh approval and no write. Declined: no write; completion check reported the unmet title |
| 5 | `6719ed8` | 1 (applied, response lost) | 1 | reconciled by reading the event back, operation `confirmed`, task completed, no question (D-011 fixed) |
| 6, 7 | `6719ed8` | 1 each, confirmed | 2 each | as runs 2–4 |
| 8 | `6719ed8` | 0 | 1 | approved ~100 ms after the approval opened: the interaction resolved but no answer command was recorded, so the task never resumed (D-014) |

Across 8 runs: no duplicate write, no write without a matching approval, and every changed action needed a fresh approval. One run lost the approval entirely (D-014).

## Contract obligations exercised live

| Obligation | Status | Evidence |
| --- | --- | --- |
| Connection update (revision guard, idempotent retry, conflicting retry) | locally verified | Scenario `connection-update-idempotency` against backend handlers at `ff180f3`: create 201 at revision 1; X expecting 1 gives revision 2; identical retry of X returns the identical receipt; `GET /v1/mutations/X` returns it; Y expecting 1 gets 409 `revision_conflict` with `current.revision: 2`; X with a changed value gets 409 `idempotency_conflict` without `current`; the connection is unchanged. The `account` shape failure (D-003) is fixed; the whole scenario passes at `e79c843`. |
| Owner isolation | locally verified for conversations, connections and permission rules | See A38; scenario `permissions-owner-isolation` adds rules (B cannot read, update or revoke A's rule; A's stale revoke gets `current`). |
| Preferences revision guard and account defaults | locally verified | Scenario `preferences-revision`: write advances the revision, a stale write gets 409 with `current`, users have separate preferences, and a default naming a connection the user does not own is refused. |
| Acceptance with retry of the same id | locally verified for conversation messages | Scenario `conversation-message-retry`. |

## Defects found in integration

| ID | Found | Owner | Reproduction | Status |
| --- | --- | --- | --- | --- |
| D-002 | 2026-11-08 at `ff180f3` | backend | As user 2222…, `PUT /v1/connections/conn_00000000000000000000` (or user A's id) with `expected_revision: 1` returned 409 `revision_conflict` without `current`; missing or foreign ids must be 404. Not an existence oracle (a random id behaved the same). | fixed in `2264300`, merged; `connections-owner-isolation` passes at `ce40471` |
| D-003 | 2026-11-08 at `ce40471` | backend | A connection created before sign-in is returned with `account: {provider_id: "", label: ""}`; r2.2 requires omitting `account` until sign-in. | fixed in `cdaf81e`, merged; `connection-update-idempotency` passes at `e79c843` |
| D-004 | 2026-11-08 at `2d2a5b6` | frontend | Apps said "Connecting apps isn't available on this Nori yet." while capabilities were still loading. | fixed in `5bd6373`, merged |
| D-006 | 2026-11-08 at `e79c843` | backend | With `NORI_CLOUD=local` and the computer enabled, a user without a local computer URL got an invalid computer provider; the task's final Stop and Cleanup failed non-retryably, the workflow FAILED and the run stayed `running` (task `827eb84b`). | fixed in `a56c217`, merged; the stuck run settled to `failed` on the next terminal-recovery pass |
| D-007 | 2026-11-08 at `268ea6b` | backend (fixed in `a7a0b17`, merged as `11357e2`; not yet re-checked) | A superseded approval's `superseded_by` holds the correction command id (`faa01b78-…`), not the replacement interaction (`int_3920…`). F10 uses it to jump to the replacement. | reported |
| D-008 | 2026-11-08 at `268ea6b` | backend (fixed in `a7a0b17`, merged as `11357e2`; verified: an identical retry returned the same 200 receipt) | Repeating an identical `POST /v1/interactions/{id}/responses` (same request id and body) returned `503 delivery_pending` without the receipt after the first returned `200 applied`; a matching retry must return the recorded receipt. | reported |
| D-009 | 2026-11-08 at `268ea6b` | backend (fixed in `a7a0b17`, merged as `11357e2`; verified across runs 5–7: approve once, write once) | After an approval resolves, the tool result already told the model `needs_interaction`, so the model calls `calendar_event_create` again with a new call id; that becomes a new operation with a new digest (`act_f94e…`, then `act_468d…`, then `act_7890…`) for the identical action and opens another approval. Approved writes never execute and the user is asked again for unchanged authority (A33). History: `contracts/assistant/histories/268ea6b-approval-loop-recorded-calendar.json`, task `dd4284cf` (stopped). | reported |
| D-010 | 2026-11-08 at `7442bff` (code inspection) | backend | Nothing armed a wake for an automation's next due time; the scheduler ticked only inside core's processing loop, so with scale-to-zero hosting a reminder would fire only when something else woke core. | fixed in `a7a0b17` (delayed wake via Cloud Tasks schedule time or SQS delay hops, armed after every tick and edit), merged as `11357e2`; not yet exercised |
| D-011 | 2026-11-08 at `11357e2` | backend | A lost provider response went straight to an uncertain question without trying the connector's reconciliation. | fixed in `2a6c7e5`, merged; verified in run 5 |
| D-012 | 2026-11-08 at `6719ed8` | backend | A side-chat task's result was also announced in the main assistant conversation. | fixed in `7d55f49`, merged at `26ee755`; re-check pending |
| D-013 | 2026-11-08 at `6719ed8` | backend | Relayed results carried an unwarranted "can't independently confirm … cited tool results … no supporting text" disclaimer: the task model cited prose instead of evidence references. | fixed in `7d55f49`, merged at `26ee755`; re-check pending |
| D-014 | 2026-11-08 at `6719ed8` | backend | Approving ~100 ms after an approval opened resolved the interaction but recorded no answer command, so task `16eb64a8` waits forever (history `contracts/assistant/histories/6719ed8-approval-lost-command.json`). Response acceptance, resolution and command intent must be atomic. | reported |
| D-005 | 2026-11-08 at `e79c843` | backend (fixed in `805b534`, merged; verified at `268ea6b` with approvals) | Question interactions projected from `state.Question` disappear when a correction advances the task: `GET /v1/interactions/{id}` becomes 404 and a response at the old revision gets 404 `not_found`, not 409 `interaction_stale` with `current`, and no `superseded`/`superseded_by` record exists for the UI to follow (I6, F10). The stale answer is safely refused. Repro: task `827eb84b-8031-5a0e-a344-e19713ece071` for user 1111…. | reported |
| D-001 | 2026-11-08 at `4143e92`, also present at `ed6b020` | backend | Run core and worker with `NORI_COMPUTER_ENABLED=false`, ask for a background web lookup. The model is still offered `browser_navigate`; the call fails, then the cleanup activity fails non-retryably with "computer provider is not configured", the workflow closes FAILED, and the run stays `running` with "Waiting for owned work and cleanup to finish." The dispatcher logs "terminal recovery: computer provider is not configured" every 30 s forever, and the UI shows the task in progress. Workflow `nori-task-11111111-…:dab0dd7b-…` on namespace `nori-integration`. | fixed in `67e01b3`, merged as `e680c6f`. Verified: after restarting the worker on that build the stuck run settled to `failed` with "computer stop could not be confirmed …" on the next terminal-recovery pass, and the UI no longer shows it in progress. The tool-exposure half (no `location: computer` tools without a computer) is implemented, not yet exercised. |

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

More users, created the same way: C `33333333-3333-4333-8333-333333333333` (`integration-c@nori.local`, `-local-computer-url http://127.0.0.1:8097`) for computer work, and scenario users `44444444-4444-4444-8444-444444444444` and `55555555-5555-4555-8555-555555555555` (`scenario-a@`, `scenario-b@nori.local`) that only `contracts-check -live` uses, so scenario leftovers stay out of the users exercised in the UI.

Local computer for user C: build `computer-agent` for the container (`GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o <dir>/computer-agent ./cmd/computer-agent`), put beside it a `start.sh` that runs headless Chromium with `--remote-debugging-port=9222` in the background and then `exec /nori/computer-agent -listen 0.0.0.0:8090 -tasks /workspace/tasks`, and run it in the existing `bot-computer:local-v1` image:

```bash
docker run -d --name nori-integration-computer --restart unless-stopped --shm-size 1g -p 127.0.0.1:8097:8090 -v <dir>:/nori:ro --entrypoint /nori/start.sh bot-computer:local-v1
```

Connections need `NORI_CREDENTIAL_STORE=local` (an interim local store under `NORI_LOCAL_ARTIFACT_DIR`, refused outside `NORI_CLOUD=local`, pending D1) and `NORI_OAUTH_REDIRECT_URL=http://127.0.0.1:3213/api/v1/connections/oauth/callback` on core and worker.

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
