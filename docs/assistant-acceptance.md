# Assistant acceptance record

Completion record for [the assistant integration plan](assistant-integration-plan.md): one row per acceptance case A01–A44 with its status, evidence and what blocks it. Owned by the integration stream on branch `claude/nori-assistant-integration-bd1278`.

Statuses: **not started**; **locally verified (recorded provider)** (exercised end to end on the local stack with `NORI_PROVIDER_REPLAY` serving hand-written Google recordings from `contracts/assistant/recordings/google`; proves Nori's side of the flow, says nothing about the real provider); **implemented** (code merged into the integration branch, not yet exercised); **locally verified** (exercised end to end on the local integration stack, evidence linked); **provider verified** (exercised against the real provider account); **blocked** (the concrete missing thing named). A passing mock, a compile, or a model saying "done" is not evidence. Rows verified under an interim D1–D4 mechanism say so.

## Summary

| Status | Count |
| --- | --- |
| blocked | 2 |
| implemented | 26 |
| locally verified | 6 |
| locally verified (recorded provider) | 2 |
| not started | 8 |

## Matrix

| ID | Scenario | Status | Evidence | Needs (credential, account or decision) |
| --- | --- | --- | --- | --- |
| A01 | New user connects several accounts | implemented | Connections with multiple accounts per integration, scopes, selected resources and Preferences account defaults are live at `155bba0`. Recorded provider: one account connects with identity, granted scopes and two calendars (`recorded-connection-signin`); defaults scenario `preferences-revision`. Not yet: several accounts at once shown in Apps and used in conversation. | Google: Austin's browser sign-in, pending his decision on interim credential storage (D1); Microsoft Graph app registration; iCloud app-specific password; no Microsoft or iCloud adapter exists yet. |
| A02 | Cancel, repeat, or replay sign-in callback | locally verified (recorded provider) | Scenario `recorded-connection-signin` passes at `5719a31` (`contracts-check -live http://127.0.0.1:8083 -recorded`): connect returns a one-time state; user B presenting A's state does not connect A's connection; A's callback connects the recorded account (provider id and email from userinfo, scopes from the token response); replaying the same callback creates no second grant (one connected connection); a cancelled sign-in (`error=access_denied`) redirects with `result=cancelled` and leaves no account; disconnect revokes (recorded revoke called) and ends `disconnected`. Core log shows token, userinfo, calendarList and revoke served from recordings. | Real provider leg: as A01 (Austin's sign-in, pending his decision on interim credential storage). |
| A03 | Expired/revoked/insufficient access | implemented | Recorded provider at `2e2351f`: repair asked for exactly the added Gmail scope and the connection came back with the new capability (needs more access → ready); disconnect revokes and closes access, and an approved write after disconnect is refused before dispatch (see recovery checks). Not yet: expired and revoked tokens detected through a refresh failure, and two workers refreshing one connection at once. | Google: Austin's browser sign-in, pending his decision on interim credential storage (D1) for the real revoke. |
| A04 | Capability-only request forbids private reads | implemented | Capability inventory (`/v1/tools`, `/v1/integrations`) is metadata-only. Not yet exercised: a capability-only question checked against operation records for zero private reads. | None. |
| A05 | Install/configure a plugin | locally verified | Scenario `plugins-lifecycle` passes live at `75e47fe`: catalog entries validate, install of nori/morning-briefing 1.0.0 creates no permission rules, user B cannot see the installation. Frontend verified the install and contributions UI on its stack. | None. |
| A06 | Update/pin/rollback/uninstall | locally verified | Same scenario: review of 1.1.0 surfaces added `reversible_write`; update without approving it is refused and the installation stays 1.0.0; update with consent, rollback to 1.0.0 and uninstall succeed. Not yet: an update while a call pinned to the old version is in flight, and dependent routines named in the review. | None. |
| A07 | Failing executable plugin | locally verified | At `01778c3` on user C's local computer (privileged container with a private cgroup namespace delegated to the agent user, standing in for a guest VM with cgroup delegation; local test only): `nori/text-stats` installed with `unknown` access and an allow rule for its tool; a normal call was confirmed (5 words, 1 line, 25 characters); `loop_forever` failed after the 5-second deadline; `allocate_mb: 512` failed at the 64 MB memory limit; Nori, the worker and the guest stayed up throughout. On the default container the guest honestly reports `sandbox_v1: false` and refuses packages. Open: D-026 (a sandbox-contained failure blocks the task with the generic \"may have changed something\" question). Plugin hooks merged at `2e2351f`, not yet exercised. | A production guest with delegated cgroup v2 and user namespaces. |
| A08 | Skill creation/import and execution | locally verified | Scenario `skills-lifecycle` (create, edit makes version 2, `connection_ids` refused, user B 404, delete) passes; D-015 is fully fixed at `2dad57b` (digests computed on read for older skills). Frontend verified create, version, export/import and Run on its stack. | None. |
| A09 | Remote HTTP MCP with OAuth | implemented | MCP HTTP client live at `11357e2`: backend connected to the public DeepWiki server (protocol 2026-07-28, 3 tools). OAuth sign-in for MCP servers returns 400 `unsupported`; bearer tokens via `/v1/mcp/{id}/credential` work (r2.3). Not yet exercised on this stack. | An OAuth-capable MCP server (Linear or GitHub MCP) for the OAuth leg. |
| A10 | Managed stdio MCP | blocked | Hosted: hosted managed stdio needs declared-host egress enforcement in the guest sandbox; the sandbox currently enforces no network at all, and stdio runs only on the local development stack. Local stack, locally verified at `f5d3b55` with a declared command (`NORI_MCP_COMMANDS`, a small go-sdk word-count server): adding the server and connecting started the declared process, negotiated protocol 2026-07-28 and discovered one tool with a digest, offered as effect `unknown`; a task's call needed an approval, ran the server (result "4 words" with its process id), was recorded `confirmed`, and no server process remained afterwards. Start, stop and restart return 400 `unsupported` (each discovery and call starts its own process; there is no long-lived managed process to reconnect). | Egress enforcement in the guest sandbox (hosted). |
| A11 | Large/changing MCP catalog | implemented | Discovery gives each MCP tool a namespaced id and digest and offers it as effect `unknown` needing approval (exercised with the local stdio server, see A10). Not yet: a large catalog with deferred discovery, and a changed tool definition after a pinned call. | None. |
| A12 | MCP resources/prompts/interactive requests | not started | No MCP resource, prompt or interactive-request support in code yet. | Backend implementation. |
| A13 | Nori MCP endpoint | locally verified | At `f221fe9` with scenario user A: a read-scoped client token lists only `read_*` tools and calling `mutate_submit_work` with it is refused (unknown tool); a mutate token lists both; the same `request_id` submitted twice returns the same task id and receipt; revoking the client makes the token answer 401; a token with another user's id swapped in answers 401; `GET /v1/mcp-clients` never includes the token or its hash. Calls by hand need the `Mcp-Method`/`Mcp-Name` headers and the 2026-07-28 `_meta` fields; SDK clients add them. | None. |
| A14 | Nested calls/programmatic composition | not started | `parent_operation_id` exists in records, but no programmatic composition or nested tool calls run yet. | Backend implementation. |
| A15 | Multi-calendar agenda and natural follow-up | implemented | Calendar list, events and freebusy adapters live; recordings cover two calendars. Not yet exercised: the agenda and follow-up journey. | Google: Austin's browser sign-in, pending his decision on interim credential storage (D1). |
| A16 | Availability/conflicts | implemented | Freebusy adapter live; the recording returns an unknown-availability calendar. Not yet exercised. | Google: Austin's browser sign-in, pending his decision on interim credential storage (D1). |
| A17 | Create/update/cancel/invite/RSVP | implemented | Create with attendees goes through approval and writes once (recorded runs 2–11); update, cancel and RSVP not yet exercised. | Google: Austin's browser sign-in, pending his decision on interim credential storage (D1); a second account to receive invitations. |
| A18 | Recurrence and time semantics | implemented | Recurrence scope handling is in the calendar adapter. Not yet exercised. | Google: Austin's browser sign-in, pending his decision on interim credential storage (D1). |
| A19 | Calendar synchronization and ICS | implemented | ICS preview and import (duplicate detection by iCalUID, events.import) merged at `6719ed8`; the web uploader accepts `.ics`. Not yet exercised: needs recordings of `events?iCalUID=` and `events/import`. | Google: Austin's browser sign-in, pending his decision on interim credential storage (D1) for the provider leg. |
| A20 | Email search/read/attachments | implemented | Gmail search/read adapters merged at `0e0c152`. Not yet exercised: no mail recordings yet. HEY has no adapter. | Google: Austin's browser sign-in, pending his decision on interim credential storage (D1); HEY access path for the user's own inbox. |
| A21 | Draft/reply/forward/send | implemented | Draft, reply, forward and send adapters merged; send is always `sensitive_send`. Not yet exercised. | Google: Austin's browser sign-in, pending his decision on interim credential storage (D1); sends only to test recipients Austin names. |
| A22 | Email organization and contacts | implemented | Mail labels and contacts lookup adapters merged. Not yet exercised. | Google: Austin's browser sign-in, pending his decision on interim credential storage (D1). |
| A23 | Cross-app reservation workflow | locally verified (recorded provider) | At `01778c3`–`2e2351f`, user A on the recorded Google account: the reservation email was found by Gmail search and read in full (Lilla Tavern, Fri Nov 13 7:30 pm, LT-58213); the agenda check found the clash with Book club 6:30–8 pm and nothing was added while it clashed; after the user chose to skip book club, the dinner was created on the primary calendar (no guests, no approval needed) and confirmed by read-back; the leave reminder became a one-time automation an hour before (6:30 pm Pacific) and, fired with `run_now`, was delivered to the in-app inbox with a link to its task. Open: no check for an existing dinner event before creating it on a retry; the first attempt hit my recording's lost-response path and its read-back returned a different event, which Nori refused to call success (D-013's disclaimer also still appeared before `0b70977`). | Real provider leg as A01. |
| A24 | Files/projects/messaging connectors | blocked | No Drive, OneDrive, Dropbox, Notion, Linear, GitHub, Slack or Teams adapters; MCP is the intended path and works for DeepWiki only. | A token, app registration or MCP OAuth for each provider. |
| A25 | One-time and recurring reminders | implemented | One-time, in-app, locally at `268ea6b`: automation `auto_07b7…` due 01:28:27Z with the worker stopped from before the due time; core accepted one occurrence (`occ_2f1c…`) and one task at the due time; after the worker restarted the occurrence completed once, with one delivery `delivered` to `dest_in_app`, one inbox item linking to the task, summary `inbox_unread: 1`. No browser was open. Not yet: recurring rules and DST, push and email delivery (no transport credentials; backend records `failed`/`no_transport`), and the production due-time wake (D-010, fix merged at `11357e2`, not yet exercised; locally the core loop polls every 2 s, which hides it). | Push and email delivery providers. |
| A26 | Edit/cancel/snooze and duplicate wakes | implemented | Locally at `268ea6b`: a reminder edited from +40 s to +6 h and another cancelled before its +40 s due time both produced 0 occurrences after the old due time. Duplicate passes are inherent locally (the core loop ticks every 2 s and on every conversation wake) and A25's reminder still produced exactly one occurrence, task and delivery. Not yet: snooze, two schedulers racing on one slot, a lost delayed wake repaired via `/api/resume`. | None. |
| A27 | Event-relative reminders | implemented | Event-relative triggers exist in the Scheduler. Not yet exercised. | Google: Austin's browser sign-in, pending his decision on interim credential storage (D1) (or the recorded provider with a moved event). |
| A28 | Notification channels and deep links | implemented | In-app delivery and inbox verified (see A25); quiet hours stored. Push and email destinations record `failed` with `no_transport`. | Push and email delivery provider credentials. |
| A29 | Goal with failed and successful attempts | implemented | Goals with criteria, blockers and next actions are live; frontend created, paused and completed one on its stack. Not yet: a goal with a failed then a successful attempt. | None. |
| A30 | Conditional monitor/briefing | implemented | Watch triggers and the morning-briefing skill exist. Not yet exercised. | Source providers for a real briefing; D2 interim. |
| A31 | Concurrent tasks and pending questions | implemented | Three parallel tasks from one turn ran independently (performance baseline). Not yet: answers, corrections and Stop aimed at one of several concurrent tasks. | None. |
| A32 | Side-chat capabilities and context | locally verified | Side chat `0fd9aab8` at `6719ed8`: `start_task` confirmed, task origin is the side chat, result appended to it once. D-012 (also announced in the assistant conversation) fixed at `7d55f49`; re-check pending. | None. |
| A33 | Late correction and approval change | implemented | Locally verified with the recorded provider: stale approval after a correction (see recovery checks); unchanged authority is no longer re-requested after D-009's fix (runs 5–7 approve once and write once); a changed action always gets a fresh approval (runs 2–7). Open: D-014 (an approval accepted before the task is waiting is lost). | Real provider leg as A01. |
| A34 | Browser takeover and resume | locally verified | At `4e429e2` on user C's local computer: take over gave the user a 15-minute lease; a browser task opened a `computer_takeover` interaction and made no computer call while the user held control; resume with a wrong lease or a stale instruction revision was refused (409); renew extended the lease; resume with the right lease, task and revision released control, and the task then navigated and took screenshots and completed (D-018 fixed). Not yet: lease expiry (15 minutes), and two tasks contending for the agent lease. | None. |
| A35 | File input and voice | implemented | Composer drag/drop, paste, upload progress and cancel, server accept list including `.ics`, saved drafts; dictation built by frontend. Not yet exercised here. | Voice was set aside in Austin's standing instructions; recorded as built but unverified. |
| A36 | Library/search/revision/export | locally verified | At `75e47fe` on user A's real results: user B gets 404 on A's item, file and legacy result; rename advances the resource revision; Markdown export; delete of all versions removes the item, the legacy result, the file and search hits; search returns results, tasks and assistant messages with typed jump links. D-016 (no side-chat messages in search, loose relevance) fixed at `f221fe9` and verified: side-chat messages are found with a conversation jump, and an unrelated three-word query returns nothing. Revise queued task `5a23002c` (result pending). | None. |
| A37 | Memory and preferences | locally verified | At `75e47fe`: a fact saved in conversation appears in `/v1/memory` with its origin; it changed the seat answer; edit works and a stale edit gets 409; user B cannot edit it; forget empties memory. D-017 (after forgetting, Nori still said the preference was saved) fixed at `f221fe9` and verified: asked again, Nori said no seat preference is saved and that its earlier claim was wrong. | None. |
| A38 | Wrong-account and cross-user attacks | not started | Scenarios pass live at `ce40471`: `conversations-owner-isolation` and `connections-owner-isolation` (user B gets 404 on read, resources, update, actions, receipt lookup and list for user A's ids; A's records unchanged). Request schemas reject `owner_id` in values (fixtures; backend returns 400). Permission rules: scenario `permissions-owner-isolation` passes. Work, operations and interactions, checked by hand at `e79c843` with user B against user A's task `827eb84b`: `GET /v1/work/{id}`, `/v1/work/{id}/operations` and `/v1/interactions/{id}` are 404; `/v1/operations?task_id=` and `/v1/interactions?task_id=` return empty lists; `POST /v1/work/{id}/actions` stop and the legacy `/api/command` stop are 404; A's task kept running with `cancel_requested: false`. Not yet covered: search, cached results, plugin calls, cross-account (same user, wrong connection) cases. | None (two local users, see runbook). |
| A39 | Uncertain external write | locally verified (recorded provider) | Run 5 at `6719ed8`: the provider applied the insert and the response was lost; Nori reconciled by reading back the deterministic event id, recorded the operation `confirmed`, made no second insert and completed. Before the D-011 fix (run 1) the operation stayed visibly `uncertain` and Nori asked the user rather than repeating the write. Not yet: a reconciliation that cannot establish the outcome (it should stay uncertain and block dependent writes). | Real provider leg needs a writable provider account. |
| A40 | Upgrade/replay/rollback | locally verified | Replay: `contracts-check` replays four retained histories (pre-pinning, pinned `Exec`, the approval loop and the lost-approval run) against the current workflow on every `make check`. Rollback: rehearsed between `c050016` and `f221fe9` with in-flight work, a reminder due during the rollback and a full receipt snapshot; nothing lost or changed (see the rehearsal section). Current grants still control new calls (revoke-before-dispatch check). Not yet: a dynamic-call history (MCP or package tool) retained and replayed after its definition changes. | None. |
| A41 | Budget exhaustion and feedback | not started | No budget-exhaustion continuation or user feedback capture in code. | Backend implementation. |
| A42 | Physical mobile and accessibility | not started | | A real phone on the same network as the local web server, operated by Austin, or a reachable test deployment (not production). |
| A43 | Provider failure and performance | not started | Latency and cost baseline started, see Performance baseline below. Provider failure paths not exercised. | Provider accounts for real failure paths; latency baseline runs locally. |
| A44 | Full everyday workflow | implemented | Recorded-provider run at `01778c3`–`2e2351f` covered calendar (agenda across two calendars, conflict, create), email (search, read), reminder creation and in-app delivery, follow-up after the user's decision, and result retrieval through search with jump links (see A23). It stops at: files and tasks (no Drive, Linear or GitHub connector), closed-app delivery (no push or email transport credentials), a real provider (Austin's Google sign-in pending his decision on interim credential storage), and the reminder firing at its real due time (fired with `run_now` instead). | Google sign-in decision; push/email delivery credentials; a files or tasks connector. |

