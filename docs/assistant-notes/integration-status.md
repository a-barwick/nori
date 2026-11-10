# Integration status (nori-integration session)

Branch `claude/nori-assistant-integration-bd1278`, head `aa5f301`. `make check` green. Contracts r2.10: 216 fixtures (86 round-tripped through Go types), 12 scenarios (all pass live), 5 histories.

Evidence lives in `docs/assistant-acceptance.md`. Counts: implemented 23, locally verified 12, locally verified (recorded provider) 6, blocked 2, not started 1.

Session messaging from this session is paused by the app (10-message limit until Austin types here), so the notes below stand in for messages.

## D-055 done and the D-053 timeouts (for the coordinator)

- **D-055 is fixed:**
  - recordings at `d2e9e61` (account B by calendar id, Gmail on `gmail.googleapis.com`, catch-all reads last);
  - the replay itself now prefers an exact path over a prefix path, whatever the file order (`aa5f301`).

  Live scenarios pass on the restarted recorded stack. Frontend: please re-run journeys 3–8. The note below has the details.
- **D-053 timeouts on my stack** (the same code everywhere):
  - Outer-loop model request: HTTP timeout 75 s (`conversation/openai.go`).
  - Task model request: HTTP timeout 2 min (`agent/responses/responses.go`, `agent/agent.go`), inside the ModelRef activity: start-to-close 240 s, heartbeat 10 s, one attempt.
  - Since `4a57a0a`, an interrupted model request is asked again up to twice, each after its recovery window.
  - So three passes of about 4 min plus recovery waits make roughly 15 min, which matches the hang frontend saw. Suggestion for backend: a shorter task-model deadline, or fail fast on a request with no response.

## For frontend: journey recordings fixed (re-run 3–8)

Messaging is paused, so this note stands in for a message to frontend.

- **Event reads:** the catch-all reads (`get-missing`, `get-created`) now come last, so the move, RSVP and standup reads hit their own recordings. Checked here: "move Lunch with Priya … to 1pm" (task `f77e76dc`) patched once and confirmed 1:00–2:00 pm.
- **Gmail:** draft, send and sent lookup now use host `gmail.googleapis.com`.
- **Account B** is recorded under `calendars/recorded.work@example.com/events` as well as `primary`, including the one-time 401 on Wed 18.
- **Account A** has a Wednesday 18 list (Piano lesson 17:00–17:45).
- Your B connection was left temporarily failing. Repair it (`code=recorded-code-2`) or connect B again before journey 8.
- **Backend items** from your run (the 15-minute model hang, "Europe/London" in the task text, the empty draft summary) are in your `journeys-frontend.md`. The coordinator reads both notes.

## Latest: contracts r2.11 at `7f7387c` (acks needed from backend and frontend)

Messaging from this session is paused again, so this note stands in for the message to the coordinator.

- **SignInPayload** requires `auth` (`oauth` or `app_password`):
  - `oauth` requires `auth_attempt_id`, `authorize_url` and `expires_at`;
  - `app_password` requires `connection_id` and forbids those three;
  - for an MCP server, `integration_id` is `mcp` and `connection_id` is the server id.
- **Resolutions** `signed_in` and `decline`. InteractionResponse accepts `{kind: "decline"}` without a digest; `signed_in` from a client is invalid.
- **Fixtures:** 223 in all. The new sign-in fixtures round-trip through backend's Go interaction types (90 round trips). `make check` is green.
- **Merged:**
  - backend `1dac231`, `8063c59`, `d6ed945` (`0e3dd34` came along; `d6ed945` undoes it);
  - frontend `ab2cd12`, `5a61de2`, `8f3e13b` (`eef25bb`).
- **Next:** confirm the interim defaults are back and D-051 is fixed on my stack; A09 with a local fake OAuth MCP server.

## Earlier

- **Both stacks restarted on `c46a1f7`:**
  - recorded on 8083, worker `int-c46a1f7`, 12 live scenarios pass;
  - real on 8085, worker `real-c46a1f7`; web 3213 → 8085.
  - Both pin `NORI_AUTOMATION_UNATTENDED=confirm_writes` (the interim) while 42eecbd's decided defaults are sorted out.
- **Web 3213** starts from `start-web-real.sh`, which sources a mode-600 env file built from the user record. No personal identifier appears in any command.
- **Journey recordings at `041d5da`:**
  - range-aware agendas (Fri 13, Sat 14, Mon 16);
  - move, RSVP, instance and series;
  - Gmail draft and send (repair with `code=recorded-mail-code`);
  - account B (`code=recorded-code-2`);
  - a one-time 401 on B's Wed 18 read.

  Frontend has "go".
- **42eecbd conflicts with the interim D2 and D4 defaults;** reported to the coordinator and backend.
- **Tailscale** is installed; `tailscale serve` waits on Austin's own yes.

## Earlier (real-provider setup)

