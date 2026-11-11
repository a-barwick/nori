# Frontend status (nori-frontend)

Updated 2026-11-10, third turn after the restart ("Slice and third assignments"). Branch `claude/nori-assistant-frontend-78149f`, with integration `ed59dc5` and coordinator `1ad4f85` merged in. Earlier versions of this note are in git history (`f7ed24a`, `ea53ff9`).

**Provider verified: 0.** Nothing here touched a real Google account; nobody signed in. This turn ran **real models** (OpenAI `gpt-6.1-sol` for chat, OpenRouter for tasks) and **real providers with no recordings**, on my own stack. Without a Google sign-in, the steps stop where the grant is missing, as recorded below.

## What ran

- **Stack.** `docs/assistant-notes/frontend-stack.sh` now defaults to real models and real providers (replay off, Google client from `.env`). `NORI_FE_RECORDED=1` brings back the recordings. A new `stop` command stops only this stack's processes, found by namespace and by ports 8082/3212, never by program name.
  - Core 8082, web 3212, database `nori_frontend`, namespace `nori-frontend`, worker build `fe-a4a225a` set current.
  - Fresh test user `frontend.real@example.com` (`40ed700f…`, America/Los_Angeles) with no recorded connections, chosen with `NORI_FE_EMAIL`. Not a real address.
  - No VAPID pair on this stack, so push shows "Push notifications aren't set up on this Nori". Integration's 3213 has one.
  - Integration's and backend's stacks were not touched.
- **Walk.** Steps 1 to 4 of the first slice, in the built-in browser at 1280×800 and at phone size (375×812). Times are America/Los_Angeles, 2026-11-10.
- **Checks** on the merged tree: `contracts:check` (current), `tsc --noEmit`, `eslint src`. All pass.

## Slice walk, steps 1 to 4 (real models, no recordings, no sign-in)

| Step | What I did | Where it stopped | Evidence |
| --- | --- | --- | --- |
| 1 Connect Google | Apps → Google → Connect | Google's real sign-in page ("Sign in to continue to Nori"). The client and the `127.0.0.1:3212` redirect were accepted, and the read scopes were requested. I backed out without typing anything. | 19:49 |
| 2 "What does my week look like?" | Asked before connecting | Task answered correctly ("connect Google … in Apps"), but **the chat reply said new calendar connections "aren't available here"** and offered an .ics upload. Backend B1. | task `001d3a48-5643-51b4-aa89-f8a3ffd5da93`, 19:47–19:48 |
| 3 Create, then move, an event on "Nori test" | Asked at phone width | No write was attempted. The reply said **"Google Calendar is still connecting"** (my abandoned Connect, B2), and the chat **passed "Tomorrow is November 12"** when it was Nov 10 (B3). The task caught the date itself. Approval cards not reached. | task `ae4f6258-7678-50f5-a67a-ce165bfa98a0`, 19:50 |
| 4 Reminder | "Remind me in 4 minutes that it's time to leave for the station" | Reminder `auto_7d3c85d1…` set for 19:55:41, run at 19:55:43, and in the in-app inbox at 19:55:47 and the chat at 19:55:49. **It can only go to the in-app inbox** (B4), so "delivered with the app closed" cannot pass as built. The chat also showed "I've saved your answer." for a question the user never saw (B5). | occurrence `occ_cc7cf5c9…`, task `e2f2fac8-e512-5343-9c66-ea87c4ecaa69` |

Acknowledgement is not slow: my message shows 17 ms after Return, "Nori is thinking… Details" right after, the "I've accepted your task" line about 7 s after sending, and the answer at about 16 s (step 2).

## Fixes in `web/` (before and after)

1. **Reminder destination said "Nowhere".**
   - Before: a reminder made in chat showed "Sends to: Nowhere; results stay in Library". It did reach the in-app inbox, and nothing said it would not reach Austin with the app closed.
   - After: "Nori inbox only (Settings → Notifications), so nothing reaches you while Nori is closed. Choose push or email under Edit.", with both links. Edit already offers the destinations. (`RoutinesScreen.tsx`)
