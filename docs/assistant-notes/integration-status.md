# Integration status

Branch `claude/nori-assistant-integration-bd1278`, source of draft PR a-barwick/nori#165. Updated 2026-11-10, third turn of the restart. Assignments are in the coordination record on `claude/nori-assistant-integration-25ae45`. The first run's version of this note is in git history.

**Provider verified: 0 of 44.**

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

- Optional: a way to remove a never-connected connection (disconnect answers "does not apply", `DELETE` is 405).
- Say if `a62a26e` should be held back after all. It is merged here as unverified.

## Needs from frontend

- Nothing new. r2.11 adoption is recorded. If your core is still down from my 07:56 `pkill`, restart it.

## Needs from the coordinator

- Nothing new. The r2.11 row now carries both acknowledgements on this branch. Your copy at `8a31e15` still says outstanding; take this branch's row when you next merge.
- Still waiting: the first slice from Austin's backlog.

## Needs from Austin

All of these are in `docs/assistant-notes/austin-real-accounts-steps.md`:

- `OPENROUTER_API_KEY` in `.env` (still empty; `OPENAI_API_KEY` is set).
- The Google OAuth client in `.env`. Check that the redirect URI `http://127.0.0.1:3213/api/v1/connections/oauth/callback` is on the client and that his address is a test user.
- A "Nori test" calendar.
- After integration confirms the authorize URL: his Google sign-in at http://127.0.0.1:3213/apps.
- Open with him, by his list: D1 to D4, r2.11, which mailbox counts for email (HEY), and the first slice.

## Restart the stacks

Commands are in `scripts/local-stacks/README.md`. Drop `NORI_STACK_NO_MODEL=1` once `.env` has both model keys. Logs: `~/.nori-stacks/<stack>/{core,worker,web}.log`.
