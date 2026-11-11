# Integration status

Branch `claude/nori-assistant-integration-bd1278`, source of draft PR a-barwick/nori#165. Updated 2026-11-10, fourth turn of the restart (slice rehearsal). Assignments are in the coordination record on `claude/nori-assistant-integration-25ae45`. The first run's version of this note is in git history.

**Provider verified: 0 of 44.**

## Fourth turn: slice rehearsal (2026-11-10, evening)

- **Merged** the coordinator head `1ad4f85` (Austin's intent, the first slice, the third assignments, Codex's environment handoff). Backend and frontend had nothing new beyond what is already here. Merge head `1e206bf`. `make check` and `go test ./...` in core are green. No code differs from `1456cc9`, so the stacks were **not** restarted.
- **Processes checked before anything else.** There is exactly one model-enabled worker per build:
  - `int-1456cc9` on `nori-integration` (pid 52918, child 52991);
  - `real-1456cc9` on `nori-real` (pid 52921, child 52989).

  Neither these workers nor the cores (52990 on 8085, 52992 on 8083, started 08:52) carry `NORI_STACK_NO_MODEL` or the dead proxy. The no-model duplicates Codex stopped are gone. Both builds are current. Other sessions' workers (`nori-frontend`, `nori-backend`) were left alone.
- **Model calls are real.** The worker logs `openrouter` / `openai/gpt-6.1-terra` and the core logs `openai` / `gpt-6.1-sol`.

### Where each slice step stops (real stack, web 3213, signed in as `real-a@nori.local`, no Google grant)

Driven in the real app as a person would, in the existing chat. Nothing was signed in to and no token was handled. No account was written to; with no grant, none could be.