## Recovery checks

Each is run on the local integration stack and recorded here with what could not be exercised.

| Check | Status | Evidence |
| --- | --- | --- |
| Kill and restart the worker mid-task | locally verified | 2026-11-08 at `e680c6f`: task `2321c723` (six sequential web searches) stopped after 4 confirmed operations, worker down 30 s (run stayed `running`, no progress), restarted on the same build `int-e680c6f`. The in-flight workflow task timed out once and was retried; the task completed with 6 operations, each `attempt: 1`, no duplicate operation ids, and the expected result. |
| Kill and restart core mid-task | locally verified | 2026-11-08 at `e680c6f`: task `aa217d42` (four sequential web searches) running with 2 operations done; core (`nori-local`) stopped at 00:56:45. The worker finished the workflow while core was down. After core restarted, the result was delivered to the assistant conversation once (one assistant message at 00:57:38), with 4 operations, each `attempt: 1`. |
| Lost wake and duplicate wake | not started | Duplicate submission only: scenario `conversation-message-retry` passes live (same message id twice gives the same active turn and one user message). A real lost wake (wake failure after acceptance) is not exercised yet. |
| Stale approval after a correction | locally verified (recorded provider) | At `268ea6b` with user A connected through the recorded Google flow: Nori asked to create an event inviting guest@example.com; the task opened approval `int_3742…` (instruction revision 1, digest `act_bbd0…`, account, calendar, recipient and "Google emails 1 attendee" effect). A correction (3:30–4:00 instead) superseded it in the same step (status `superseded`, revision 2); approving it at `expected_revision: 1` returned 409 `interaction_stale` with `current`. The replacement approval `int_3920…` bound the new time with a new digest at instruction revision 2; presenting the old digest to it returned 409 and left it pending. Defects found on the way: D-007, D-008, D-009. |
| Revoke during execution | locally verified (recorded provider), before dispatch | At `2e2351f`: a task's calendar create with a guest waited on its approval; the connection was disconnected (02:19:48Z) and the approval then given (02:19:52Z). No calendar write reached the provider; the task said Google Calendar is no longer connected and to reconnect in Apps, and its completion check listed the approved action as not carried out. Not exercised: revoke while a provider request is already in flight (a lost late response). |
| Registry update after a pinned call | not started | |
| Replay of a retained Temporal history against the new worker | locally verified | See A40: ed6b020 and 4143e92 histories replay against the merged workflow in `contracts-check`. |
| Scheduler race with two due-slot observers | locally verified | At `2e2351f` a second core (port 8084) ran against the same database and Temporal namespace while three one-time reminders fell due at the same instant (02:16:47Z); both cores were processing at that moment. Each reminder produced exactly one occurrence, one task and one delivery. |
| Computer takeover and lease expiry | not started | |
| Disconnect retention (D4 interim flags) | locally verified, partly | At `2e2351f` on three recorded connections, disconnect with each flag combination (`purge_cache` / `delete_imported_content`: off/off, on/off, on/on) closed access at once (resource reads answer 409 `not_connected`), called the provider's revoke, kept the account identity for attribution, and left saved Library items untouched. Not shown: a difference between the flags, because these connections had no cached or imported content yet. |