2. **A reminder that had run looked on and cancelled at once.**
   - Before: after it fired, the page showed the pill "On", the line "Cancelled.", and Run now, Pause, Snooze and Cancel automation.
   - After: pill "Done", line "This one-time automation has run. It won't run again.", with no Pause or Snooze. The chat card says "Done" too. Core keeps such a reminder `enabled` with no `next_at`; the web reads that as done. (`RoutinesScreen.tsx`, `CardView.tsx`)
3. **The step-3 capability was cut off on a phone.**
   - Before: Apps → Google at 375 px showed "Create and change events, i…".
   - After: capability names wrap. Other rows still truncate long titles. (`Row` `wrapTitle`, `AppsScreen.tsx`)

Checked and fine, so no change: Connect shows "Opening sign-in…" and reaches Google in about 2–3 s. Backing out of Google's page leaves "No Google account yet" (last turn's fix holds on the real path). The Scheduled tab lists the reminder with "in 3 minutes". The chat at phone width has no overflow.

## Not reachable without Austin's Google sign-in

- What Apps shows on return from Google: the connected banner, the partial-grant warning, the account row.
- Step 2 with sources from his real calendar.
- Step 3's more-access card (the first calendar write asks for a second Google sign-in), both approval cards (create, then move), their live summaries naming the "Nori test" calendar, the event's removal at the end, and how the result reads in chat and in Work → Finished.
- Step 4 tied to an event ("before I leave"), and push delivery with the app closed. That needs push enrolled in Settings → Notifications on 3213, and also B4.

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

## Needs

### From backend (found this turn, real models)

- **B1, step 2 dead end.** `core/internal/prompts/conversation.txt:25` says "New connectors are not available in this slice. Explain that limit when relevant." It dates from `7c49b68` and is now false: Google connects from Apps. The chat model followed it and told the user connections "aren't available here" even though the task said to connect in Apps. Smallest change: delete the line, or replace it with "When an app isn't connected, tell the user they can connect it in Apps."
- **B2, abandoned Connect reads as "still connecting".** `connections.Service.Inventory` reports a `connecting` record with no `account.provider_id` as "Needs attention in Apps: (connecting)", so the model says "Google Calendar is still connecting". Apps shows "No Google account yet" for the same record. Smallest change: treat a connecting record with no provider account as "Not connected. The user can connect it in Apps."
- **B3, wrong date passed to a task.** At 19:50 PST on Nov 10, the chat gave the task "Tomorrow is November 12, 2026" (task `ae4f6258…`), although the time context it had was "Tuesday, November 10, 2026, 19:51 PST" (quoted by the chat itself in command `4645dff4…`). The prompt says to pass dates in the user's own words; the model added a resolved date anyway. This is related to D-054.
- **B4, step 4 cannot deliver with the app closed.** Reminders made in chat get `destination_ids: []`, which goes only to the in-app inbox. `Preferences.default_destination_ids` is stored but never applied, and no task tool takes a destination. Smallest change: when an automation is created without destinations, use the user's default destinations, plus the in-app inbox.
- **B5, "I've saved your answer."** appears in the chat when the task asked a question (here, the current time) and the chat answered it itself (command `4645dff4…`). The user never saw a question. Smallest change: skip the `answer-accepted` message when the answer did not come from the user.
- Still open from the first turn: `reason` in the sign-in payload (r2.11 forbids it).

### From integration

- Nothing blocking. On 3213, Austin needs push enrolled in Settings → Notifications before step 4 can be judged, and B4 before it can pass.

### From Austin (through the coordinator)

- The Google sign-in for steps 1 to 4. After it, I re-walk the screens listed under "Not reachable".

### From the coordinator

- Record frontend's r2.11 acknowledgement (unchanged from the last turn).
- B1 to B5 are backend's files; I did not edit them. If the coordinator wants frontend to take B1 (a one-line prompt change), say so.
