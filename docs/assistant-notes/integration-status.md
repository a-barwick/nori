# Integration status

Branch `claude/nori-assistant-integration-bd1278`, source of draft PR a-barwick/nori#165. Updated 2026-11-10, eighth turn of the restart (3a and 5 on `cf76ae6`). Assignments are in the coordination record on `claude/nori-assistant-integration-25ae45`. The first run's version of this note is in git history.

**Provider verified: 0 of 44.**

## Re-run of 3a and 5 on `real-cf76ae6`, test account (2026-11-10, 21:41 to 21:56 PST)

Build: merge head `cf76ae6` (backend `c658376`/`257f316`: a repeat sign-in merges into the existing connection, and disconnect never revokes a shared grant; frontend `e3644ba`: no reload loop when the service goes away). Models are on; one worker per build; `NORI_USER_SENDS=ask`. The three existing connections for the test account were **not** collapsed: backend's fix stops new duplicates, and per the coordinator nobody here disconnects them. All writes below used `conn_295df0b9ca785025bbd0`.

| Step | Task (finished) | Result |
| --- | --- | --- |
| 3a. Create "Nori test" calendar | `b9006062` (21:46:25) | **PASS**: `calendar_create`, then a calendar-list read-back, with no sign-in card (the newer connection holds `calendar.create`). It first refused to start, pointing to the old waiting task `f793fa0e`. That task was pinned to `real-cfc0d6f`, whose worker had been stopped. "Not now" and then "Stop task" left it in **Stopping** for good, because no worker serves its build. It took an explicit "start a new task" to proceed. |
| 3. Event on "Nori test": create | `43149b9b` (21:48:22) | **PASS** after two FAILs: the account question, then "I passed along November 12 … which date?" (the date defect persists) |
| 3. Move | `3f869c44` (21:50:00) | **PASS**, with an `event_get` read-back. Nori's reply said it could not verify the new time from "shortened details", although its own read-back ran. |
| 3. Delete the event | `e91cce56` (21:51:08) | **PASS** |
| 3. Delete the "Nori test" calendar | `ee2e66e7` (21:51:56, incomplete) | **FAIL: no tool to delete calendars.** The empty "Nori test" calendar remains on the test account; Google Calendar → Settings → Nori test → Remove calendar. |
| 5a. Find a message | `06451b1a` (21:53:06) | **PASS** after the account question |
| 5b. Send to own address | `5d25bbe6` (waiting) | **PASS up to the card**: straight to "Send this?" with From and To (the account only), the full text and "cannot be recalled", with no account question this time. **Left pending, not approved** (see below). |
| 5c. Send to another address | `16eb5617` (21:55:03, incomplete) | **PASS** for the limit: no send operation and nothing sent. **FAIL**: the account question again, and an unsent draft left behind. |

Still failing on this build:
- **Account question on most Google tasks**, from the three connections. The chooser shows two identical "Google" entries, and "Use this account next time" did not stick: it was ticked at 21:50 and asked again at 21:51.
- **The date defect**: "tomorrow" passed on as November 12.
- **Drafts left behind** after a refused send.
- **No calendar delete.**
- **Tasks pinned to a stopped build cannot be stopped.** `f793fa0e` stays in Stopping. Before any restart, finish or stop tasks; or keep the old build's worker until they drain.
- **Web 3213 reload loop came back** after this restart, even with `e3644ba`: about 900 loads of `/` and of `/apps/google?connection=conn_295df…&result=connected` in three minutes. The second is the sign-in callback page, probably open in the browser Austin signed in with. A web-only restart cleared it again. For frontend: the loop also happens when a tab sits on the callback page while the dev server restarts.

**On approving the own-address send:** the coordinator relayed Austin's standing words ("Just do this shit", "use whatever you need") and said integration may approve own-address sends on the test account. Integration takes send approvals only from Austin directly, in its own session, one send at a time, because a relayed approval is not his. He approved two sends there earlier tonight (both done). This card is left pending (it expires in 24 hours) for Austin to approve or decline himself, or to tell integration directly.


