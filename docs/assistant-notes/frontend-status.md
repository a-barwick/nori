# Frontend status (nori-frontend)

Updated 2026-11-10, third turn after the restart ("Slice and third assignments"), with step 5 added after Austin chose his personal Gmail for the slice (coordinator `c15cd04`). Branch `claude/nori-assistant-frontend-78149f`, with integration `ed59dc5` and coordinator `1ad4f85` merged in. Earlier versions of this note are in git history (`f7ed24a`, `ea53ff9`).

**Provider verified: 0.** Nothing here touched a real Google account; nobody signed in. This turn ran **real models** (OpenAI `gpt-6.1-sol` for chat, OpenRouter for tasks) and **real providers with no recordings**, on my own stack. Without a Google sign-in, the steps stop where the grant is missing, as recorded below.

## Reload loop, second time (after integration's ~21:56 restart, with `e3644ba` merged)

**What was reported.** About 900 loads in three minutes, of `/` and of `/apps/google?connection=…&result=connected` (the sign-in landing page, open in the browser pane). A web-only restart cleared it (integration `27146cf`).

**Cause: still not proven.**
- **The log was lost.** The intact log the coordinator pointed to is from the web process started after that restart, and it was overwritten again by about 21:57. It began with two `[browser] ⨯ unhandledRejection: ChunkLoadError` lines: a tab left open from before the restart was loading chunk names that the new server did not serve.
- **Not reproduced on my stack** (8082/3212, browser pane hidden as on 3213), with tabs on `/` and on the callback URL:
  - restart core, then web: 1 reload per tab;
  - restart web with changed client code in between: 1 reload per tab;
  - start web on `4867f35`, open the tabs, stop web, switch to the current head, start web: 1 reload per tab;
  - no `ChunkLoadError`, no "Compiled", no loop in any of them.
- **Ruled out:**
  - our code (no reload calls);
  - a leftover `next dev` parent restarting its server (killing the listener ends the parent too);
  - stale browser-cached chunks (the dev server sends `no-cache, must-revalidate`);
  - a hidden pane on its own.
- **Not ruled out:** Turbopack's on-disk dev cache out of step with code merged while the server was down. That fits the `ChunkLoadError`s, the loop following merges, and a second restart (same code) clearing it. Integration's cache is 615 MB under `web/.next/dev/cache/turbopack`.

**What changed now.**
1. **Development-only reload guard** (`lib/dev/reloadGuard.ts`, installed from `instrumentation-client.ts` before Next opens its dev-server socket).
   - It counts full page loads per tab (`sessionStorage`). After 5 within 15 s it holds back the dev-server messages that can reload that tab:
     - `reloadPage`, `serverComponentChanges`, `staticParamsChanged`, `turbopack-message`, `clientChanges`, `serverOnlyChanges`, `middlewareChanges`;
     - on a reconnect, also `turbopack-connected` and `sync`, which reload when the server's id or hash changed.
   - Everything else still flows; holding all of it blanked the page, because React's debug chunks arrive the same way.
   - It raises one uncaught error, which Next forwards to the dev server's terminal. The line starts `[nori dev] <path> loaded N times in 15 s`, followed by the dev-server messages and errors each earlier load saw before it went away. **The next loop will name its own cause in `web.log`.**
   - A reload by hand after 15 s quiet brings live reload back. Not in production builds (`NODE_ENV` check).
   - Verified on 3212:
     - six quick loads paused the tab, and the line appeared in `web.log` as `[browser] Uncaught Error: [nori dev] / loaded 6 times …`;
     - the paused tab still rendered;
     - a web restart did **not** reload it (same `performance.timeOrigin` before and after);
     - a normal load records `turbopack-connected`, two binary chunks, `sync`, `building`, `built`.
2. **The sign-in result is read once.**
   - Before: `?result=connected` stayed in the address bar, so every reload, including Next's reload after a dev-server restart, showed "Connected." again for a sign-in that happened earlier.
   - After: the app page and the MCP server page read `result` and `connection` once, then remove them with `history.replaceState`.
   - Verified: `/apps/google?connection=…&result=connected` shows "Connected." and the address becomes `/apps/google`.
3. **Account chooser.**
   - Before: each entry led with the connection title, so two Google connections read "Google", "Google".
   - After: each entry leads with the account address, then the app name. Two entries with the same address also say when each was connected. An id the list doesn't have reads "An account that is no longer connected" instead of the raw id.
   - Checked by typecheck only: the fixture's account question is already answered, and the real stack now has one Google connection (its label is the Gmail address).

**For integration.** Before restarting web after a merge, clearing `web/.next/dev/cache` would test the cache explanation, at the cost of a slower first compile. If it loops again, the `[nori dev]` line in `web.log` is the evidence to send me.

## Reload loop on 3213, 21:05 to 21:16 (coordinator report)

