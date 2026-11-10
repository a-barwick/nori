# Backend status (restart of 2026-11-10)

Branch `claude/nori-assistant-backend-9c520e`, worked from worktree `.claude/worktrees/backend-restart-26132e` on the new machine. The first run's version of this note is at `origin/claude/nori-assistant-integration-25ae45:docs/assistant-notes/backend-status.md`. This file is the current state.

Evidence label (rule 2): **provider verified: 0.** Everything below is locally verified against a loopback model stub, the local database and the local Temporal dev server. Nothing here ran against a real model or a real provider account.

## What changed this turn

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