## Full slice pass on `real-cfc0d6f`, test account (2026-11-10, 21:27 to 21:38 PST)

Build: merge head `cfc0d6f` (backend `f14264b`: sign-in instead of giving up, relative times, `calendar_create`). No frontend fix for the reload loop had been pushed. Models are on; one worker per build; `NORI_USER_SENDS=ask` on the real stack. Recorded stack: 12 of 12 scenarios, 0 failures. Austin was away. Reads and calendar writes ran; the own-address send was taken to its approval card and **declined**, because sending email needs Austin's own approval each time and earlier approvals covered single sends.

| Step | Task (finished) | Result |
| --- | --- | --- |
| 2. Week, with sources | `0c9079c2` (21:28:38) | **PASS**: calendar and mail read with sources; `drive_search` fails (Drive API disabled) |
| 3a. Create a "Nori test" calendar | `f793fa0e` (waiting) | **BLOCKED on Austin**: the task raised a sign-in card ("Create new calendars", `calendar.create`) instead of giving up. That is R-1 working. The card is left pending; nobody but Austin may sign in. |
| 3b. Create event, primary calendar | `b0a285d8` (21:30:08) | **PASS**: no date question this time |
| 3c. Move it | `002c3181` (21:30:54) | **PASS**, with a read-back |
| 3d. Delete the event | `da45bb31` (21:31:26) | **PASS**, with a read-back. The calendar delete was not reached (3a blocked). |
| 4. "Remind me in 3 minutes" | `c3ddff6f` (21:31:51), fired as `f880afd3` (21:34:03) | **PASS**: no clock-time question (R-2 fixed); set for 9:34 and delivered in the app at 21:34. App closed: **not exercised**. |
| 5a. Find a message | `d9b23044` (21:33:37) | **PASS** after a **FAIL**: it first asked which Gmail account to search (see "duplicate connection" below) |
| 5b. Send to own address with the approval card | `eca719f8` (21:36:20, incomplete) | **PASS for the card, two FAILs before it**. It first asked which account to use for `mail.draft`. Then it addressed "my own address" to the Nori profile address (`real-a@nori.local`). Backend's own-address guard blocked that and saved a draft; the task asked whether to use the Gmail address instead. After "yes", the card showed To and From (the account), the full text and "cannot be recalled". **Declined**, so nothing was sent; a draft remains. |
| 5c. Send to another address (`someone@example.com`) | `20b71f1d` (21:37:38, incomplete) | **PASS** for the limit: no send operation and no approval card, nothing sent. **FAIL**: the account question again, and an unsent draft left. Nori's reply also said the tool result "confirms only the saved draft—not the reason sending failed". |

New problems found in this pass (for backend and frontend; not fixed here):
- **Duplicate Google connections.** At 21:28:58 a second connection for the same Gmail account, `conn_c3ec41c528ad5973b454`, was created and signed in through the web app (`POST /api/v1/connections`, then a connect action), with `calendar.create` included. It was not this session. Since then, mail tasks ask which account to use, and the chooser lists the same address three times. The original `conn_376569c546155488accd` was chosen each time. Someone needs to say which connection is meant to stay.
- **"My own address" resolves to the Nori profile address**, not the connected Gmail address.
- **Drafts are left behind** whenever a send is refused or declined. The test account now holds unsent drafts to `someone@example.com` (twice) and to itself.
- The 3a sign-in card is waiting in Work for Austin.



## Sixth turn: restart on `5ef24d5` and re-run (2026-11-10, 21:05 to 21:25)