**What was seen.** Integration's web on 3213 (`next dev`, started 21:04 on the merged head) kept reloading `/`. Integration's core log (since overwritten by the 21:26 restart) showed 1,000 to 1,500 cancelled requests a minute from 21:05:01 to 21:16, mostly `/v1/snapshot` (the "saved work wake repair failed: context canceled" line). That is a page reloading itself about every half second. Core was up the whole time (one start, 21:04:54).

**What I could and could not establish.**
- **Our code never reloads the page.** The only navigations in `web/src` are the two `window.location.assign` calls to a Google sign-in URL.
- **Every reload came from Next's dev client.** It reloads in three cases:
  - the dev server restarted (new session id): once per restart;
  - an HMR update can't be applied;
  - a server change arrives after the page has hit a runtime error, or is showing the root error page (`__next_error__`).
  - The console pattern ("[HMR] connected", then "[Fast Refresh] rebuilding", then a reload) and the "Compiled in N ms" after every `GET /` match the last two: the dev server kept rebuilding, and each rebuild reloaded the page.
- **Not reproduced on my stack** (8082/3212, same frontend code), with one or two tabs open on `/`:
  - core stopped for 40 s, then restarted: 0 page reloads;
  - web stopped for 30 to 45 s, then restarted (alone, and after core): exactly 1 reload each, then steady.
- **Cause not proven.** Integration's 21:04 `web.log` and `.next/dev/logs` were overwritten by later restarts, and no file under `web/` outside `.next` changed after 21:03:54. What made that one dev server rebuild on every request is not established.
  - Two things fit and are not ruled out: something writing into the dev server's project directory (for example a `next build` in the same `web/`), and a stale Turbopack cache from before the 21:03:50 merges.
  - The next web process on the same head has been stable.
- **What turned rebuilds into a loop is a gap on our side:**
  - The app had no route error boundary, so any render error fell through to `global-error`. Once that happens, Next's dev client reloads on every server change.
  - A page opened while core was unreachable got a bare-text 503 and never recovered.
  - An open tab kept "Nori is temporarily unavailable … Refresh history to reconnect" until it was refocused.

**Fixes (before → after).**
1. **Render errors.**
   - Before: a render error anywhere in the app replaced the whole document with the framework error page (`global-error`), and in dev every later rebuild reloaded the page.
   - After: `app/error.tsx` and `app/(nori)/error.tsx` (one `ScreenError` component) show "This screen didn't load. Your work is safe." with **Try again** and **Open chat**, and report the error to Sentry. They never retry by themselves.
   - The error stays in a route boundary, so it doesn't reach Next's root error state, which is what triggers reload-on-rebuild.
   - Not exercised with a real render error, since none occurred.
2. **Opening a screen while the service is down.**
   - Before: `proxy.ts` returned the text "Nori is temporarily unavailable. Try again shortly." with a 503 and nothing else, and the user had to reload by hand.
   - After: document requests (`Sec-Fetch-Dest: document`, not RSC) get a small self-contained page, "Reconnecting to Nori … This page opens by itself when it is back", with a **Try now** button (`lib/runtime/reconnecting.ts`).
   - It checks the same URL with `HEAD` after 2, 4, 8 … up to 30 s. It reloads **once**, when that check answers 200.
   - The wait is kept in `sessionStorage` for 2 minutes, so a flapping service can't cause a storm. A 429 uses `Retry-After`.
   - 403 and API calls are unchanged.
   - Verified: `/apps` opened with core down showed the page (dark mode). After core came back: one `HEAD /apps 200`, then one `GET /apps`, and the screen loaded.
3. **The chat during an outage.**
   - Before: a red alert "Nori is temporarily unavailable. Try again shortly. Refresh history to reconnect", and the header said "Disconnected". It only cleared when the tab was refocused or went back online.
   - After: a status line "Can't reach Nori right now. Reconnecting on its own… **Try now**", and the header says "Reconnecting…".
   - When the event stream reopens after a failed snapshot, the chat reads the snapshot once and the line clears by itself.
   - Verified both ways with the tab in front and no reload (navigation type stayed `navigate`): stream-only outage, and snapshot failure during the outage (forced with a `visibilitychange`).
   - The messages are shared constants (`lib/runtime/messages.ts`) used by `auth.ts`, `useRuntime.ts`, `NoriApp.tsx` and `AppShell.tsx`.

**Production build.** `bun run build` passes.
- The client bundle has no HMR socket (`_next/hmr`) and no Fast Refresh code.
- Its one `location.reload()` is the router's popstate handler for history entries left by the old pages router. Nothing in production reloads on a server restart, a new build or an unreachable service.
- `next start` on a spare port with no hosted runtime configured served the reconnecting page for `/` (503); the API answered 403.
- Local sign-in is development-only (`localDevelopment` requires `NODE_ENV=development`), so the signed-in app was not driven on a production build.