## Performance baseline

Measured with `scripts/assistant-latency.py --user 11111111-1111-4111-8111-111111111111` on the local stack (core and worker on one laptop, OpenAI outer model, OpenRouter task model, Brave search) at `e680c6f`, 2026-11-08. Seconds; cost in USD from the usage records.

| run | condition | ack | dispatch | model | tools | other | deliver | total | calls | cost |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `ef41561d`, `aad1673f`, `972249a0` | warm; one turn started three parallel one-search tasks | 8.4 | 8.1 | 4.8–6.4 | 0.8–0.9 | 1.9–2.5 | 2.9–4.0 | 20.1 | 5–6 | 0.021–0.038 |
| `2321c723` | warm start, six sequential searches; worker stopped 30 s mid-task | 7.0 | 6.9 | 19.1 | 4.3 | 39.9 (includes the outage) | 3.8 | 74.1 | 11 | 0.063 |
| `aa217d42` | warm start, four sequential searches; core stopped mid-task | 5.5 | 5.4 | 14.1 | 2.9 | 3.2 | 46.2 (core down) | 71.7 | 9 | 0.061 |
| `ec27d94f` | cold: no worker polling until it was started 3 min later | 7.5 | 7.5 | 14.4 | 3.4 | 176.0 (no worker) | 3.9 | 205.1 | 10 | 0.028 |
| `2ae52bd2` | process-cold: first request after restarting core and worker at `c050016`, user idle for over an hour | 5.9 | 5.8 | 6.2 | 0.8 | 2.8 | 4.0 | 19.7 | 6 | 0.025 |
| `7172a197` | warm: the same request shape a minute later, same processes | 5.7 | 5.7 | 4.4 | 0.8 | 1.2 | 1.8 | 13.9 | 6 | 0.026 |