| Step | What I asked | Task, time (PST) | Where it stops |
| --- | --- | --- | --- |
| 1. Connect Google | not attempted (Austin's sign-in) | n/a | Apps lists Google under **Needs attention** as **Connecting**. That is the probe connection left by Codex's authorize-URL check (and my earlier `conn_376569c546155488accd`). It has no grant. |
| 2. "What does my week look like" | "What does my week look like?" | `209d093d-dbf6-5f0e-8ccc-a51235023056`, incomplete 19:47:36 | **Stops before any calendar read.** Reply: it could not review November 9–15 "because Google Calendar is still connecting", Outlook and iCloud are not connected, and "new calendar connections aren't available here right now", with an offer to summarise an uploaded .ics. **No sign-in card was raised.** Work lists it as Failed. |
| 3. Create, then move, an event on Nori test | "Create an event called Nori rehearsal tomorrow at 3pm for 30 minutes on my Nori test calendar." | `51b3dfbc-a2f6-5add-975c-8480b9f4ed29`, incomplete 19:48:18 | **Stops before the approval.** Reply: "The event was not created because Google Calendar is still connecting". It restated the request correctly (Wed 11 Nov, 3:00–3:30 p.m., Nori test) and pointed to Apps. No approval card and no sign-in card. The move and the removal were not reachable. |
| 4. "Remind me before I leave", delivered with the app closed | "Remind me in 3 minutes to leave for my appointment." | `e891e310-9dcb-5bb6-9e6c-732eac743660`, completed 19:49:21 | **Asked for an absolute time** ("the reminder task couldn't determine the current time"); I answered "7:54 PM Pacific today". The automation "Leave for your appointment" (once, 19:54) fired as task `f5690ffd-43a5-5658-8b95-f1f9c3eb8495`, completed at 19:54:03. "⏰ Time to leave for your appointment!" appeared in the chat and the Nori inbox. **Stops at in-app delivery.** No push destination exists ("Notify this device" is off), so "delivered with the app closed" is not exercised. |

### Step 5 rehearsal (before the sign-in; Austin chose Gmail, `c15cd04`)

| Step | What I asked | Task, time | Where it stops |
| --- | --- | --- | --- |
| 5, find | "Find the most recent reservation email in my Gmail." | `fed43de1-cd70-5c27-b0b6-ae60391126ab`, incomplete 19:51:07 | "Couldn't search your Gmail because its connection is still in progress". No sign-in offered (R-1). |
| 5, send to self | "Email me a one-line note from my Gmail saying the Nori rehearsal reached the send step." (no address given) | `5e0c9ff8-bb9c-5592-91b9-4453856fcdb9`, incomplete 19:51:37 | "The note was not sent because Gmail is still connecting". No approval card reached. |

### After Austin's sign-in (test Gmail account, read-only scopes): reads run

Recorded in full in `docs/assistant-acceptance.md` under "Real provider runs".
- Step 2: `b4a79d12-17f5-5716-ad0b-8b4fad2786e2` **passed**. It read the real calendar list and events with sources; the week is empty and there is one calendar. `drive_search` **failed** because the Drive API is disabled in the Google Cloud project that owns the client.
- Step 5's read: `f38b0eb2-33b1-5559-ac01-0b4a4d19be8d` **passed** (`mail_search`, `mail_read`).
- Calendar list: `43c0e7a3-8e28-5f8e-a396-33790d15b08a`. Only the primary calendar exists; **no "Nori test" calendar**.

No write was asked for. Provider verified stays 0: this is a test account, and the slice names his personal Gmail.

### What the write steps need

From `core/internal/connectors/google/google.go` and `core/internal/connections/service.go` at `1456cc9`, and the Apps page:

| Write | Capability | Google scopes | Requested by Connect? |
| --- | --- | --- | --- |
| Step 3: create, move, then delete an event on Nori test | `calendar.write` | `calendar.readonly` + `calendar.events` | **No.** Connect requests only the capabilities whose effect is `read` (`scopesFor` with no capabilities given). The Apps page says so: "Connect asks Google only for the Read items." |
| Step 5: send one note to his own address | `mail.send` | `gmail.compose` + `gmail.send` | **No**, same reason. (`mail.draft` is `gmail.compose` alone, if a draft comes first.) |

**How they get granted.** There are two routes; both are a further Google consent by Austin, which nobody else can do:
1. **Ahead of time:** in Apps → Google → the account, press **Allow** next to "Create and change events, invitations and replies" and next to "Send email". Each starts a sign-in for that capability. The authorize URL carries `include_granted_scopes=true`, so the read scopes are kept.
2. **When the task needs it:** a write tool on a connection lacking the capability gets `ErrInsufficientScope`. The task then raises a sign-in interaction for exactly that capability (`LackingCapability` → `signIn` in `core/internal/task/external.go`) and resumes when it completes. This path is exercised only on hand-written recordings (A03); it has not run against Google.

The write itself then still needs the approval Austin clicks (calendar writes are `reversible_write`, send is `sensitive_send`).

**What Austin must do before steps 3 and 5's send:**
- Create a **"Nori test"** calendar on the account used for the run. The test account has none.
- Grant `calendar.write` and `mail.send` by one of the routes above. With the app in Testing, the account must be a test user. Gmail send is a restricted scope, so Google shows the unverified-app screen again.
- For provider verified, do the same on his **personal** Gmail account (his choice for the slice). The test account's runs do not count for that.
- Optional: enable the **Google Drive API** in the project that owns the client, so "my week" stops reporting a Drive failure. Drive is not in the slice.
- For step 4 with the app closed: allow notifications ("Notify this device") on the device he wants reminders on.

Integration will not grant any of this itself: each item is a sign-in or a change in his Google account.

### Findings from the rehearsal (for backend and frontend; not fixed here)

- **R-1 (backend).** A connection stuck in `connecting` (an authorize URL that was opened and never completed) makes calendar tasks give up with "still connecting". They do not raise the r2.11 sign-in interaction that would let Austin finish connecting from the chat. The reply also claims new calendar connections "aren't available here", which is wrong: Apps offers Connect. Austin's step 1 is in Apps, so this does not block the run, but it is a dead end for anyone who starts in chat.
- **R-2 (backend).** The reminder task said it "couldn't determine the current time" and asked for an absolute time in Pacific. A relative reminder ("in 3 minutes", or "before I leave") should not need that. Answering "7:54 PM Pacific today" let it schedule the reminder.
- **R-3 (frontend/backend).** Steps 2 and 3 both show as **Failed** in Work, with "Incomplete" text, when the cause is a missing grant. An actionable "needs Google" state would read better. This is for frontend's usability walk to judge.
- **Push.** Settings → Notifications shows only the Nori inbox (On), with "Notify this device" off. I did not enable push in the browser pane: that grants a browser permission and sends through an outside push service. "Delivered with the app closed" therefore remains unexercised. It needs a device subscription on the device Austin uses, which is part of his run.

## Third turn (2026-11-10)

- Merged the coordinator head `b7ca838` (second restart turn recorded; D-056 reply recipients, held) and backend's three commits `11fdcfd`, `7aa061f` and `ae7ac07`: Google path summaries (calendars by name, drafts never empty), and `.env.example` now names `GOOGLE_CALENDAR_CLIENT_ID`, `GOOGLE_CALENDAR_CLIENT_SECRET` and `NORI_OAUTH_REDIRECT_URL`. Merge head `a62e88c`, merged cleanly.
- `make check` and `go test ./...` in core are green at `a62e88c`. Contracts are still r2.11 (223 fixtures); the r2.11 row is single and carries both acknowledgements.
- Keys are unchanged: `OPENROUTER_API_KEY` is empty and the Google client is absent from `.env`.
- **My stacks were stopped from outside at about 08:27.** Both cores, both workers and web 3213 got SIGTERM, which I did not send. Other sessions' processes (8081, 8082, 3212, and the `nori-backend` and `nori-frontend` workers) stayed up. Whoever stops processes on this machine: please use `scripts/local-stacks/stack.sh <stack> stop` or stop by port, not by program name.
- I restored both stacks on `1456cc9`, still in no-model mode: workers `int-1456cc9` and `real-1456cc9` are current, **12 of 12 live scenarios pass** on 8083, and web 3213 answers. The summary fixes are loaded but not exercised: with no model, nothing produces a summary.

## Second turn (2026-11-10)

- **Merged**:
  - the coordinator branch at `a9e1d8b`;
  - frontend head `f7ed24a` (r2.11 adopted at `ca49e42`, Google connect copy, approval message lines);
  - backend head `bd5a7d6` (D-053, D-054, HEY note, `backend-stack.sh`).

  All three merged cleanly. Merge head `a8781a3`, then `031b475` for the r2.11 row.
- **Backend's hosted stdio WIP `a62a26e` is merged, labelled unverified.** It came with the backend head. `make check` and `go test ./...` in core both stayed green at `a8781a3`, so it stays in. Per backend, it affects only packaged stdio MCP servers. Nothing in it has been exercised here, and A10 stays blocked.
- **Backend's two summary fixes** were not on `origin/claude/nori-assistant-backend-9c520e` at either fetch this turn, so they are not merged yet.
- **r2.11 row updated**: backend acknowledged at `2ed6bdb`, frontend accepted and adopted at `ca49e42`, still open with Austin, and not recorded as accepted.
- **Keys, by name** (re-checked this turn; `.env` unchanged since 07:58):
  - `OPENAI_API_KEY` is **set**.
  - `OPENROUTER_API_KEY` is **still empty**.
  - `GOOGLE_CALENDAR_CLIENT_ID` and `GOOGLE_CALENDAR_CLIENT_SECRET` are **still absent**.

  So the stacks were **not** restarted with models, and the **Google authorize-URL check has not run**. Both wait on those three values. I did not wait in a loop.
- **Stacks moved to `031b475`**, both still in `NORI_STACK_NO_MODEL=1` mode:
  - recorded: core 8083, worker `int-031b475` current, **12 of 12 live scenarios pass**;
  - real: core 8085, worker `real-031b475` current, web 3213 answers.
- **Once the keys are present**, I will:
  1. stop both stacks (`stack.sh <stack> stop`);
  2. start core and worker without `NORI_STACK_NO_MODEL`, then run `set-current`;
  3. re-run the recorded scenarios;
  4. on the real stack, create a Google connection for `real-a`, call `connect`, and load the returned authorize URL without choosing an account, recording what Google shows.

## First turn (2026-11-10)



- **Merges**: the coordinator branch `claude/nori-assistant-integration-25ae45` (docs), frontend `ea53ff9` and backend `840555e` (that commit, not the branch tip; `a62a26e` WIP stays out under rule 1). All merged cleanly; merge head `e290c5f`.
- **Checks on this machine**:
  - `make check` is green. The only fix needed was `bun install` in `web/`; nothing in the repository changed for it.
  - Note that `make check` runs only `go vet` for core, so I also ran `go test ./...` in core: green.
  - Contracts r2.11: 223 fixtures, 90 Go round trips, 12 scenarios, 5 histories.
- **Stack scripts** are committed at `scripts/local-stacks/` (`stack.sh` and a README) and hold no secret.
  - Commands: `setup`, `users`, `core`, `worker`, `set-current`, `web`, `check`, `stop`, `status`.
  - `psql` and the `temporal` CLI run inside the Compose containers, since neither is on this host.
  - Machine-local state lives in `~/.nori-stacks/`.
- **Recorded stack**: core 8083, database `nori_integration`, namespace `nori-integration`, worker `int-e290c5f` set current.
  - Users: `integration-a@`, `integration-b@`, `scenario-a@` and `scenario-b@nori.local`.
  - **12 of 12 live scenarios pass** (`contracts-check -live … -recorded`), twice, the second time after a restart.
  - They ran in `NORI_STACK_NO_MODEL=1` mode: placeholder model keys, all outbound HTTP(S) sent to a dead local proxy. No model turn was exercised, and the scenarios are API-level.
- **Real stack**: core 8085, database `nori_real`, namespace `nori-real`, worker `real-e290c5f` set current.
  - Web 3213 → 8085, signed in as `real-a@nori.local` (user `66666666-…`; no personal address anywhere).
  - Credential store `local`, with a new empty vault at `~/.nori-stacks/real/artifacts/credentials`. Redirect: `http://127.0.0.1:3213/api/v1/connections/oauth/callback`.
  - It answers, also in no-model mode, and advertises `push_public_key`.
  - Starting a Google connection answers `provider_unavailable` ("this integration is not configured in this deployment"), so **the Google authorize-URL check could not run**: there is no Google client on this machine.
  - A not-connected "Google probe" connection (`conn_376569c546155488accd`) remains on `real-a` because I found no action that removes a never-connected connection. It is harmless and can be reused or ignored.
- **VAPID**: a new pair was generated into `~/.nori-stacks/real.env` (mode 600, never printed), which `stack.sh` loads for core and worker. It is not in `.env`, contrary to the assignment, because Austin had `.env` open in vim at the time (pid 33716, swap file present). An edit underneath would have been overwritten by his save. It can move into `.env` if wanted.
- **`.env` gap, by name**, compared with `.env.example` and with what core reads. Full table in `austin-real-accounts-steps.md`.
  - `OPENROUTER_API_KEY`: empty.
  - `OPENAI_API_KEY`: empty at 07:52, set after Austin's 07:58 save.
  - `GOOGLE_CALENDAR_CLIENT_ID`, `GOOGLE_CALENDAR_CLIENT_SECRET`: absent, and also **absent from `.env.example`**, a documentation gap for backend.
  - `NORI_BACKGROUND_TASKS_ENABLED=false` (chat-only); `stack.sh` overrides it.
  - No `NORI_CREDENTIAL_STORE`, `NORI_OAUTH_REDIRECT_URL` or VAPID entries; `stack.sh` sets these.
  - Microsoft and SMTP are absent; they come later.
- **Steps for Austin**: `austin-real-accounts-steps.md` is rewritten for this machine, covering Google only. Order: OpenRouter key, then Google client with the redirect URI and his address as a test user, then a Nori test calendar, then his sign-in. Microsoft and iCloud are marked later.
- **Acceptance record regraded** under rule 2 (`docs/assistant-acceptance.md`):
  - provider verified 0;
  - verified locally with no provider involved 6;
  - exercised on hand-written recordings 2;
  - implemented 33 (12 with parts on recordings, 13 with parts exercised locally, 8 not exercised);
  - blocked 2; not started 1.

  15 rows whose status said verified but whose text listed unexercised parts were lowered, with the reason in bold. Wording that presented the local vault, the interim D1–D4 settings, or "voice set aside" as Austin's decisions was removed. PR #165's description matches; it stays a draft, with no reviews requested and no comments.
- **r2.11** is recorded in the Contract revisions table of `docs/assistant-coordination.md` (one row after r2.10): published, proposed by backend, no acknowledgement recorded from either stream, open with Austin. No new revision was published.

### What failed or went wrong (first turn)

- **I killed other sessions' cores.** At about 07:56 I ran `pkill -f nori-local`, which matches every session's core on this machine. Frontend's core (its web 3212 and worker on `nori-frontend` were up) was very likely killed, and possibly backend's. I messaged both sessions to restart. `stack.sh stop` now stops only its own ports and namespace, and the README warns against stopping by name.
- Both cores refuse to start without `OPENROUTER_API_KEY` even with `OPENAI_API_KEY` set, because background tasks need it for conversation summaries. With only the OpenAI key present, the stacks still run in no-model mode.
- The Google authorize-URL check did not run (no client).
- `.env` holds `PORT=8080`. A worker with `PORT` unset picks that up and enters hosted mode (`NORI_WORKER_SERVICES must be configured`). `stack.sh` exports an empty `PORT` instead.

## Needs from backend

- R-1: a calendar task facing a `connecting` (never completed) Google connection should raise the sign-in interaction rather than give up, and should not say new connections are unavailable.
- R-2: relative reminders should resolve the current time themselves.
- Confirm the write-scope table above (Connect asks for read only; writes come from Allow in Apps or the task's sign-in interaction), and whether the in-task route is expected to work against real Google as built.
- Optional: a way to remove a never-connected connection (disconnect answers "does not apply", `DELETE` is 405).

## Needs from frontend

- R-3, for your usability walk: a missing grant shows as Failed in Work.

## Needs from the coordinator

- Nothing new. The keys are in, and the stacks run with models on `1456cc9`, one worker per build.

## Needs from Austin

- See "What the write steps need" above: a "Nori test" calendar; Allow for calendar writes and send; the same sign-in on his personal Gmail for provider verified; notifications on his device for step 4.
- Open with him: D1 to D4 and r2.11.

## Restart the stacks

Commands are in `scripts/local-stacks/README.md`. `.env` now has both model keys and the Google client: start without `NORI_STACK_NO_MODEL`, and check first that no other worker polls the same build. Logs: `~/.nori-stacks/<stack>/{core,worker,web}.log`.