**For integration (suggestion, not done by me).** Restart `web` after merging into its worktree, and run `next build` or checks that write `web/.next` from a separate checkout, never in the `web/` that serves 3213 or 3214. Keep each dev server's log (append, or a timestamped file) so the next incident can be traced.

## What ran

- **Stack.** `docs/assistant-notes/frontend-stack.sh` now defaults to real models and real providers (replay off, Google client from `.env`). `NORI_FE_RECORDED=1` brings back the recordings. A new `stop` command stops only this stack's processes, found by namespace and by ports 8082/3212, never by program name.
  - Core 8082, web 3212, database `nori_frontend`, namespace `nori-frontend`, worker build `fe-a4a225a` set current.
  - Fresh test user `frontend.real@example.com` (`40ed700f…`, America/Los_Angeles) with no recorded connections, chosen with `NORI_FE_EMAIL`. Not a real address.
  - No VAPID pair on this stack, so push shows "Push notifications aren't set up on this Nori". Integration's 3213 has one.
  - Integration's and backend's stacks were not touched.
- **Walk.** Steps 1 to 4 of the first slice, in the built-in browser at 1280×800 and at phone size (375×812). Times are America/Los_Angeles, 2026-11-10.
- **Checks** on the merged tree: `contracts:check` (current), `tsc --noEmit`, `eslint src`. All pass.

## Slice walk, steps 1 to 5 (real models, no recordings, no sign-in)

| Step | What I did | Where it stopped | Evidence |
| --- | --- | --- | --- |
| 1 Connect Google | Apps → Google → Connect | Google's real sign-in page ("Sign in to continue to Nori"). The client and the `127.0.0.1:3212` redirect were accepted, and the read scopes were requested. I backed out without typing anything. | 19:49 |
| 2 "What does my week look like?" | Asked before connecting | Task answered correctly ("connect Google … in Apps"), but **the chat reply said new calendar connections "aren't available here"** and offered an .ics upload. Backend B1. | task `001d3a48-5643-51b4-aa89-f8a3ffd5da93`, 19:47–19:48 |
| 3 Create, then move, an event on "Nori test" | Asked at phone width | No write was attempted. The reply said **"Google Calendar is still connecting"** (my abandoned Connect, B2), and the chat **passed "Tomorrow is November 12"** when it was Nov 10 (B3). The task caught the date itself. Approval cards not reached. | task `ae4f6258-7678-50f5-a67a-ce165bfa98a0`, 19:50 |
| 4 Reminder | "Remind me in 4 minutes that it's time to leave for the station" | Reminder `auto_7d3c85d1…` set for 19:55:41, run at 19:55:43, and in the in-app inbox at 19:55:47 and the chat at 19:55:49. **It can only go to the in-app inbox** (B4), so "delivered with the app closed" cannot pass as built. The chat also showed "I've saved your answer." for a question the user never saw (B5). | occurrence `occ_cc7cf5c9…`, task `e2f2fac8-e512-5343-9c66-ea87c4ecaa69` |
| 5 Find one message, send a note to his own address | "Find my most recent reservation email and send me a short note with its details." | No Gmail read or send. The chat said "Gmail is still connecting" and "New connections aren't available here" (B1, B2). The task then asked in Needs you **"Who should I send the note to?"** (B7). I did not answer. The send approval was not reached live; its layout was checked on a fixture (fix 5). | task `57f65be0-4cdf-5022-8f2d-37ad7a0c3c4b`, 20:01 |

Acknowledgement is not slow: my message shows 17 ms after Return, "Nori is thinking… Details" right after, the "I've accepted your task" line about 7 s after sending, and the answer at about 16 s (step 2).

## Fixes in `web/` (before and after)

