# Integration status

Branch `claude/nori-assistant-integration-bd1278`, source of draft PR a-barwick/nori#165. Updated 2026-11-10 (restart, first turn, new machine). The first run's version of this note is in git history.

**Provider verified: 0 of 44.**

## What ran this turn

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

## What failed or went wrong

- **I killed other sessions' cores.** At about 07:56 I ran `pkill -f nori-local`, which matches every session's core on this machine. Frontend's core (its web 3212 and worker on `nori-frontend` were up) was very likely killed, and possibly backend's. I messaged both sessions to restart. `stack.sh stop` now stops only its own ports and namespace, and the README warns against stopping by name.
- Both cores refuse to start without `OPENROUTER_API_KEY` even with `OPENAI_API_KEY` set, because background tasks need it for conversation summaries. With only the OpenAI key present, the stacks still run in no-model mode.
- The Google authorize-URL check did not run (no client).
- `.env` holds `PORT=8080`. A worker with `PORT` unset picks that up and enters hosted mode (`NORI_WORKER_SERVICES must be configured`). `stack.sh` exports an empty `PORT` instead.

## Needs from backend

- Add `GOOGLE_CALENDAR_CLIENT_ID` and `GOOGLE_CALENDAR_CLIENT_SECRET` (and `NORI_OAUTH_REDIRECT_URL`) to `.env.example`. Core reads them, but the example omits them, so a new machine cannot tell they are needed.
- Acknowledge or reject r2.11 as published at `7f7387c` in your status note.
- Optional: a way to remove a never-connected connection (disconnect answers "does not apply", `DELETE` is 405).
- If you had a core running at 07:56, restart it (see above).

## Needs from frontend

- Restart your core if it is down (my `pkill`, 07:56).
- State in your status note whether you accept r2.11 as published (the restart assignment).

## Needs from the coordinator

- I added one row to `docs/assistant-coordination.md` on this branch (r2.11 in Contract revisions). Merge it, or tell me to move it to your branch, so the two copies don't diverge.
- The restart section's "This machine" list assumes the Google OAuth client is in `.env` ("Integration confirms the Google OAuth client by name"). It is absent, and the model keys were empty. Please correct that list.
- The first slice from Austin's backlog. I prepare, and add no cases, recordings or delivery work until it arrives.

## Needs from Austin

All of these are in `docs/assistant-notes/austin-real-accounts-steps.md`:

- `OPENROUTER_API_KEY` in `.env`.
- The Google OAuth client in `.env`. Check that the redirect URI `http://127.0.0.1:3213/api/v1/connections/oauth/callback` is on the client and that his address is a test user.
- A "Nori test" calendar.
- After integration confirms the authorize URL: his Google sign-in at http://127.0.0.1:3213/apps.
- Open with him, by his list: D1 to D4, r2.11, which mailbox counts for email (HEY), and the first slice.

## Restart the stacks

Commands are in `scripts/local-stacks/README.md`. Drop `NORI_STACK_NO_MODEL=1` once `.env` has both model keys. Logs: `~/.nori-stacks/<stack>/{core,worker,web}.log`.
