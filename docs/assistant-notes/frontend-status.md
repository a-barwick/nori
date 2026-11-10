# Frontend status (nori-frontend)

Updated 2026-11-10, first turn after the restart. Branch `claude/nori-assistant-frontend-78149f`, with integration `54c6b91` merged in. The first run's version of this note is in git history (`ea53ff9` and earlier).

**Provider verified: 0.** Nothing in this note ran against a real provider. "Recorded" means the hand-written recordings in `contracts/assistant/recordings`; they show screens and Nori's flow, not Google. "Fixture" means `?fixtures=1` in the web, which is display only.

## What ran

- **Branch.** Switched the worktree to `claude/nori-assistant-frontend-78149f` and deleted the scratch branch. Merged integration twice, at `e290c5f` (fast-forward) and at `54c6b91`.
- **Stack** (core 8082, web 3212, database `nori_frontend`, namespace `nori-frontend`, queue `nori-tasks-frontend`, replay on).
  - Restart commands: `docs/assistant-notes/frontend-stack.sh`. It holds no secret and reads `.env` through `NORI_ENV_FILE`. Logs, the dev vault and artifacts go to `$TMPDIR/nori-frontend-stack`.
  - Test user: `frontend.test@example.com`, user `1e815713…`, America/Los_Angeles. It is not a real address.
  - Worker build `fe-e290c5f`, set current with the `temporal` CLI inside the Temporal container.
  - **This machine's `.env` has no model key** (`OPENAI_API_KEY` and `OPENROUTER_API_KEY` are both empty; I checked names only). The stack runs with `NORI_FE_NO_MODEL=1` placeholders, so screens load but every chat reply and task fails at the model call. Chat shows "I couldn't write a conversation reply: model provider returned HTTP 401." Integration's `scripts/local-stacks/stack.sh` (`NORI_STACK_NO_MODEL=1`) also sends outbound traffic to a dead proxy, so it is the better pattern. My script does not do that.
- **Checks on the merged tree:** `bun install`, `bun run contracts:check` (types current at r2.11), `tsc --noEmit` and `eslint src`. All pass.

## r2.11: accepted as published

Frontend accepts r2.11 as published at `7f7387c`. It is open with Austin, so this acknowledgement is frontend's and is not final.

- The card needed exactly what r2.11 gives:
  - `auth` says whether to open a URL or show the app-password form;
  - `integration_id: "mcp"` with the server id says to sign the server in, not a connection;
  - `signed_in` lets a resolved card say what happened;
  - `{kind: "decline"}` without a digest matches a card that has no action to digest.
- Adopted in `ca49e42`. The sign-in card reads `payload.auth`; for `mcp` it reads `/v1/mcp/{id}` and, when the link has expired, asks for a fresh one through `/v1/mcp/{id}/actions` `sign_in`. A resolved card shows "Signed in", or "Not now" for a declined sign-in. Not now sends `{kind: "decline"}`. Fixture check: the Google and iCloud cards render, and iCloud's Sign in opens the app-password form.
- **Not objections, for a later revision:**
  - The MCP `sign_in` action takes no `return_to`, so a card that refreshes an expired MCP link returns to the server's page, not the task. That is a route on `/v1/mcp`, not part of r2.11.
  - **Backend emits a field r2.11 forbids:** `core/internal/task/external.go` `signIn` puts `reason` (the health code) into the sign-in payload, and `SignInPayload` has `additionalProperties: false`. The web ignores `reason`. Smallest change: backend drops `reason`, or a revision adds it.

## Journeys 3 to 8 (rehearsal on recordings): not run

Blocked: no model key on this machine (see What ran). Every journey needs model turns. Integration's recording fixes (`d2e9e61`, `aa5f301`) are on my branch and unexercised by me. The first run's results stay in `journeys-frontend.md`.

| # | Journey | Result |
| --- | --- | --- |
| 3 | Move lunch with Priya | not run (no model key) |
| 4 | Accept the design review | not run (no model key) |
| 5 | Standup instance and series | not run (no model key) |
| 6 | Allow send, find the reservation, reply and send | Allow path not re-run; the task steps were not run (no model key) |
| 7 | Reminder that follows a moved event | not run (no model key) |
| 8 | Repair mid-task on account B | not run (no model key) |