0. **Write access took four Google trips (from Austin's real connect, reported by the coordinator; top priority).**
   - Before: Connect asked only for read access. After connecting, each change or send capability had its own Allow button, and each one was a full Google consent trip.
   - After, at first connect: two choices before going to Google, each one consent. "Read only" reads calendar, email, contacts and files, and Nori asks for a new Google sign-in the first time it needs to change or send something. "Read, change and send" is everything in Read only, plus each change and send item listed by name. Under that choice: "Sending still follows your approval settings in Settings → Permissions; nothing about that changes here." The "What Nori can do" checks follow the choice.
   - Read only is preselected as the smaller grant. **That is frontend's choice, not Austin's decision**, and it can be flipped.
   - After, on a connected account: where two or more items aren't allowed yet, one "Allow everything Nori can do here" button, above the per-item Allow buttons: "One Google sign-in for all of these. Allow on one item asks for just that one." With a single account, "Allow everything" also appears on "What Nori can do". Both send every supported capability through the existing connect and repair action (`allCapabilities`). (`AppsScreen.tsx`)
   - Verified on my stack with the real Google client at 20:05. "Read, change and send" → Connect opened one Google page requesting `calendar.readonly calendar.events gmail.readonly gmail.compose gmail.send gmail.modify contacts.readonly contacts.other.readonly drive.readonly drive.file` (plus `openid email`). It was Google's account chooser; I chose nothing and went back. The connected-account button was checked on fixtures at phone and desktop width only; a real repair needs a signed-in account.

1. **Reminder destination said "Nowhere".**
   - Before: a reminder made in chat showed "Sends to: Nowhere; results stay in Library". It did reach the in-app inbox, and nothing said it would not reach Austin with the app closed.
   - After: "Nori inbox only (Settings → Notifications), so nothing reaches you while Nori is closed. Choose push or email under Edit.", with both links. Edit already offers the destinations. (`RoutinesScreen.tsx`)
2. **A reminder that had run looked on and cancelled at once.**
   - Before: after it fired, the page showed the pill "On", the line "Cancelled.", and Run now, Pause, Snooze and Cancel automation.
   - After: pill "Done", line "This one-time automation has run. It won't run again.", with no Pause or Snooze. The chat card says "Done" too. Core keeps such a reminder `enabled` with no `next_at`; the web reads that as done. (`RoutinesScreen.tsx`, `CardView.tsx`)
3. **The step-3 capability was cut off on a phone.**
   - Before: Apps → Google at 375 px showed "Create and change events, i…".
   - After: capability names wrap. Other rows still truncate long titles. (`Row` `wrapTitle`, `AppsScreen.tsx`)
4. **The connect screen didn't say plainly what is granted.**
   - Before: the note sat under "What Nori can do", below the Connect button, and read "Connect asks Google only for the Read items…".
   - After: directly under Accounts, next to Connect: "One Google sign-in lets Nori read your calendar, email, contacts and files. Nori can't change or send anything yet: it asks again, with a new Google sign-in, the first time it needs to." The list comes from the app's categories, and the sentence shows only before the first account. (`AppsScreen.tsx`)
   - I did not write "you approve every send", because a permission rule can allow sends (`permissions/rules.go`).
5. **The send approval didn't set out the recipient and the text.**
   - Before: core uses the whole summary (`Send "subject" from … to …:` plus the message) as the card's heading, so the heading repeated the full message, and the same text appeared again below. "To" came after "Account".
   - After: the heading is a short question ("Send this?"; "Make this change?" for writes). The message is in its own quoted block, labelled "Message to be sent". "To" comes first in bold, then "From". When core has shortened the message (trailing "…"), the card says "Only the start of the message is shown here. The rest is sent too."
   - Checked at phone width on a display-only fixture in core's summary shape (`int_send_note`, `?fixtures=1`), not on a real send. (`InteractionCard.tsx`, `fixtures.server.ts`)

Checked and fine, so no change: Connect shows "Opening sign-in…" and reaches Google in about 2–3 s. Backing out of Google's page leaves "No Google account yet" (last turn's fix holds on the real path). The Scheduled tab lists the reminder with "in 3 minutes". The chat at phone width has no overflow.

## Not reachable without Austin's Google sign-in

- What Apps shows on return from Google: the connected banner, the partial-grant warning, the account row.
- Step 2 with sources from his real calendar.
- Step 3's more-access card (the first calendar write asks for a second Google sign-in), both approval cards (create, then move), their live summaries naming the "Nori test" calendar, the event's removal at the end, and how the result reads in chat and in Work → Finished.
- Step 5: Gmail search results, the real send approval card (live text, recipients, account label), and the sent message.
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
- **B6, approval text can be cut.** `connectors/google/mail.go` (send prepare) cuts the body at 600 **bytes**, which can split a multi-byte character. It also joins To, Cc and Bcc into one "to" list in the summary. Austin should see the full text before he clicks. Smallest change: cut by runes at a larger limit, or send the full body in the approval payload, and name Cc/Bcc separately. The card already handles a shortened body honestly (fix 5).
- **B7, "send me" is not resolved to his own address.** For "send me a short note", the task asked "Who should I send the note to?" (task `57f65be0…`). With Gmail as the slice mailbox, "me" should be the connected Google account's address.
- Still open from the first turn: `reason` in the sign-in payload (r2.11 forbids it).

### From integration

- Nothing blocking. On 3213, Austin needs push enrolled in Settings → Notifications before step 4 can be judged, and B4 before it can pass.

### From Austin (through the coordinator)

- The Google sign-in for steps 1 to 4. After it, I re-walk the screens listed under "Not reachable".

### From the coordinator

- Record frontend's r2.11 acknowledgement (unchanged from the last turn).
- B1 to B7 are backend's files; I did not edit them. If the coordinator wants frontend to take B1 (a one-line prompt change), say so.
