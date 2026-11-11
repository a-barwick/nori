# Environment recovery handoff

Codex checked this machine on 2026-11-10 around 08:55 America/Los_Angeles after Austin asked for help with the blocked Claude run.

## Credentials and running stacks

- Recovered the existing `OPENROUTER_API_KEY`, `GOOGLE_CALENDAR_CLIENT_ID` and `GOOGLE_CALENDAR_CLIENT_SECRET` from their existing Nori GCP Secret Manager entries into the main checkout's gitignored `.env`. The file is mode 600. Values were kept out of output and this note. The OpenRouter key endpoint returned HTTP 200.
- Preserved the OpenAI key Austin entered and the selected model roles. The real stack uses Sol through OpenAI and `openai/gpt-6.1-terra` through OpenRouter.
- Restarted the integration worktree's recorded and real stacks from `1456cc9` with real model credentials. The current Temporal builds are `int-1456cc9` in `nori-integration` and `real-1456cc9` in `nori-real`.
- Both cores return HTTP 200 on `/healthz`. The real web app responds on `http://127.0.0.1:3213/apps`. Logs remain under `~/.nori-stacks/{recorded,real}/`.
- A concurrent restart also launched duplicate workers with `NORI_STACK_NO_MODEL=1` and the dead HTTP proxy under those same build IDs. Stopped those identified local duplicates. Leave `NORI_STACK_NO_MODEL` unset for model runs. The active model-enabled worker parents started here are PIDs 52918 and 52921. Check processes before restarting, because a second worker with different network settings can poll the same build.

## Checks performed

- Through the real web proxy, chat `ce927411-bea8-426f-b1ec-fcdf90979bcc` completed with `model connection works`.
- Real-stack background task `7dcbed0f-fad7-5ada-b4bb-0e24e0440133` completed with `667` for 23 multiplied by 29. Its reported model cost was $0.00545925 under the existing $1 task ceiling.
- Earlier task `3b0d297d-6fd2-5c1e-ac0d-2cb0a701d0ce` returned the correct arithmetic answer but marked itself incomplete. My request had also asked the task to verify background execution and the worker. The simpler arithmetic task above then completed successfully. Keep the earlier failure in the evidence.
- The real app generated a Google `calendar.read` authorization link with PKCE and the callback `http://127.0.0.1:3213/api/v1/connections/oauth/callback`. An unauthenticated request followed it to Google's `/v3/signin/identifier` with HTTP 200 and no client or redirect rejection. This verifies the client ID and redirect acceptance. The client secret exchange and account access still require Austin's sign-in.
- `scripts/local-stacks/stack.sh recorded check` passed all 12 scenarios, 223 fixtures including 90 Go round trips, and 5 replayed histories with zero failures. It reports 52 schema definitions without fixtures. Those scenarios use hand-written provider recordings.

## What Claude can do next

The missing model key and OAuth client configuration no longer block the run. Use the existing real stack on port 3213 for Austin's Google sign-in through Apps. The existing Google probe is now connecting after the authorization-link check. Request a new sign-in attempt through the UI as needed.

No Google refresh token was recovered into the local vault. No real account sign-in, Calendar read/write, Gmail operation or full first-slice verification was completed here. Keep the acceptance matrix honest about that distinction.

The first workflow remains undefined. A fresh Linear search found no HEY match and did not identify the backlog Claude referenced. Austin has been asked for its issue link, ID or workflow text. D1–D4, r2.11 and the mailbox choice remain his decisions.