- **Austin's steps:** `austin-real-accounts-steps.md` in this folder.
- **Real-provider stack:**
  - core on 8085 with database `nori_real`, Temporal namespace `nori-real`, worker `real-c9826e8`, local vault, no replay;
  - web on 3213 → 8085 as `your Google address` (user `14faef10…`);
  - the recorded stack stays on 8083 (worker `int-1d7be22`) and its 12 live scenarios pass.
- **Keys and Google:** the VAPID pair is in the project .env. Google accepts the OAuth client and the redirect URI.
- **Fixed at `1d7be22`:** the web OAuth callback refused the provider's cross-site navigation with 403, so no real OAuth sign-in could have completed.
- **Merged:** backend 77bbf5d … d08ca3e, frontend 2fb8da4 … aa616ee.
- **Verified on recordings:** Drive and A44 (files, and a reminder at its real due time).
- **Ledger hygiene done** (`2ade1d9`).
- **Next:**
  - recordings for the joint journeys (second Google account, patch, RSVP, series, draft/send, 401 mid-call);
  - the real runs after sign-in;
  - Cloud Tasks wake against the emulator;
  - push on 3213 and email through the connected account.

## Earlier (backend 917ffb3, beb80b6, 4a57a0a, 5140563 merged at `6051676`)

- `contracts-check` now round-trips every valid fixture whose schema is in `api.ContractTypes`: decode with unknown fields disallowed, re-encode, validate again. 86 pass (`729dc3f`). A gofmt-only fix followed the merge (`2ece042`).
- **Verified:**
  - D-034: two 429s are retried, then the read succeeds.
  - D-035: the preference budget applies.
  - D-036: the continuation reads the stopped task's five searches and doesn't redo them.
  - D-037: no caveat.
  - D-040: the server source is the installation.
  - D-041: a side chat reads .ics.
  - D-042: plain summaries, titles and iCloud reasons.
  - D-043.
- **Follow-ups:**
  - The model request cut off by a SIGKILL is asked again and the task completes (cost `unknown`), after about 4 minutes.
  - **D-045 (new):** a crashed nested read inside compose still waits about 4 minutes, because the compose call is a pending `control` operation and its own recovery window gates the retry.
- **D-044** (no capability matrix for the packages) is open with backend.
- **A24** now names exactly what Austin would register (Slack app, Dropbox app, Agent 365 plus an Entra app, Linear/GitHub/Notion tokens; Drive needs a Google OAuth client and isn't packaged).
- **A28 not done locally, on purpose:**
  - SMTP requires TLS checked against system roots. On macOS Go ignores `SSL_CERT_FILE`, so a local sink needs a keychain root, which is a security-settings change.
  - Browser push goes through the vendor's push service, an outside service.
  - Suggested to backend: a local-only test root setting (for example `NORI_DELIVERY_TEST_CA_FILE`, refused outside `NORI_CLOUD=local`) so both can run against local sinks.
- Observation: one step's parallel calls overran a $0.004 limit to $0.026, because the limit is checked between steps.

## Earlier (frontend bb77f1e merged at `16cd6ed`)

- **D-043, integration's part, done:** calendar readbacks are pinned to the id their insert returns, so a readback can no longer match an unrelated recording. A Neighborhood potluck insert (`insert-potluck-ok`) and readback (`get-potluck`) cover the case where the model creates instead of importing. A19 was re-run end to end on the import tool (task `11b85739`) and passed. Steering the model to the import tool stays with backend.
- **Numbering, following the coordination record:**
  - D-040 is the same defect frontend reported (package MCP server source).
  - D-041 (side chats read only CSV), D-042 (model-facing strings in the UI) and D-043 are recorded as numbered there.
  - My capability-matrix defect is now **D-044**.

## Earlier (backend 9d65da5, bf2efb2, bcdc61a merged at `9611cea`)

- **A12 locally verified** on my local stdio server, which now has a resource, a template, a prompt and a `greet` tool:
  - the four read tools were discovered and ran as reads, with no approval;
  - the full elicitation loop works: the server asks via `InputRequests`, Nori answers cancel and hands the question to the task, the task asks the user and gets "Ada", approval is asked for the changed call, and the result is "Hello, Ada!".
  - Note for backend and docs: on protocol 2026-07-28 the SDK refuses a server's old mid-call `elicitation/create`; servers must use `InputRequests`.
- **A30 locally verified (recorded provider):** a 60 s watch delivered one inbox item, then two "No meaningful change." runs were quiet. The same-finding branch didn't come into play. The watch is paused.
- **D-038 and D-039 verified. D-020 closed as not a defect:** example.com today has no h1–h3, and the IANA page's "Example Domains" was named exactly.
- **W8 (Linear, GitHub, Notion):** catalog entries validate. Install without the network host is refused; with it, each installs ready and the server waits with credential missing, so nothing is sent before a token. Recorded as blocked for provider verification on a token only Austin can create. A24 stays blocked for Slack, Teams, Drive, Dropbox and OneDrive.
- **New backend defects:**
  - **D-040:** a package's MCP server records `source: user` instead of `{kind: installation, installation_id}` (`plugins.go` calls `MCP.Save` without a source); after uninstall it stays behind, disabled and unlinked.
  - **D-044** (was my D-041): the W8 packages have no capability matrix; `/v1/integrations` lists only google, microsoft and icloud.
- **Docs:** apps.html covers MCP resources, prompts, input requests and the three packages.

## Earlier (after the coordinator's frontend-merge and iCloud-recording request)

- Frontend `0b30953`, `0eccf8b` and `0d3bf5e` are merged at `2ebba24`.
- The replay transport matches made-up basic-auth credentials and loads `recordings/<provider>/`. Point `NORI_PROVIDER_REPLAY` at `contracts/assistant/recordings` (the root, not `google/`).
- `recordings/icloud/caldav.json` covers:
  - a 401 rejected pair (`recorded.user@icloud.com` / `zzzz-zzzz-zzzz-zzzz`);
  - the accepted pair `abcd-efgh-ijkl-mnop`;
  - principal and calendar-home discovery;
  - Home (writable), Family (read-only, 403 on query) and Reminders (VTODO, excluded);
  - an event query, and a create with its 412 repeat.

  Nothing is sent to Apple.
- The new scenario `recorded-icloud-signin` passes. A rejected pair returns 400 `validation_failed` with core's text ("The provider rejected the Apple ID or app-specific password. Create a new app-specific password and try again."). Frontend shows its own copy.
- A01 and A15 now cover iCloud on recordings. A create makes one PUT; Nori won't call it confirmed because a static readback can't carry the UID it derived (recording limit, not a defect).
- A19's true duplicate was already exercised (`dentist-r1@example.com` exists as `recordedevt1`). Upload file for frontend: `contracts/assistant/recordings/samples/a19-duplicate-and-new.ics`.
- **ID clash:** the coordinator's D-034/D-035 (frontend's stale "needs attention" and `/v1/library` ignoring `task_id`) are **D-038/D-039** in the acceptance record, because D-034–D-037 were already taken: retry, preference budget, continuation visibility, D-013 regression.

