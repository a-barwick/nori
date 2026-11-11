# Backend status (restart of 2026-11-10)

Branch `claude/nori-assistant-backend-9c520e`, worked from worktree `.claude/worktrees/backend-restart-26132e` on the new machine. The first run's version of this note is at `origin/claude/nori-assistant-integration-25ae45:docs/assistant-notes/backend-status.md`. This file is the current state.

Evidence label (rule 2): **provider verified: 0.** No real provider account was used. The D-053 and D-054 re-tests in the third assignment ran against the real models (OpenAI and OpenRouter, keys from `.env`); everything else is locally verified against a loopback model stub, Google answering from hand-written recordings, the local database and the local Temporal dev server.

## Third assignment (2026-11-10, coordination record "Slice and third assignments")

Stack: core 8081, worker, database `nori_backend`, namespace `nori-backend`, queue `nori-tasks-backend`, user A. Integration's processes were not touched.

### 1. D-053 and D-054 against the real models

Build `be-ed59dc5` (integration's head, which holds both fixes), real keys from `.env` read in place: the conversation on `gpt-6.1-sol` through OpenAI, tasks on `openai/gpt-6.1-terra` through OpenRouter. The intent classifier is off (no key in `.env`).

| Check | Task or message (UTC) | Result |
| --- | --- | --- |
| D-054, conversation, saved zone America/Los_Angeles | message 03:47:25 | "It's 7:47 p.m. on Tuesday, November 10, 2026, for you. I'm using America/Los_Angeles". Correct. |
| D-054, after `PUT /v1/preferences` to Asia/Tokyo (revision 2), no restart | message 03:47:58 | "It's 12:47 p.m. on Wednesday, November 11, 2026, for you. I'm now using Asia/Tokyo". Correct (03:47 UTC is 12:47 JST). |
| D-054, background task after the change | task `1b5290ff-6e7d-5cd1-aa6d-69ad30dd84be`, 03:48:25 to 03:48:27 | "Today is Wednesday, November 11, 2026, in Asia/Tokyo. The current local time was not provided." Correct: the task prompt carries the date and the zone, not the clock. No London anywhere. |
| D-053, worker SIGKILLed during a real model request | task `1c92850d-0932-53d1-8072-3f1dfb1db276` (a 1,500-word story) | Request 03:49:24; worker killed 03:49:31 and restarted 03:49:35; the recovery attempt settled the request as interrupted at 03:53:24 (the dead attempt's 240 s window), recorded the `model-step-deadline` marker, asked once more, and the model answered at 03:54:12 (48 s). Completed at 4:48 with a result. Cost status "unknown", because the cut-off request's cost was never reported. |

Not re-testable against a real model: the 2-minute hang. A real model that does not answer cannot be produced on demand, so that path stays verified against the stub only (task `62f3a126`, first turn). The ordinary real task above finished in under 3 s, so the 2-minute request bound did not get in the way.

### 2. Pi Durable: what Nori already has, and the gaps on the slice's path

Read: Pi at `682c7b9` (`packages/durable`, marked experimental): `docs/spec.md`, `src/harness/tool.ts`, `src/harness/types.ts`, `src/tool-search/tool-search.ts`. Compared with the plan's rows "Durable tool registration", "Durable execution" and "Tool discovery", with `core/README.md` (which cites Pi at `6e2222a` for task ownership intent, held completion, commit publication, inbox order, wake demand and context compaction, but says little about tool-level intent or replay policy), and with the code, which has both.

| Pi | Nori before this turn | Gap on the slice's path? |
| --- | --- | --- |
| **Tool intent before execution.** The `execute` checkpoint (final arguments, replay policy) is committed before `execute()` runs. | `task.durableTool` admits the operation with its receipt deadline and writes an operation record (outcome `pending`, tool reference and digest, action digest, summary and spec, account, authority) before dispatch; `runExternal` saves the attempt count before each provider call; the arguments are in the Temporal activity input. | Adopted, except that the record did not hold the whole prepared action (the provider revision it was approved against, and its provider keys). See gap A. |
| **Declared replay safety.** `replay: "safe" \| "unsafe"` per tool, default unsafe, stored with the intent; both the stored and the current declaration must say safe to re-run. | Each definition declares `retry`: `safe` (only reads and control may), `provider_key` or `reconcile_only`, pinned with the call; an adapter can implement `Reconcile`. An interrupted write is reconciled from provider state or settles `uncertain`, and holds the task's later writes until the user answers. Slice tools: calendar reads `safe`; create `provider_key` (client-chosen event id); change and cancel `reconcile_only`; the reminder create `provider_key` (request id). | Richer than Pi. One gap: the first-party adapter behind reminders (slice step 4) has a provider key but no reconciliation, so an interrupted reminder save stayed `uncertain` and asked the user, although Nori can read its own store. See gap B. |
| **Recovery from committed state.** Reopening in the intent phase re-runs a safe tool with the stored arguments, else records "interrupted and may have partially run"; restart limit 5. | Waits out the dead attempt's receipt window, reuses a settled result, reconciles, else `uncertain`; Temporal allows three attempts; the model step is bounded by D-053. | **Gap A:** recovery prepared the action again from a fresh provider read instead of the saved intent. For `calendar_event_update` (slice step 3, the move) that read already shows the interrupted PATCH's new revision, so reconciliation compared the event with its own new revision and settled a move that had gone through as failed, "the interrupted attempt did not change the event". An event an interrupted cancel had deleted could not be prepared at all, so that settled `uncertain`. Also, a reconciled "did not happen" settled `failed` with no reason it was harmless, which holds every later write in the task until the user answers. |
| **Deferred tool visibility.** `deferred` tools stay out of the request until `tool_search` loads them; the loaded set is stored per branch. | Exposure `discoverable` plus `capability_load`; the loaded set is read from the transcript, so it survives restarts and compaction; the catalog lists each capability and its tools. Google's tools are `direct`, but the inventory offers only definitions whose access the account granted. | Not a gap on the slice's path. With the calendar grant the model is offered the 10 Google Calendar tools, which steps 2 and 3 use; mail tools appear only once mail access is granted (step 5). Making them discoverable would add a `capability_load` round trip to every calendar task. Unchanged. |

Left out deliberately: Pi's scheduler, task graph, structured concurrency, code mode, sessions and storage backends (Nori has Temporal and its database for these). Also left out: re-running a `provider_key` write on recovery in general. Pi would re-run a tool declared safe, but Drive's create declares `provider_key` while its Execute does not deduplicate, so a general re-run could create a second file. The calendar create's reconciliation already reads the event by its own id, which is what a re-run would establish.

### 3. What was implemented (gaps A and B, nothing else from Pi)

PENDING_IMPL

## Second assignment (2026-11-10, coordination record "After the first turn")

| Commit | What |
| --- | --- |
| `11fdcfd` | `.env.example` names `GOOGLE_CALENDAR_CLIENT_ID`, `GOOGLE_CALENDAR_CLIENT_SECRET` and `NORI_OAUTH_REDIRECT_URL`, one line each on what they are for. |
| `11fdcfd` | Event-create summaries name the calendar as the user sees it: "your main calendar" for the primary, otherwise `the "<title>" calendar` from the account's calendar list (one extra read of the list at prepare; if it fails, the id is kept). |
| `11fdcfd` | Draft summaries are never empty: a reply's subject is read from the original message the same way saving the draft resolves it ("Re: …"); a missing subject reads "(no subject)" and no recipients reads "with no recipients yet". The draft result carries the subject the draft was saved with. |

How verified (build `be-11fdcfd` on my stack; the loopback model stub drove each task with one tool call; Google answered from integration's hand-written recordings with `NORI_PROVIDER_REPLAY` and the made-up recorded client; user A connected through the recorded OAuth callback, then repaired with `code=recorded-mail-code` for drafting). This is evidence about Nori's text, not about Google:

| Tool call | Operation summary (from `GET /v1/operations`) |
| --- | --- |
| `calendar_event_create` on `team.recorded@group.calendar.google.com` | `Create "Planning" on the "Team" calendar, Mon 16 Nov 2026 10:00–10:30 PST` |
| `calendar_event_create` on `primary` | `Create "Planning" on your main calendar, Mon 16 Nov 2026 10:00–10:30 PST` |
| `mail_draft_create` replying to `18f0reservation1`, no subject, no `to` | `Draft "Re: Reservation confirmed: Friday, Nov 13 at 7:30 PM" with no recipients yet (not sent)` |
| `mail_draft_create` to one address, no subject | `Draft (no subject) to friend@example.com (not sent)` |

Notes:

- A direct request does not raise an approval (`NORI_USER_SENDS=allow`, interim), so on the stack the summary showed on the operation, which Work displays; an approval shows the same text.
- The action spec (the digest input) still holds the arguments as given, so a reply's spec has `subject: ""`. That is deliberate: the digest binds to what the model asked for.
- The Team create came back uncertain because the recordings have no insert for that calendar (it is read-only there); the task asks the user what happened, as designed. Not a defect.
- **Found, not fixed (outside this assignment):** a reply draft is not addressed from the original message. `saveDraft` writes only the `to`/`cc`/`bcc` it is given, so a reply without them is saved with no recipients. The summary now says so instead of hiding it. Filling reply recipients from the original's From/To/Cc (reply, reply_all) would be the fix; it is a change to what gets written to Austin's mailbox, so I'd like the coordinator's go-ahead first.

## What changed in the first turn

| Commit | What |
| --- | --- |
| `2403234`, `8e7203f` | Merged integration (`e290c5f`, then its later head) twice; both clean. |
| `55ac03e`, `2ed6bdb` | **D-053** model step bounded, readable outcome (details below). |
| `a500fe9`, `77ba67b` | **D-054** prompts follow the time zone saved in Preferences (details below). |
| `2ed6bdb` | **r2.11 conformance**: the repair sign-in payload no longer carries `reason`; a client-sent `{kind: "signed_in"}` answers 400 `validation_failed`. |
| `72befe2`, `f5f990d` | `NORI_MODEL_TEST_URL` (local only, http loopback, refused unless `NORI_CLOUD=local`) sends every model request to a stub; `scripts/backend-stack.sh` holds this stack's restart commands and no secret. |
| `941b5ec` | HEY re-checked from public sources and this machine; see `backend-providers.md`. |

Nothing was pushed to production, nothing was signed in to, no token value was read, no external message was sent.

### Trade-in snapshots: nothing lost

Compared file by file against the branch (each line the snapshot added, searched in the branch's version of that file; then the whole file diffed):

| Snapshot | File | Absent from the branch? |
| --- | --- | --- |
| `…-a4fbb33a39` (`77a5e9d` on `d6ed945`) | `core/cmd/computer-agent/main.go`, `core/go.mod`, `core/go.sum`, `core/internal/connectors/plugin/plugin.go` | No: identical to the branch. |
| | `core/internal/plugins/plugins.go`, `core/internal/computerd/cgroup_other.go` | No: every added line is present; the files differ only by later edits in `840555e`/`a62a26e`. |
| | `core/internal/computer/rpc/protocol.go` | No: `Reopen bool \`json:"run_reopen_v1"\`` is present, gofmt-aligned. |
| | `core/internal/computerd/server.go` | No: the health line is present with the later `Sessions` field added. |
| | `core/internal/computerd/sandbox.go` | No: the egress checks and proxy start are present, refactored into `sandboxError` in `840555e`/`a62a26e`. The snapshot is an earlier state of `840555e`. |
| `…-a4fbb33a39-latest` (`1ff70c4` on `840555e`) | `core/internal/computer/computer.go`, `core/internal/computer/rpc/protocol.go`, `core/internal/computerd/sandbox.go`, `core/internal/computerd/server.go` | No: byte-identical to `a62a26e`. The snapshot is the uncommitted state of the WIP commit. |

Nothing was brought over. Both remote branches are left in place.

### D-053: model step bounded, readable outcome

Cause, from the code: `ModelRef` runs with up to three activity attempts of 240 s each. A context-size retry could start a second 2-minute request inside the same 240 s, so the request ran into the activity deadline, the checkpoint save had no time left ("The model result could not be saved"), Temporal then timed the activity out, the recovery attempt waited out the dead attempt's window and recorded "interrupted", and the workflow asked again, twice. Three passes of about 4 minutes plus recovery waits make the 15 minutes frontend saw. (The exact path of task `29a69f42` is not provable here: its history is on the old laptop.)

Fix:

- Activity side (no workflow change): one model request ends by the earlier of 2 minutes and the activity deadline less 15 s. A context-size retry shares the remainder. The checkpoint save always has time, so a slow model no longer turns into an activity timeout and recovery loop.
- Workflow side: an interrupted request (worker stop) is asked again only while one normal pass (2 min request + 15 s save) still fits within 9 minutes of the step's start. A hanging request is stopped after 2 minutes and not asked again; a request cut off by a crash is recovered and asked once more, so that step ends within about 9 minutes; only a second crash during the re-ask can take longer, and it is not asked again.
- What the user reads (task `error.message`):
  - timeout: "The model did not answer within 2 minutes, so the task stopped. This step changed nothing; ask again to retry."
  - cut off by a stop: "The model request was cut off before its answer was saved (Nori restarted or the request ran out of time), so the task stopped. This step changed nothing; ask again to retry."
  - save failed: "Nori could not store the model's answer, so the task stopped. This step changed nothing; ask again to retry."

Replay compatibility:

- The activity-side change and the new messages change no workflow command. Activity results are read from history on replay.
- The re-ask gate calls `workflow.GetVersion(ctx, "model-step-deadline", DefaultVersion, 1)` only when a request came back interrupted, so a history without an interruption has the same command sequence as before (checked: the plain timeout run `62f3a126` has no new marker). A new history with an interruption gets one `MarkerRecorded` before the re-ask (checked on `c834cecb`). A history recorded before this change has no marker, `GetVersion` returns `DefaultVersion`, and the old unconditional re-ask path replays.
- `go run ./cmd/contracts-check`: 5 pinned histories under `contracts/assistant/histories` replayed, 0 failures. None of them contains an interrupted model step, so they exercise only the unchanged path. An old-code interrupted history was recorded and replayed separately (below): 0 failures, and the negative control fails.

Verified on my stack (`be-72befe2`, `be-77ba67b`, `be-2ed6bdb`; core 8081; stub on 127.0.0.1:18990 that never answers task requests):

| Run | Task | Result |
| --- | --- | --- |
| Model hangs | `62f3a126` | One task request at 08:01:03; failed 2:00.1 after start with the timeout message. No second request. |
| Model hangs, worker SIGKILLed 21 s in, restarted (first rule, 6 min) | `954a2252` | Recovered; failed at 5:21 with the "cut off" message; no re-ask (past that rule's window). This run is why the window was widened: integration had verified recovery-then-re-ask for a SIGKILL. |
| Same, final rule | `c834cecb` | Request 08:11:03, kill 08:11:24, recovery, one re-ask at 08:15:03 (4:00 in), stopped at 2 minutes; failed at 6:00.2 with the timeout message. History: 2 `ModelRef` schedules, the `model-step-deadline` marker. |
| Task left on a build whose worker I had stopped | `9182fe08` | Drained by a worker started under the old build id; failed at 7:21 with the "cut off" message, no re-ask. |
| Old-code interrupted history replayed on new code | `6b43c723` | See "Replay of an old interrupted history". |

### Replay of an old interrupted history

- Recorded with the pre-fix workflow code (`2403234` plus only the test-URL commit, in a scratch worktree, build `be-old2403234`): request 08:19:39, worker SIGKILLed 08:19:59, restarted, recovery, re-ask scheduled 08:23:39. Exported after the re-ask: 36 events, 2 `ModelRef` schedules, no `model-step-deadline` marker. Then terminated on the local stack.
- `contracts-check -root <copy of contracts/assistant with that history added>` on the new code: 6 histories replayed, 0 failures.
- Negative control: with the gate temporarily changed to "never re-ask" and no version check, the same run reports 1 failure (that history). So the version gate is what keeps it replaying. The file was restored afterwards.
- The history is in the shared notes folder as `old-code-interrupted-history.json` for integration to pin if wanted; it is not committed here (`contracts/**` is integration's).

### D-054: time zone follows Preferences

Cause: the user facts in every model prompt (outer conversation, side chats, task worker) and the outer clock were built from the user row once, when the process opened the user, and the task catalog line "Today is … in <zone>" used the worker's zone. Only tool summaries read `Preferences.Timezone`. A change in Settings never reached the prompts.

Fix: `platform.Profile` reads `Preferences.Timezone` at each request (user row as fallback) for the outer advisory context and clock, side chats, the task prompt and the catalog date line. Preferences never saved now default to the user row's zone instead of UTC, so summaries and prompts agree before the first save.

Verified on my stack: user A was created with Europe/London. Before the change of preference, the outer prompt said `"time_zone":"Europe/London"` and "Tuesday, November 10, 2026 16:01 GMT (Europe/London)". After `PUT /v1/preferences` to America/Los_Angeles, with no restart: the next outer prompt said `"time_zone":"America/Los_Angeles"` and "08:03 PST (America/Los_Angeles)"; on build `be-77ba67b` the task prompt said `"time_zone":"America/Los_Angeles"` and "Today is Tuesday, 10 November 2026 in America/Los_Angeles", and contained no "London" at all. (Read from the request bodies the stub recorded.) The only "London" left in the outer prompt is a static example phrase in `prompts/conversation.txt` ("4 p.m. London time"), which is not the user's zone.

### r2.11 as published (`7f7387c`) against what backend serves

Matches, after `2ed6bdb`:

- `SignInPayload`: oauth repair sends `auth`, `auth_attempt_id`, `authorize_url`, `expires_at`, optional `requested_scopes`, plus `integration_id`, `connection_id`, `return_to`; app-password repair sends `auth: app_password`, `integration_id`, `connection_id`, `return_to`; MCP sign-in sends `integration_id: mcp` and the server id as `connection_id`. Before `2ed6bdb` the repair payload also carried `reason` (the health code), which the published schema forbids. Frontend found the same thing. Dropped; the health code is still on the Connection.
- Resolutions: `decline` from the client resolves a sign-in ("Not now"); `signed_in` is set by the server from the connection or MCP callback. Before `2ed6bdb` the HTTP respond route accepted a client-sent `signed_in`; it now answers 400 `validation_failed`, as r2.11 requires. The Nori MCP answer tool only offers answer, approve and decline.
- Backend acknowledges r2.11.

## How to run this stack

`scripts/backend-stack.sh` (`setup`, `users`, `core`, `worker`, `current`). With `NORI_MODEL_TEST_URL=http://127.0.0.1:<port>` it runs against a model stub with placeholder keys and no hosted telemetry. `NORI_BUILD_ID` overrides the build id only to drain tasks pinned to an older build. The stub used this turn is a 40-line Python server in the session scratchpad (records request bodies, answers from scripted JSON, or never answers); it is not in the repository.

## Unverified

- Anything against a real model: this machine's `.env` has no model key, so no real model turn ran. The prompts and timeouts were checked against the stub only.
- The `Interrupted` re-ask after a graceful stop (SIGTERM during a deploy) rather than SIGKILL; the code path is the same recovery read.
- Side-chat prompts after a time zone change: same `Profile` wiring, not exercised on the stack.
- The first-run task `29a69f42`'s exact 15-minute path (history not on this machine).

## Parked, and whether it threatens the first real Google run

- `a62a26e` (WIP hosted stdio, unverified) is on this branch below this turn's commits, so merging the branch brings it. It only changes packaged stdio MCP servers (`command_ref: package:…`) and needs the computer; the Google path does not touch it. Label it unverified when merged (rule 3), or tell me to revert it on this branch.
- W8 packages, D-045, D-047, D-048, D-049: not on the Google path. D-045 (a crashed nested read inside `compose` waits about 4 minutes) only matters if the model uses `compose`.
- **Likely to hurt the real run (not parked by me, found by frontend; next for backend):**
  - The calendar approval summary names the calendar by its raw id (`Create "…" on primary`); on Austin's dedicated test calendar that is a long `…@group.calendar.google.com` id. It should use the calendar's name (`connectors/google/calendar.go` `prepareInsert`).
  - The draft summary can be empty (`Draft "" to  (not sent)`).
- Worker restarts strand running tasks: running workflows stay pinned to the build that started them, so stopping a worker for a new build leaves its tasks "running" until a worker for that build polls again (seen this turn, drained with `NORI_BUILD_ID`). During the real run, keep the old worker up until its tasks finish.

## Needs

### From integration

- Merge `2ed6bdb` and the commits before it (or this branch's head; see the `a62a26e` note above). Nothing in `contracts/**` changed.
- Optional: `NORI_MODEL_TEST_URL` lets a stack run model turns against a loopback stub without keys; `NORI_STACK_NO_MODEL` could use it instead of a dead proxy if model-turn plumbing is worth checking.
- If wanted as replay evidence: the interrupted-then-re-asked history from the old code (see "Replay of an old interrupted history") can be pinned under `contracts/assistant/histories`; it is `old-code-interrupted-history.json` in the shared notes folder.

### From frontend

- `reason` is gone from the sign-in payload (`2ed6bdb`).
- D-053's messages arrive as the task's `error.message` with `code: task_failed`, `retryable: false`. Showing a "Try again" that starts the same request would match the text ("ask again to retry").

### From the coordinator

- D-053 and D-054 are fixed and locally verified (stub), ready to be re-tested in journeys once a model key exists.

### From Austin

- A model key in `.env` (`OPENAI_API_KEY` and/or `OPENROUTER_API_KEY`), and the Google OAuth client values, before any real run; the coordinator has recorded this.
- HEY: whether to run the read-only CLI check in `backend-providers.md` ("The cheapest honest check"). Nothing on HEY is built.