- **Stacks restarted on merge head `5ef24d5`**, with models on and `NORI_STACK_NO_MODEL` unset. One worker per build: `real-5ef24d5` (pid 68899, child 68971) and `int-5ef24d5` (pid 68901, child 68972), both set current. The real stack carries `NORI_USER_SENDS=ask` from `~/.nori-stacks/real.env`, a machine-local, mode-600 file, as a coordinator's setting for this stack; it is not a D2 decision. Recorded scenarios: 12 of 12, 0 failures.
- **Web 3213 went into a hot-reload loop** after the restart: about 6,800 page loads in 10 minutes, "[Fast Refresh] rebuilding" over and over, and the page blank. No file in the worktree was changing. Restarting only the web process cleared it. Cause unknown; frontend may want to know (`next dev` at 3213, started while the browser pane was hidden).
- **Re-run, approved by Austin directly in this session.** Details are in `docs/assistant-acceptance.md` → "Re-run on the merged build".
  - Step 2 passes.
  - Step 3 (create, move, delete) passes. The needless "11 or 12 November?" question is still there on the new build.
  - The own-address send stopped at an approval card with recipient and full text; I approved it and it sent (`0a41ef0e…`).
  - A send to `someone@example.com` was refused before any approval, and nothing was sent (`95a5a7e8…`). **But it left an unsent draft to that address in the test account's Drafts**, because the draft is created before the recipient check. That is for backend. I did not delete it.
- Step 4 with the app closed: still not exercised.

## Fifth turn: slice writes on the test account (2026-11-10, about 20:57 to 21:03)

- **Approval.** The coordinator relayed Austin's "Just set this up". Before any write, I asked Austin directly in this session. He approved three things: the calendar event steps, one email to the account itself, and allowing notifications. The connection already had `calendar.write`, `mail.draft` and `mail.send` from his own Google consent. I started no sign-in.
- **Step 3: passes as writes, with gaps.** Details are in `docs/assistant-acceptance.md` → "Real provider runs".
  - "Nori test" could not be created: there is no create-calendar tool (`ca2260d9…`). The primary calendar of the test account was used, per the coordinator.
  - Create, move and delete each ran once against Google and were confirmed (`c478478f…`, `0a93f9a7…` with a read-back, `e898a019…`). The run ends with the event removed.
  - The create task first asked whether "tomorrow" meant 11 or 12 November, though it knew today is the 10th (frontend's B3).
- **Step 5's send: passes, with a failure.** One note went to the account's own address only (`b7dab195…`). There was no approval card.
- **Step 4 with the app closed: blocked in the built-in browser pane** ("Notifications are blocked for this site"). It needs a real browser or a phone where Austin allows notifications.
- **Merged after the run**: coordinator `bf004ba`, backend `cb97cc8` and frontend `c7dffa0`. Backend's `7c58cc1` limits Gmail sends to the connected account's own address and drops `gmail.send`; its `0c199b7`/`25480a6` change recovery. Frontend's `c7dffa0`/`65eb487` cover the connect choice and the send approval block. Merge head `d9e0b03`. `make check` and `go test ./...` are green. **The stacks still run `1456cc9`**, so none of this was in the runs above. Restart the real stack on the new head (one worker, `set-current`) before the next run.
- **No approval card for any write. This is for the coordinator and Austin, not settled here.**
  - Calendar writes the user asks for directly are authorised by the request itself (`core/internal/permissions/rules.go`: `user_request` for `reversible_write`).
  - Sends go out without approval because the interim D2 setting `NORI_USER_SENDS` defaults to `allow` (`core/internal/settings/settings.go:269`).
  - The slice says each write happens "after an approval he clicks". I did not change the setting: D2 is open with Austin. If the slice rule stands, the real stack could set `NORI_USER_SENDS` to require approval for the run, but that is the coordinator's or Austin's call.

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

**Update after the reads (coordinator record `8626371`):** the coordinator started one combined access request (calendar.write, mail.draft, mail.send) on `conn_376569c546155488accd`, and Austin approved Google's consent himself. So the grant item below is done for the test account; integration has not yet observed it in a run. Steps 3 and 5's send were **not** run this turn: my assignment for this turn was reads only. They wait for the coordinator's go-ahead to me, and step 3 also needs a "Nori test" calendar, which the test account did not have at 19:58.

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