## Austin's path: walked up to Google's page

Walked on my recorded stack with a made-up Google client. Clicking Connect reached accounts.google.com, which showed `invalid_client` as expected. No account was chosen and nothing was signed in. To see the screens after a connection, I sent the recorded code to the callback in place of Google's redirect. Fixed in `web/` (`666aeff`, `74217bb`):

1. **Before connecting, the page looked as if Connect grants everything.** Every capability had a green check, but Connect asks Google only for the read scopes (core `scopesFor`). Now only the Read items are checked before the first account, with a line saying changes and sends need a second Google sign-in.
2. **An abandoned sign-in looked like an account.** After backing out of Google's page, the connection showed as "Google · Connecting" with "Add another account". It is now treated as unfinished and reused by the next Connect.
3. **"Connected." next to "Needs more access".** If Google grants less than was asked (its consent screen lets Austin untick items), the return banner now says so, in the warning tone.
4. **Approval for a send ran the message into the header.** A send's summary is `Send "subject" from … to …:` followed by the body on new lines; the card collapsed it into one line. The lines are now kept.
5. **The more-access sign-in didn't say what it asks for.** When a task needs a capability that hasn't been granted, core raises a sign-in ("Give Nori more access to … so it can continue this task.") with `requested_scopes`. The card now names the capability, for example "Google will ask you to allow: Create and change events, invitations and replies."

Points that need words to Austin, not code (for `austin-real-accounts-steps.md`):

- Connect asks for calendar, Gmail, contacts and Drive **read** access in one consent. Gmail and Drive read are restricted scopes, so with an unverified OAuth client Google is likely to show "Google hasn't verified this app", and Austin must be listed as a test user. I have not seen this screen; it is an expectation.
- The first calendar write or send asks him to sign in to Google again from a card in Work → Needs you, or from Apps → Google → account details → Allow. The task waits and resumes by itself afterwards.
- Without push or email delivery configured, nothing tells him a card is waiting. He has to look at the Needs you badge in the sidebar and header.
- **Finding the result afterwards (unverified here):** Work → Finished → the task, which shows the result and its actions. Also the task card in the chat.

Not verified here, because each needs a running task: the approval card's live text for a calendar write and a send, the more-access card raised by a real task, and the result screens.

## Needs

### From Austin (through the coordinator)

- A model key in `.env`: `OPENAI_API_KEY`, or `OPENROUTER_API_KEY` for the configured task model. Without it no journey or task runs on any stack.
- The Google OAuth client (`GOOGLE_CALENDAR_CLIENT_ID`, `GOOGLE_CALENDAR_CLIENT_SECRET`) for the real-provider stack. Integration lists these too.

### From backend

- Drop `reason` from the sign-in payload in `task/external.go` `signIn`, or propose adding it to the contract (r2.11 forbids it).
- The calendar approval summary names the calendar by its raw id (`Create "…" on primary, …`). On Austin's dedicated test calendar this will be a long `…@group.calendar.google.com` id. It should use the calendar's name. Seen in `connectors/google/calendar.go` `prepareInsert`; no task id, because no task ran.
- Still open from the first run: D-053 (model call with no time limit), "Europe/London" in task text when Preferences say America/Los_Angeles, and the empty draft summary (`Draft "" to  (not sent)`). Not re-checked this turn.
- A chat whose first reply fails shows the failure above the user's first message (same minute). Seen once on 8082 with the placeholder key; not investigated.

### From integration

- Nothing blocking. Once a model key exists I re-run journeys 3 to 8 on my stack, or on `scripts/local-stacks/stack.sh recorded` if that is preferred.

### From the coordinator

- Record frontend's acknowledgement of r2.11 (above) in the Contract revisions table. It stays open with Austin.
- Decide whether frontend should switch to `scripts/local-stacks/stack.sh` with a third stack entry instead of keeping `frontend-stack.sh`.