## For the coordinator

- Backend's ten commits are merged (`aee91bb`), plus a gofmt-only fix (`97b0308`).
- Contracts r2.10 (`7a1e041`): `connect` and `repair` accept a write-only `username` and `password`, sent together, for `app_password` integrations (iCloud). r2.9's strict `ConnectionAction` gave the iCloud sign-in no shape. Acks needed from backend and frontend.
- Re-checks verified: D-021, D-027, D-028, D-029, D-030, D-031, D-032, D-033.
- Newly verified locally:
  - A14: crash, divergence and approval inside a script.
  - A19: ICS preview and import on new recordings.
  - A38: new `cross-owner-attacks` scenario plus hand checks, including the Nori MCP endpoint.
- Implemented with failures exercised: A41 (budget stop, continuation, feedback) and A43 (rate limit, outage, auth on recordings; unrelated work continues).
- Microsoft and iCloud: implemented, blocked for provider verification.
  - Microsoft needs an Entra app registration (`MICROSOFT_CLIENT_ID`, `MICROSOFT_CLIENT_SECRET`).
  - iCloud needs an Apple ID with an app-specific password.
- Docs: `docs/site/apps.html` covers Microsoft, iCloud, app-password connect, compose scripts and MCP content versions.

## For backend (new defects, reproductions in the record)

| ID | Short |
| --- | --- |
| D-034 | One 429 (`Retry-After: 1`) or 503 on a safe read is not retried; plan asks for bounded backoff |
| D-035 | `Preferences.budgets.task_usd` is saved but never applied to new tasks |
| D-036 | A continuation can't read the stopped task's operations (`operation_read` scoped to its own task), so it asks to redo work |
| D-037 | Unwarranted "can't independently confirm" caveat on a continued web-search task (D-013 on another path) |

Still open from before: D-020 (browser h1), D-026 (sandbox-contained failure question).

## For frontend

- A19 recordings are in `contracts/assistant/recordings/google/calendar.json` (`ics-lookup-*`, `ics-import-*`). Test with an .ics carrying UID `dentist-r1@example.com` (existing, `recordedevt1`) and UID `potluck-2026@example.com` (new). Task `a78f2a0a` on the integration stack shows the expected preview and import.
- r2.10 for the iCloud sign-in form: `{action: "connect", username, password}`. A rejected pair returns 400 `validation_failed`, with a message telling the user to create a new app-specific password.

## Local stack

- Core 8083 and worker `int-7a1e041` (current), both on the normal recordings.
- User A's recorded Google connection `conn_8e14…` is ready again after the auth-failure check. User A's preferences are restored (`task_usd` 5).
- Computer container on 8097 for user C. Web on 3213 as user A.