Stages: ack = user message to acknowledgment; dispatch = user message to task start; model and tools = time in `ModelRef` and `ToolRef` activities; other = the rest of the task (history sync, spend, projection, cleanup, workflow-task queueing); deliver = task end to result message. Observations: the acknowledgment costs 5–8 s of outer-model time before any work starts, the largest fixed cost on the warm path; tool time is small next to model time; delivery adds about 3–4 s for the terminal outer turn. Limits: one run per condition; parallel tasks from one turn each count that turn's outer calls, so their costs overlap; every run has one call with unknown cost (the classification hint, `jev`); the process-cold run took 5.8 s longer than the warm run of the same shape (first model call +1.8 s, workflow start and cleanup +1.6 s, delivery +2.2 s), while the acknowledgment cost was the same; a hosted cold start (scale-from-zero wake of core and worker, and an idle computer VM) cannot be measured locally; the provider stage is from recordings, not a real provider.

## Deployment and rollback rehearsal (local stack)

Rehearsed on 2026-11-09 between the integration build `c050016` (new) and `f221fe9` (old: before plugins in the sandbox, hooks, diagnostics and continuation), with user A's real data. Both builds use the same single records migration, so the database needs no down-migration.

| Step | What was done | Result |
| --- | --- | --- |
| Snapshot | Automations with next run, Library items and versions, connections, memory, inbox, finished tasks and every finished task's operation receipts (`snapshot.py`). | 4 automations, 25 Library items, 8 connections, 2 inbox items, 32 finished tasks with receipts. |
| Prepare | A reminder due during the rollback; a six-search task started on the new build. | The task was pinned to `int-c050016`. |
| Roll back | Old worker started beside the new one; core replaced with the old build; old version made current. | All snapshot items and receipts identical under the old build. |
| During rollback | Reminder fell due; a new request was sent. | The reminder fired once on the old build and was delivered; the new task ran on `int-f221fe9`; the in-flight task finished on its pinned `int-c050016` worker. |
| Roll forward | Core back to `c050016`, new version current, old worker stopped. | Nothing lost or changed from the snapshot; receipts identical; Lilla Tavern reminder still due 2026-11-14T02:30:00Z and the edited reminder still due 07:29:37Z; only the rollback period's reminder, three tasks and their results were added. |

