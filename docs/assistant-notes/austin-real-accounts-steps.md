# What this Mac needs from Austin before a real Google run

Rewritten 2026-11-10 by integration for the new machine. It covers Google only. Microsoft and iCloud come later (see the end).

Local Nori for real accounts runs on this Mac at **http://127.0.0.1:3213**. It has its own database (`nori_real`), its own Temporal namespace (`nori-real`) and a new, empty local credential vault under `~/.nori-stacks/real/`. Production is not touched. Integration restarts it with `scripts/local-stacks/stack.sh real …` (see `scripts/local-stacks/README.md`).

Today it answers, but only as plumbing: without `OPENROUTER_API_KEY` it starts only in a no-model mode (placeholder keys, all outbound traffic blocked), and without a Google client it cannot start a Google sign-in. The steps below are the things only you can supply. Please put values in the file yourself. Don't paste them into a chat or a session: nobody else reads, prints or copies them.

## What `.env` lacks, by variable name

Compared on 2026-11-10 against `.env.example` and against what core actually reads (`core/internal/settings/settings.go`). Values were never read or printed. Only whether each one is present was checked.

| Needed for | Variable | State in `/Users/austin/Documents/dev/nori/.env` | Who |
| --- | --- | --- | --- |
| Any conversation | `OPENAI_API_KEY` | empty at 07:52; **set** when re-checked after your 07:58 save | done |
| Any conversation or task (with background tasks on, core and the worker both refuse to start without it; conversation summaries and the task model `openai/gpt-6.1-terra` go through OpenRouter) | `OPENROUTER_API_KEY` | present, **empty** | **you** |
| Google sign-in | `GOOGLE_CALENDAR_CLIENT_ID`, `GOOGLE_CALENDAR_CLIENT_SECRET` | absent (also missing from `.env.example`; hosted reads them from Secret Manager) | **you** |
| Google redirect | `NORI_OAUTH_REDIRECT_URL` | absent; the stack script sets `http://127.0.0.1:3213/api/v1/connections/oauth/callback` | integration (done) |
| Credential store | `NORI_CREDENTIAL_STORE`, `NORI_CREDENTIAL_DIR` | absent; the stack script sets `local` and a new vault | integration (done) |
| Background tasks | `NORI_BACKGROUND_TASKS_ENABLED` | `false` (chat-only); the stack script overrides it to `true` for the stacks only | integration (done) |
| Push (VAPID) | `NORI_VAPID_PUBLIC_KEY`, `NORI_VAPID_PRIVATE_KEY`, `NORI_VAPID_SUBJECT` | absent from `.env`. A new pair was generated into `~/.nori-stacks/real.env` (mode 600) and the real stack loads it from there. It went outside `.env` because `.env` was open in vim when it was generated, and your next save would have overwritten an edit made underneath it. | integration (done) |
| Web search inside tasks (not needed for a Google slice) | `BRAVE_SEARCH_API_KEY` | present, empty | you, optional |
| Microsoft (later) | `MICROSOFT_CLIENT_ID`, `MICROSOFT_CLIENT_SECRET`, `MICROSOFT_TENANT` | absent | you, later |
| Email delivery (not in scope now) | `NORI_SMTP_*`, `NORI_EMAIL_FROM` | absent | not now |

## Step 1. Model keys (about 2 minutes)

In `/Users/austin/Documents/dev/nori/.env`, fill:

- `OPENAI_API_KEY=` with a key from platform.openai.com → API keys. It is used for the outer conversation. (Set as of 07:58 on 2026-11-10.)
- `OPENROUTER_API_KEY=` with a key from openrouter.ai → Keys. It is used for the task worker.

The project budget defaults still apply: `NORI_TASK_BUDGET_USD` caps each task.

## Step 2. Google OAuth client for local use (about 3 minutes)

The first run used the Nori web client whose client ID starts `8273801370-`.

1. Open Google Cloud Console for the project that owns that client → **Google Auth Platform → Clients** → open that client.
2. Under **Authorized redirect URIs**, make sure `http://127.0.0.1:3213/api/v1/connections/oauth/callback` is listed. The first run had added it. Add it if it is gone.
3. Copy the **Client ID**. For the secret, use **Add secret** and copy the new value (Google shows it once). An existing secret stays valid for production.
4. Add two lines to `/Users/austin/Documents/dev/nori/.env`:
   - `GOOGLE_CALENDAR_CLIENT_ID=<client ID>`
   - `GOOGLE_CALENDAR_CLIENT_SECRET=<secret value>`
5. Under **Google Auth Platform → Audience**, check that the app is in **Testing** and that your Google address is under **Test users**. Add it if not. Gmail scopes are restricted, so an unverified app only works for test users.

Then tell the coordinator (or any Nori session) "keys are in". Integration restarts the real stack and checks that Google accepts the authorize URL. It loads the consent page without choosing an account, before anything else happens.

## Step 3. A calendar just for the checks (1 minute)

At calendar.google.com → Other calendars **+** → Create new calendar, named **Nori test**. Writes from the real run go only there.

## Step 4. Sign in (about 2 minutes, after integration confirms step 2)

1. Open http://127.0.0.1:3213/apps → Google → Connect.
2. Choose your Google address and approve the access shown.
   - "Google hasn't verified this app": choose Continue. It is your own app in testing.
   - "Access blocked: … has not completed the Google verification process": your address is not a test user; see step 2.5.

You sign in to Nori's web on this Mac as a local test user (`real-a@nori.local`). Your Google account is what gets connected; no address of yours is written into any command or file.

## Also open with you (not steps; listed so nothing waits silently)

- **Which mailbox counts for email.** Your backlog asked about HEY. Backend is checking what can be learned about HEY on this machine without signing in. Nothing about HEY has been checked against your account.
- **The first slice.** The real run tests only the slice taken from your backlog once it is recorded in `docs/assistant-coordination.md`. Nothing writes to your account before that run is agreed with you.
- **D1 to D4 and r2.11** are open with you; see the coordination record. Interim mechanisms are in use and are not your decisions.

## Rules for the real run

- Nothing writes to your account until you have signed in and the slice run is agreed with you.
- Writes then go only to the **Nori test** calendar. They are reversible, and integration deletes what it made.
- Email goes only to your own address, after an approval you click.
- A row in `docs/assistant-acceptance.md` becomes "provider verified" only with the task id and time of a run against your account.

## Later, not now

- **Microsoft**: an Entra app registration (`MICROSOFT_CLIENT_ID`, `MICROSOFT_CLIENT_SECRET`, `MICROSOFT_TENANT=consumers`) with the redirect URI above. The first run's steps are in git history (this file before 2026-11-10).
- **iCloud**: an app-specific password, entered by you in Apps → iCloud Calendar.

## Turning it off

Disconnect Google in Apps. Google also lists the app at myaccount.google.com/connections. Integration stops the stack with `scripts/local-stacks/stack.sh real stop`.
