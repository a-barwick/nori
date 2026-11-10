# Frontend status (nori-frontend)

Branch `claude/nori-assistant-frontend-78149f`, head `bb77f1e` (integration `9611cea` merged in). Contract adopted: r2.10 (`7a1e041`).

## Where things are

| Area | Code | State |
| --- | --- | --- |
| Shell, nav, badges, focus on navigation | `web/src/components/assistant/AppShell.tsx` | Locally verified (8082 and 8083) |
| Chat (existing conversation + side chats), typed cards | `web/src/components/runtime/NoriApp.tsx`, `assistant/CardView.tsx` | Chat locally verified; side-chat task cards verified against backend 938d665; other cards wait for backend card parts |
| Work list and task detail (steer, stop) | `assistant/WorkScreen.tsx` | Locally verified on /v1/work and on the legacy snapshot (ask → task → stop) |
| Interactions (question, approval, account, sign-in, takeover) | `assistant/InteractionCard.tsx` | Question answered live through /v1/interactions/{id}/responses; approval, account choice and sign-in not yet raised by a live task |
| Apps, plugins, MCP | `assistant/AppsScreen.tsx` | Apps catalog/detail render live from 8083; sign-in round trip not yet run; MCP add server and write-only token verified against a6c0e65 (token never in browser storage or DOM); plugins (install, off/on, pin, uninstall) and skills (create, version, export, import, run, delete) verified live; Connected clients (A13) verified: token works on /mcp, 401 after revoke |
| Routines, Goals | `assistant/RoutinesScreen.tsx`, `GoalsScreen.tsx` | Locally verified against backend 805b534: preview, create, pause, resume, snooze, run now; goal create, pause, complete |
| Library and search | `assistant/LibraryScreen.tsx` | Search verified live against a6c0e65 (tasks and chat messages with jump links); library list live |
| Settings | `assistant/SettingsScreen.tsx` | Permissions, Preferences (two-tab conflict), Memory (edit, forget) verified live; notification destinations (r2.9): email add, send code, remove verified live (send code returns provider_unavailable locally, shown plainly); this-device push and real delivery blocked on VAPID keys and a mail provider; history fixture-backed |
| Computer | `assistant/ComputerScreen.tsx` | Verified live with my own computer container (port 8098): take over, more time, hand back resumes the waiting task; shows the task's latest screenshot (no live screen feed in the core) |
| Composer | `runtime/RuntimeComposer.tsx`, `lib/assistant/voice.ts` | Server accept list (incl. .ics), upload progress and cancel, drag/drop, paste, saved draft verified live. Dictation verified with a scripted SpeechRecognition: interim words, final words into the draft (nothing sent), stop, blocked-mic message; read-aloud speaks a new reply, and starting dictation or turning read-aloud off cancels it. Real microphone needs Austin's device |

## How to run

- Fixtures: open any screen with `?fixtures=1` (dev only, served by `/api/fixtures/v1`). Never evidence.
- Live: web reads `/api/v1/<family>` through `assistantRoute` in `web/src/lib/runtime/forward.ts`; a screen switches to live data when `/v1/capabilities.assistant_features.<family>` is true.
- Types: `cd web && bun run contracts` regenerates `src/lib/contracts/assistant.ts`; `bun run contracts:check` fails when stale.
- After merging, `rm -rf web/.next` if the dev server reload-loops.

## Open items

- ICS: live on 78cf85f from a fresh chat (preview called, completed, no false duplicates). The long main chat still repeats stale "needs attention"; reported to backend. A real duplicate match has not been exercised (the recordings have no matching iCalUID).
- 0b30953: connection state reads status and missing_scopes; Allow more access requests the missing capabilities; task result renders markdown; Saved filters by result_ids (core ignores ?task_id=).
- 0eccf8b: action lists hide capability_load; working-hours inputs fit on a phone.
- Microsoft renders Unavailable in the catalog (9acf840 merged).
- Blocked: push/email delivery (VAPID keys, mail provider: Austin), real Google provider (D1), dictation with a mic, real phone + screen reader (A42), shell in my computer container (no cgroup delegation in an unprivileged container).

- Local stack note: `nori-local` does not host the task worker. Run `nori-worker` with `TEMPORAL_BUILD_ID` and set it current, or tasks are accepted but never run.
- Live checks this round: plugin update and rollback (uploaded deepwiki/mcp 1.1.0), Google connect through the web's callback with NORI_PROVIDER_REPLAY, .ics read and listed (duplicate check blocked by backend scope gating), memory edit/forget, two-tab conflict, offline retry reusing the request ID, drag/drop and paste.

## Round: r2.10, A19, W8, copy pass
- iCloud sign-in (0d3bf5e): rejected pair shows Apple's refusal in plain words; accepted pair connects; the password is in no browser storage and no response. Verified against the iCloud recordings.
- A19 from the main chat: preview marked Dentist as existing and potluck as new; only the potluck was written (calendar_event_create, not ICS import). The readback (calendar_event_get) returned an unrelated recorded event, so the task ended Incomplete. Side chats read CSV only (core design), so .ics checks run in the main chat.
- W8 (bf2efb2): Linear, GitHub and Notion are in the catalog; Linear installs (review shows mcp.linear.app) and its server asks for a token. It is bearer-token auth, not OAuth. The core reports plugin-contributed servers with source kind "user".
- Copy pass (bb77f1e): Routines renamed Automations in the UI (paths stay /routines); cut redundant lines under titles and helper text; "Access:" with "any action" for unknown effects; plural tool counts; plain step names; Activity skips tool calls already listed under Actions.

## Round: accessibility, Drive (frontend list complete)
- Contrast (aa616ee): danger #cc1f33, warning #946000, tertiary #6e6880 meet 4.5:1 on their backgrounds; axe-core shows 0 violations on the audited screens at desktop and phone width. Chat has a heading.
- Keyboard (7e1f76c): skip link (message box on Chat, title elsewhere); focus returns to the control, or the one that replaced it, after an action. Walked in the built-in browser: Work, Chat, Preferences, New automation, automation Pause/Resume (announced).
- Drive (93a47e0, 817214d): "Allow" per capability on a single signed-in account; Drive granted through the recorded exchange (code recorded-drive-code); a chat search found and read the Lisbon plan, with source links to the Doc and the packing list.
- Inbox lines show plain text (83357c9).
- Waiting: backend d08ca3e (D-045) and aa802b2 (D-042: side-chat files, times in the user's zone) are not on integration yet. D-042 still shows ISO times in the iCloud create summary and the automation run's saved title.