How to repeat: keep the previous build's worker running until its pinned workflows finish, replace core, set the worker deployment's current version, then reverse. Not covered: a release that changes the database schema, and production hosting (Cloud Run revisions and the GCE computer), which this rehearsal does not touch.

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
| D-013 | 2026-11-08 at `6719ed8` | backend (first fix `7d55f49` did not remove the disclaimer in A44; second fix `0b70977` merged at `2e2351f`, re-check pending) | Relayed results carried an unwarranted "can't independently confirm … cited tool results … no supporting text" disclaimer: the task model cited prose instead of evidence references. | fixed in `7d55f49`, merged at `26ee755`; re-check pending |
| D-014 | 2026-11-08 at `6719ed8` | backend (fixed in `f66d81f`, merged at `75e47fe`; verified: approvals 13, 33 and 80 ms after opening each delivered and wrote once) | Approving ~100 ms after an approval opened resolved the interaction but recorded no answer command, so task `16eb64a8` waits forever (history `contracts/assistant/histories/6719ed8-approval-lost-command.json`). Response acceptance, resolution and command intent must be atomic. | reported |
| D-015 | 2026-11-08 at `75e47fe` | backend | Skills reported `ref.digest` `""`; `DefinitionRef.digest` requires a content digest. | fixed: new skills at `155bba0` (verified), older skills computed on read at `2dad57b` |
| D-016 | 2026-11-08 at `75e47fe` | backend | Search omitted side-chat messages and matched unrelated multi-word queries. | fixed at `f221fe9`, verified |
| D-017 | 2026-11-08 at `75e47fe` | backend | After a memory was forgotten, Nori still said it was saved, citing the transcript. | fixed at `f221fe9`, verified |
| D-018 | 2026-11-09 at `f221fe9` | backend (fixed with `run_reopen_v1`, merged at `4e429e2`; verified: resumed task navigated and completed) | When a task hit the takeover gate the host cancelled its run in the guest (`/cancel` at 01:52:18); after resume every guest call for that run returned 409 "run canceled" (`/screenshot`, `/navigate`, `/exec`), so task `b5a4ce5c` could not use the computer and failed. | reported |
| D-019 | 2026-11-09 at `17b8f1d` | backend | The package sandbox created its cgroups inside the agent's own cgroup, which cgroup v2 forbids from distributing memory, pids and cpu while it has member processes; after the fix, only the agent moves to the leaf and the browser in the same cgroup still blocks it (EBUSY). | partly fixed at `8fa91de`; follow-up reported |
| D-020 | 2026-11-09 at `4e429e2` | backend | Asked for the main heading of example.com, the browser task answered that the page has no visible main heading (it has "Example Domain"). | reported |
| D-024 | 2026-11-09 at `f5d3b55` | backend (fixed at `90eb176` and following, merged; verified by A07) | Every sandboxed package call (`text_stats`) fails on the host with `computer.rpc.failed` before any request reaches the guest's `/sandbox/run`, and is recorded `uncertain` with a blocking question although it provably did not run. | reported |
| D-025 | 2026-11-09 at `f5d3b55` | backend | A conditional request (\"add it if it doesn't clash\") that took the clash branch ended `failed` because the task listed an irrelevant unmet item. | worker prompt fixed at `0b70977`, merged; the mapping of model-declared unmet items to failed is kept deliberately |
| D-026 | 2026-11-09 at `01778c3` | backend | A sandboxed package call that failed at its deadline blocked the task with the shell-style \"may have changed something\" question, although a sandbox-contained failure is definite. | reported |
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

Local computer for user C (for A07 sandbox checks it runs `--privileged --cgroupns=private -u 0` with a start script that hands the namespace's cgroup root to uid 1000 and drops privileges; local test only): build `computer-agent` for the container (`GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o <dir>/computer-agent ./cmd/computer-agent`), put beside it a `start.sh` that runs headless Chromium with `--remote-debugging-port=9222` in the background and then `exec /nori/computer-agent -listen 0.0.0.0:8090 -tasks /workspace/tasks`, and run it in the existing `bot-computer:local-v1` image:

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
