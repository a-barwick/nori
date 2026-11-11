# Backend status (restart of 2026-11-10)

Branch `claude/nori-assistant-backend-9c520e`, worked from worktree `.claude/worktrees/backend-restart-26132e` on the new machine. The first run's version of this note is at `origin/claude/nori-assistant-integration-25ae45:docs/assistant-notes/backend-status.md`. This file is the current state.

Evidence label (rule 2): **provider verified: 0.** No real provider account was used. The D-053 and D-054 re-tests in the third assignment ran against the real models (OpenAI and OpenRouter, keys from `.env`); everything else is locally verified against a loopback model stub, Google answering from hand-written recordings, the local database and the local Temporal dev server.

## Duplicate Google connection for one account (real stack defect)

Commit `257f316`.

**Cause.** "Add another account" creates a new connection, and its callback stored whatever account signed in. Choosing the account that was already connected therefore left two connected records for the same provider subject (`conn_376569c546155488accd` and `conn_c3ec41c528ad5973b454` on the real stack).

**Fix:**
- **Merge.** When a first sign-in on a new connection reaches an account the user already has live (same integration, same provider subject, not disconnected), the existing connection is updated instead. It gets the newest token (keeping its refresh token if Google sent none), the union of granted and desired scopes, recomputed capabilities and a new authorization epoch, with the health message "This account was already connected; its access was updated." The new, never-signed-in connection is deleted in the same write. The callback returns to the existing connection with `result=connected`, and tasks waiting on either connection resume.
- **Contract.** r2.11 allows only `connected|cancelled|failed`, so I did not add a distinct `already_connected` result. If frontend wants to show that plainly, it is a small contract revision (coordinator's call). The health message carries the text meanwhile.
- **Disconnect never revokes a shared grant.** Disconnecting a connection now skips the provider revoke while another live connection holds the same account. Google revokes per account and client, so revoking one duplicate would also cut off the one that stays. Only Nori's copy (credential, cache) is removed then.

Verified on my stack (recorded Google, core `257f316`):

| Case | Result |
| --- | --- |
| User B, `recorded.work@example.com` already connected (`conn_f191657c1166529bb7a1`): new connection `conn_fdd1c0a5895e59c9becb`, connect (calendar.read and mail.draft), callback for the same account | Redirected to `/apps/google?connection=conn_f191657c1166529bb7a1&result=connected`. The new connection answers 404, and one connection remains with the union and "This account was already connected; some requested access was not granted." (my recorded token lacks drafts, which is correct for what was granted). |
| User C: two connections for `recorded.user@example.com` made with the previous build (`97c9c1d`, scratch worktree), as on the real stack; then on `257f316`, disconnect `conn_2e3585633a6f5be997e5` through the app's disconnect action | Disconnected; **no revoke request** in the core log; the remaining `conn_5a2b8344ee545e51be6f` still reads its resources (200). |
| Control: then disconnect that last connection | One `POST oauth2.googleapis.com/revoke`, as before. |

**Real stack: not cleaned up yet, deliberately.**
- The real core (8085) runs integration's `cfc0d6f`, which does not have `257f316`. Its disconnect revokes the token at Google. That would most likely also invalidate the connection we keep, and it is a write to Austin's Google account.
- The one to keep is `conn_c3ec41c528ad5973b454`, the wider grant: everything the other has, plus `calendar.create`. The one to disconnect is `conn_376569c546155488accd`.
- Steps, once 8085 runs a build that includes `257f316`: disconnect `conn_376569c546155488accd` with `POST /v1/connections/conn_376569c546155488accd/actions {"action": "disconnect"}`, or Apps → Disconnect. Then confirm no `oauth2.googleapis.com/revoke` request in the core log, and that the kept connection still lists calendars.
- Read-only check behind this: the two records in `nori_real` (status, capabilities, granted scopes); nothing there was changed.

## R-1, R-2 and creating a calendar (from integration's real runs)

Commits `02989a5`, `97c9c1d`. Verified on my stack with the **real models** (`gpt-6.1-sol`, `openai/gpt-6.1-terra`). Google answered from recordings (a scratch copy of integration's, with a recorded calendar create and an extra token exchange). No real Google account was used, so these are not provider evidence.

### R-1: a task asks to connect instead of giving up

Cause, from the code:
- While an account was only "connecting", the inventory offered none of its tools: only granted definitions were offered. So the model could not reach the executor path that raises the sign-in interaction, and it replied "still connecting".
- A connecting record whose sign-in attempt lapsed stayed "connecting" for good.
- The conversation prompt said "New connectors are not available in this slice", which is where "new connections aren't available here" came from.

Fix:
- **Lapsed attempts.** A record still connecting more than 15 minutes (the attempt's life) after its last write reads as not connected, with health "The last sign-in was not finished." It reads as needs-sign-in if the account had signed in before. This is applied when records are read; nothing is rewritten. Apps and the assistant then show a normal Connect.
- **Ungranted tools are offered.** For an OAuth integration configured in this deployment (Google here), tools whose access is not granted are offered too. With a usable account they appear like its other tools; with none, they appear once the task loads the capability, so unrelated tasks don't carry them. The catalog line says that calling a tool asks the user from the task. Exposure is not part of a definition's digest, so pinning is unchanged.
- **Connect from the task.** Calling such a tool with no usable account opens a `sign_in` interaction ("Connect your Google account so Nori can continue this task.") for exactly the access that call needs. It reuses a record that never finished its first sign-in, or creates one, named by the operation so a retry reuses it. The task resumes when the callback completes (the existing `OnConnected` path).
- **Conversation prompt.** Accounts connect in Apps or from a task's sign-in card; when a task waits on one, the conversation tells the user to use the card. Only apps with no Nori integration cannot be added.

| Case | Task | Result |
| --- | --- | --- |
| User B with only a stale connecting Google record (backdated 20 minutes in my local database): "What does my week look like on my Google calendar?" | `82976bfd-3ec4-5b04-b0cf-6fc29a9c1b2f` | The record read as `not_connected`. The task opened "Connect your Google account so Nori can continue this task." on that same record, asking for `calendar.readonly` only. After the recorded callback the account was connected and the task resumed and read the calendar (6 operations). It ended "incomplete" because account B's recordings have no events in this week and no recorded single-event read; that is my recordings, not the flow. This run was before the prompt fix, and the conversation still said "New connections aren't available here". |
| User C with no Google record: "What's on my Google calendar tomorrow?" (after the prompt fix) | `039c5a95-17ba-53df-b31e-6d9db8978dc2` | A connection was created and the task opened the connect card for `calendar.readonly`. The conversation said "Please use the sign-in card to connect your Google account. Once connected, I can continue checking your calendar." Left waiting. |

Not done: a way to delete a never-connected connection (integration's optional ask). A lapsed one now reads as not connected and is reused by the next connect from a task.

### R-2: relative times resolve from the clock

Cause: the task prompt carried only "Today is <date> in <zone>". The clock was left out so the cached prompt prefix changes at most daily, so the task could not resolve "in 3 minutes". Fix: each task model request now ends with a short runtime note giving the current time in the user's zone, telling it to resolve relative times from it and not to ask. It comes after the saved history, so the cached prefix is unchanged, and it is not saved in the task history.

| Case | Task | Result |
| --- | --- | --- |
| User A (zone Asia/Tokyo), sent 05:12:29 UTC: "Remind me in 3 minutes to stretch." | `20077ed4-d9f1-56eb-a4d3-1df5d4cd02f8` | No question. Reminder `once` at `2026-11-11T14:15:00+09:00` (05:15 UTC, rounded to the minute); the reply said "set for 2:15 PM JST". It fired as automation task `6e4d2999-94fb-59ef-88ce-1e6f6338d989` at 05:15:00 and completed. |

### Creating a calendar

- **Scope.** Google's `calendars.insert` accepts `calendar`, `calendar.app.created` or `calendar.calendars`; `calendar.events` (what `calendar.write` holds) is **not enough**. The narrowest is `calendar.app.created`: make secondary calendars, and manage events on the calendars Nori made.
- **Capability.** A new capability `calendar.create` ("Create new calendars", reversible write) asks for `calendar.readonly` and `calendar.app.created`. `calendar.write` is unchanged.
- **Tool.** New tool `calendar_create` (title, optional description and time zone, default the user's). Before creating, it reads the calendar list and refuses a title the account already has, giving that calendar's id to use instead. So an interrupted create reconciles by title (`reconcile_only`).
- **Events on it.** Adding events to the new calendar uses the existing `calendar_event_create`, under `calendar.write`.
- **On Google's side:** nothing to enable; the Calendar API is already in use. Austin accepts the extra access when a task asks for it (one Google consent), or allows it in Apps. With the app in Testing, a listed test user can grant it.

| Case | Task | Result |
| --- | --- | --- |
| User A (recorded account with `calendar.write`, without `calendar.create`): "Put a 30-minute event called Planning on my Nori test calendar on Thursday, November 12 at 10 am Pacific. If that calendar doesn't exist yet, create it." | `80c720fe-dda3-585c-ad4f-4514fd967bb3` | The real model listed calendars, found no "Nori test" and called `calendar_create`. Nori asked for exactly `calendar.readonly` and `calendar.app.created` on the existing account ("Give Nori more access…"), and the conversation pointed to the card. After the recorded grant: `calendar_create` confirmed ("Create a new calendar "Nori test" (America/Los_Angeles)"), then `calendar_event_create` on it confirmed with a read-back. 4 operations, none failed. The event summary names the calendar by its id only because my recorded calendar list does not include the new calendar; real Google's would. |

## Mailbox decided: Gmail for step 5 (coordination record "Mailbox decided; Pi Durable port already on main")

Commit `7c58cc1`.

**Scopes.** Step 5 uses three Google capabilities: `mail.read` (find one message: `gmail.readonly`), `mail.draft` (write the note as a draft: `gmail.compose`) and `mail.send` (send the draft). Nori only ever sends drafts (`users.drafts.send`), and Google's reference lists `mail.google.com`, `gmail.modify` and `gmail.compose` as the scopes for both `drafts.create` and `drafts.send`; `gmail.send` is not among them. `mail.send` used to ask for `gmail.compose` and `gmail.send`; the second was never used, so it now asks for `gmail.compose` alone. Step 5 therefore needs exactly `gmail.readonly` and `gmail.compose`. A consequence: a grant of drafts now also counts as a grant of sending, because Google's own `gmail.compose` consent already says "Manage drafts and send emails". Every send still needs its approval (see below). The test account's earlier grant (`compose` and `send`) still satisfies both.

**Only to his own address.** New setting `NORI_MAIL_RECIPIENTS`, default `self` (stream's choice, following Austin's limit; `any` lifts it). With `self`, a Gmail send whose To, Cc or Bcc holds any address other than the connected account's own is refused. The own address is the account label, which Nori reads from Google's userinfo when the account connects. It never comes from the model, a setting, code or a committed file. The check runs at prepare, before an approval is shown, and again at the moment of sending. Drafts are not limited, since a draft sends nothing. The comparison ignores case only, so a Gmail alias or a dotted variant of the address counts as someone else.

Verified on my stack (`be-7c58cc1`, `NORI_USER_SENDS=ask`, stub model, Gmail from recordings plus two recorded drafts in a scratch copy, recorded account `recorded.user@example.com`):

| Case | Task | Result |
| --- | --- | --- |
| Send a draft addressed to `friend@example.com` | `780327f3-af0d-5715-add5-488506beb1cc` | Refused at prepare, not admitted, no approval shown, no send request made: "this Nori sends email only to the connected account's own address (recorded.user@example.com); the draft is also addressed to friend@example.com". |
| Send a draft addressed to the account itself | `bb41c07f-509a-59a9-ae3f-be30b340452b` | Approval shown: `Send "Note to self" from recorded.user@example.com to recorded.user@example.com`. After approving it through the API, the send was made (recorded) and the operation is `confirmed`. |

Two earlier runs of the second case (`7fe637c6`, `29d33acc`) failed because of my stub and my recording, not Nori. The stub did not re-issue the call after approval, and the recording's send matched a different draft id.

**Does Google need anything from Austin?**
- **Gmail API:** already enabled on the existing client's project. Integration's real `mail_search` and `mail_read` on his test account succeeded (task `f38b0eb2`), which they could not do with the API disabled.
- **Scopes:** Nori asks for them in each sign-in request, so nothing needs adding for them to be requested. If the app is in Testing, Google lets a listed test user grant `gmail.compose` behind its unverified-app screen (it is a restricted scope).
- **Only Austin can do:** add his **personal** Gmail address as a test user on the OAuth consent screen, if the app is in Testing and it is not listed yet. Then sign in with it and allow mail access. Listing the scopes on the consent screen's data-access page is only needed for verification, which the slice does not need.

**For the run (not changed here):**
- **Step 5's approval needs `NORI_USER_SENDS=ask`** on the real stack. The interim default `allow` sends a directly requested send with no approval click (D2's interim mechanism; Austin can flip it).
- **Step 3 cannot show an approval as the code stands.** A direct request's reversible write (create or move an event) is always allowed: `permissions/rules.go`, the `user` plus `reversible_write` case, with no setting. The slice says "each after an approval he clicks". That is a D2 question for the coordinator and Austin; I have not changed it.
- **Calendar invitations are email to other people.** An event with attendees and `send_updates` other than `none` makes Google email them. `NORI_MAIL_RECIPIENTS` does not cover that; such an event is a `sensitive_send`, so with `ask` it does need approval. Step 3's event has no attendees.
- **Outlook sending is not limited by the setting.** No Microsoft client is configured on this machine.
- **D-056 is not needed for step 5:** the note is a new message to himself, not a reply.

## Pi Durable: starting from the port on main

The port (AB-317 to AB-320, `5f2d841...2dd6126`) is already in this branch's history, so the third assignment's comparison below was made against code that includes it. The port did not touch the external-tool path: `task/external.go`, `connectors/**`, `invoke` and `scheduler` are unchanged in `5f2d841...2dd6126`. Gaps A and B below were therefore still open on the slice's path after it. They are fixed in `0c199b7` and `25480a6`. Nothing else from Pi was taken.

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

| Commit | What |
| --- | --- |
| `0c199b7`, `25480a6` | **Gap A, recovery from the saved intent.** The operation record now saves the whole prepared action before the first attempt: `action.intent`, `action.precondition` (the provider revision the action was approved against) and `action.keys` (its provider idempotency keys), next to the digest, summary and spec it already had. Recovering an interrupted **write** rebuilds the action from that record instead of preparing it again (`task.planExternal`), so reconciliation compares provider state with what was approved. Reads still re-run from a fresh prepare. Records saved before this change have no `intent` and recover as before. |
| `0c199b7` | **Gap A, a proven "did not happen" stops holding the task.** On recovery, which runs only after the interrupted attempt's receipt window, a reconciliation that shows no effect settles `failed` with the reason `did_not_run`, so it no longer holds the task's later writes. Right after a timeout (not recovery) the request may still land, so a `failed` there keeps holding, as before. Google Drive, Outlook and iCloud already set that reason in their own reconciliation; Google Calendar and Gmail did not. |
| `0c199b7` | **Gap B, reminders reconcile from Nori's store.** The first-party adapter now reconciles an interrupted `automation_create`: the automation saved under the call's request id (the save is one transaction) is reported `confirmed`; none saved is `failed` (`did_not_run`). Other first-party writes stay `uncertain`, as before. |

No workflow command changed (all of this runs inside activities), so no version gate is needed. `make -C core check` and `go run ./cmd/contracts-check` (5 histories replayed, 0 failures) pass.

How verified (stub model; Google answering from integration's recordings, copied to the session scratchpad and edited per case; user A's recorded Google connection). The recorded-provider state lives in the worker's memory, so each case started the worker with one recording set (the PATCH hangs for 120 s), SIGKILLed it about 5 s into the PATCH, and restarted it with a second set showing the provider after the cut-off attempt: either the move had landed (the event at 13:00 with revision `…41`) or it had not (12:30, revision `…40`). Recovery ran at the end of the dead attempt's 240 s window.

| Case | Build | Task | Result |
| --- | --- | --- | --- |
| Move landed before the crash | `be-0c199b7` | `a120eacd-2750-5b79-ae6c-29e9872413a2` | Before the restart, the record already held `intent: true` and `precondition: "3400000000000040"`, outcome `pending`, attempt 1. Recovery at 04:02:13: one read of the event, no second PATCH, settled **confirmed** ("Changed"); task completed. |
| Move had not landed | `be-25480a6` | `5f22ae98-a872-5358-aa77-518ade295d5d` | Recovery at 04:08:31: one read (revision `…40`, equal to the saved precondition), no second PATCH, settled **failed** with reason `did_not_run`, so later writes are not held; task completed. |
| Same as the first row, on the code before this turn (negative control) | `be-old-ed59dc5` (scratch worktree at `ed59dc5`) | `e7922a91-1b0b-5f1d-9c54-153e6617fa15` | Recovery at 04:06:49 prepared again (its read already showed revision `…41`), then reconciled against that: settled **failed**, and the model was told "the interrupted attempt did not change the event" although the event had moved. This is the defect gap A fixes. |
| Reminder saved, result lost | `be-25480a6`; before the kill, a scratch build of the same commit with an injected 120 s pause after the save (not committed) | `cae0aa3e-aa3c-51d8-af9f-f70e3728f4db` | Before the restart the automation existed and the operation was `pending`. Recovery at 04:12:51 found it by request id and settled **confirmed**; one automation, no question to the user. The code before this turn has no reconciliation here and settles `uncertain`, which asks the user (read from the code; not re-run). The test reminder was cancelled afterwards. |

One earlier run of the "move had not landed" case (`5e7ecf29`) failed because of my recording edit, not the code: removing the PATCH exchange broke another recorded exchange that depends on it, so the restarted worker's recorded transport would not load. It was re-run with the PATCH exchange kept (above).

Not verified: the delete path (an interrupted cancel whose event is gone now reconciles from the saved spec instead of settling `uncertain`; same code path, no recorded case), and anything against real Google or a real crash in Austin's run.

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
- **Found, not fixed (outside this assignment):** a reply draft is not addressed from the original message. `saveDraft` writes only the `to`/`cc`/`bcc` it is given, so a reply without them is saved with no recipients. The summary now says so instead of hiding it. Filling reply recipients from the original's From/To/Cc (reply, reply_all) would be the fix; it is a change to what gets written to Austin's mailbox. The coordinator holds it as **D-056** (it would send mail to other people; Austin's limit is that email goes only to him) until the first slice and his word. Not changed.

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

`scripts/backend-stack.sh` (`setup`, `users`, `core`, `worker`, `current`). With `NORI_MODEL_TEST_URL=http://127.0.0.1:<port>` it runs against a model stub with placeholder keys and no hosted telemetry. `NORI_BUILD_ID` overrides the build id only to drain tasks pinned to an older build. The stub is a small Python server in the session scratchpad (records request bodies, answers from scripted JSON, or never answers); it is not in the repository. The third assignment's interrupted-write cases used a scratchpad script that starts the worker with one recording set, kills it during the provider call, and restarts it with another; also not in the repository.

State left running: core 8081 and a worker on `be-97c9c1d`, with the real models, recorded Google responses (a scratch copy with a calendar create and an extra token exchange) and `NORI_USER_SENDS=ask`. User C's connect task `039c5a95` is left waiting on its card (user A's Google connection is a recorded one, so a real-model worker must not run tasks for user A against real Google). Real-model mode is `scripts/backend-stack.sh core|worker` without `NORI_MODEL_TEST_URL`.

## Unverified

- D-053's 2-minute hang against a real model (cannot be produced on demand; stub only). D-053's crash recovery and D-054 were re-tested against the real models in the third assignment.
- Gaps A and B against real Google and a real worker crash in Austin's run: verified with recorded Google responses and an injected pause only (third assignment, section 3).
- The `Interrupted` re-ask after a graceful stop (SIGTERM during a deploy) rather than SIGKILL; the code path is the same recovery read.
- Side-chat prompts after a time zone change: same `Profile` wiring, not exercised on the stack.
- The first-run task `29a69f42`'s exact 15-minute path (history not on this machine).

## Parked, and whether it threatens the first real Google run

- `a62a26e` (WIP hosted stdio, unverified) is on this branch below this turn's commits, so merging the branch brings it. It only changes packaged stdio MCP servers (`command_ref: package:…`) and needs the computer; the Google path does not touch it. Label it unverified when merged (rule 3), or tell me to revert it on this branch.
- W8 packages, D-045, D-047, D-048, D-049: not on the Google path. D-045 (a crashed nested read inside `compose` waits about 4 minutes) only matters if the model uses `compose`.
- The two summaries frontend found (calendar named by raw id, empty draft summary) were fixed in the second assignment (`11fdcfd`).
- Worker restarts strand running tasks: running workflows stay pinned to the build that started them, so stopping a worker for a new build leaves its tasks "running" until a worker for that build polls again (seen this turn, drained with `NORI_BUILD_ID`). During the real run, keep the old worker up until its tasks finish.

## Needs

### From integration

- Merge this branch's head (third assignment: `0c199b7`, `25480a6` and the status commit). Nothing in `contracts/**` changed. The new operation-record fields (`action.intent`, `action.precondition`, `action.keys`) are internal and optional; records without them recover as before.
- Optional: `NORI_MODEL_TEST_URL` lets a stack run model turns against a loopback stub without keys; `NORI_STACK_NO_MODEL` could use it instead of a dead proxy if model-turn plumbing is worth checking.
- If wanted as replay evidence: the interrupted-then-re-asked history from the old code (see "Replay of an old interrupted history") can be pinned under `contracts/assistant/histories`; it is `old-code-interrupted-history.json` in the shared notes folder.

### From frontend

- `reason` is gone from the sign-in payload (`2ed6bdb`).
- D-053's messages arrive as the task's `error.message` with `code: task_failed`, `retryable: false`. Showing a "Try again" that starts the same request would match the text ("ask again to retry").

### From the coordinator

- D-053 and D-054 re-tested against the real models (task ids in the third assignment, section 1).
- Pi Durable gap list and the two gaps implemented (sections 2 and 3). Nothing else from Pi was taken. D-056 stays held.

### From Austin

- HEY: whether to run the read-only CLI check in `backend-providers.md` ("The cheapest honest check"). Nothing on HEY is built.
