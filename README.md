# Nori

Nori is a personal assistant you text. This repository holds the whole product:

- [core](core/README.md): the Go agent service and its infrastructure.
- [web](web/README.md): the assistant web interface, its Chats tab and the authenticated Next.js server.

Design:

- [Clients](docs/site/clients.html): what each client and the service own.
- [Service API](docs/site/api.html): assistant and independent-chat routes.
- [Chats](docs/site/chat-client.html): streaming, files, approvals and context limits.

## Run

Run the following commands from the repository root. Install the web dependencies
before `make check`:

```sh
(cd web && bun install --frozen-lockfile)
```

The assistant and independent chat use one Go service. Configure the existing main
checkout's gitignored, mode-600 root `.env` with the names in [.env.example](.env.example).
The app needs its existing Firestore database, Application Default Credentials and
Temporal connection, namespace, task queue and worker configuration. Project and
database names have no defaults. Starting the web and API does not start the separate
inner task worker or computer. Use explicitly configured emulators for isolated data.

Any development command that can call a model must run through the spend wrapper.
Select the existing main configuration in place, including from a worktree:

```sh
NORI_MAIN_ENV=/absolute/path/to/main/.env
NORI_FEATURE_CHAT=your-existing-logical-chat-id
python3 core/infrastructure/spend/run.py \
  --env "$NORI_MAIN_ENV" --task "$NORI_FEATURE_CHAT" --repository "$PWD" \
  -- make dev NORI_ENV_FILE="$NORI_MAIN_ENV"
```

Reuse that logical chat ID for every paid command and retry. Do not copy credentials
into a worktree. The wrapper's keys and reservations cover both conversation paths.
Keep the selected models and providers in [AGENTS.md](AGENTS.md).

With the guarded development stack running, open `http://127.0.0.1:3210/`. Independent
chats are in the side panel's Chats tab, next to Activity.

Each chat saves a separate conversation, streamed replies, assigned files and exact approval
decisions. Its foreground reader accepts CSV files up to 64 KiB, although the upload
limit is 10 MiB. It has a finite saved-context limit and does not include assistant
history, tasks or memories. Use the assistant for background work, shell, browser,
PDF or image analysis. See [Chats](docs/site/chat-client.html).

### Offline fixture and checks

These commands use no project `.env` or paid model:

```sh
make migration-prep   # fixture HTTP journeys and verifier checks
make check            # Go, TypeScript, ESLint and documentation checks
```

The offline fixture in `migration/fixture.ts` stores conversations only while its
process runs. Its results check the chat contract and verifier, and cannot establish
shared-service readiness.

### Shared-service verification

The shared service, the assistant and its Chats tab are implemented. `migration/plan.json`
lists the acceptance criteria. The verifier requires current evidence for service behavior,
the web app, shared budgets, retained-data recovery and the selected providers.
It does not deploy anything.

Start isolated Firestore and Temporal emulators on loopback, then name their existing
listeners explicitly. Recovery builds the previous release's `nori-local` from git;
set `NORI_RECOVERY_OLD_BINARY` only to use a different prebuilt executable:

```sh
export FIRESTORE_EMULATOR_HOST=127.0.0.1:8095
export TEMPORAL_ADDRESS=127.0.0.1:7245
make migration-doctor
make migration-baseline
make migration-integration
```

The baseline runs all six registered suites. Integration runs the shared-service and
upgrade/rollback journeys. Both groups reject skipped or empty runs, create isolated
databases and task queues, and exclude paid providers.

For browser verification, start the deterministic service and worker from the repo root:

```sh
NORI_SHARED_PORT=18080 NORI_SHARED_WORKER=1 \
  bun --no-env-file core/e2e/shared-service-harness.js
```

In another terminal at the repo root, run the Next server against that service:

```sh
NORI_LOCAL_AGENT_URL=http://127.0.0.1:18080 \
  bun --no-env-file run --cwd web dev --port 3310
```

Then verify the assistant and its Chats tab through Next. Install the browser driver once
with `(cd migration && bun install --frozen-lockfile)` and use an existing Chromium executable:

```sh
NORI_BROWSER_EXECUTABLE=/absolute/path/to/chromium \
  python3 migration/verify.py browser --url http://127.0.0.1:3310
python3 migration/verify.py contract --url http://127.0.0.1:18080
```

Browser verification checks owner authentication failures, same-origin requests,
conversation isolation, desktop and phone interactions, attachments and exact approvals.
The target must identify itself as an isolated deterministic service. Fixture and
production targets cannot pass this acceptance check. Stop only the harness and Next
processes started for this run when finished. Preserve their files and evidence.

Each verifier invocation writes logs and a report under `.artifacts/migration/`.
Readiness combines those reports with authorized shared-budget and provider evidence:

```sh
python3 migration/verify.py readiness \
  --evidence /absolute/path/to/baseline/report.json \
  --evidence /absolute/path/to/integration/report.json \
  --evidence /absolute/path/to/browser/report.json \
  --evidence /absolute/path/to/shared-budget/report.json \
  --evidence /absolute/path/to/provider/report.json
```

Reports must match the exercised source files and plan. A partial baseline or fixture
pass cannot certify readiness. Real-ledger and provider checks require separate
budget authorization and the same spend wrapper. Retain eligible provider evidence
instead of repeating paid calls for a documentation change.

Local verification does not update deployed services, worker images or infrastructure.
Production deployment remains a separate action through `make deploy`.

## Durable tasks

The deployed task API uses Temporal Cloud. Tasks can wait across worker shutdown
and project pause; resuming the project wakes retained work. Model requests and
computer operations remain individually bounded. Uncertain tool work is never
automatically replayed. Each inner task starts with a $1 model budget; an exact
`continue` reply while waiting at that budget grants another $1. Outer
conversation and Jev usage are accounted separately. See [core](core/README.md)
and [operator lifecycle](core/infrastructure/app/README.md) for the protocol and
capacity controls.

Cloud lifecycle changes in source require separate deployment. Local validation
does not update the deployed services, worker images, or Terraform state.

## Development inference limits

For local feature work, `core/infrastructure/spend/run.py` issues a key in the
same development workspace. Feature chats start at $0.25 and can receive
$0.25 increments, up to $1 total, when a subsequent command observes a new source
fingerprint after at least 70% of the existing allowance is accounted for.
This detects repeated attempts without code changes; it does not judge code
quality. Nori's controlled
model calls share $2/day and $20/month UTC limits. Jev is excluded
at Austin's request. Existing production keys are separate.

An explicit approval can raise one existing chat's allowance up to $2. The
operator records `approved_allowance_nanos` on that chat's existing spend record
and updates its allowance and native key limit together. This preserves spent
amounts, reservations and expiry. Automatic increases still stop at $1, and
shared daily/monthly limits remain unchanged.

Use the main checkout's mode-600 root `.env`; never copy it into a worktree:

```sh
python3 core/infrastructure/spend/run.py --env /absolute/main/.env \
  --task STABLE_CHAT_ID --repository /absolute/feature/worktree -- COMMAND ARGUMENTS
python3 core/infrastructure/spend/run.py --env /absolute/main/.env \
  --task STABLE_CHAT_ID --status
```

Reuse the same task ID for every command/retry in a chat. `--close` disables its
native key; the issuer can reopen the same unexpired allowance without resetting
spend. Keys expire after 24 hours. Nori's OpenAI and OpenRouter adapters reserve
conservative full request bytes and maximum output in the existing Firestore
`nori-sandbox` database before dispatch. Atomic reservations share the run and
daily/monthly budgets across runtime and worker processes. Unknown outcomes
remain reserved across restarts and calendar resets.
Controlled requests reserve at the selected models' price ceilings per million
tokens: Sol $2 input/$10 output and Terra $1 input/$5 output. Sol uses standard
tier and explicit cache mode without cache writes; its $0.10/M cached-input rate
is applied only to reported cached tokens. Requests above the 272K input threshold
are refused. OpenAI token usage produces a calculated cost, including reasoning
output; OpenRouter reports cost. Missing or incompatible usage keeps the full
reservation. Pricing checked October 31, 2026 against the
[Sol model reference](https://developers.openai.com/api/docs/models/gpt-6.1-sol).
Jev remains on its existing direct adapter and does not block budget readiness.

Run `python3 core/infrastructure/spend/e2e.py` for two no-cost native-key lifecycle
journeys covering retry/cleanup and feature progress grants. External cloud and
OpenRouter APIs are simulated; these checks do not establish live inference.

Local commands receive these controls through the wrapper. Arbitrary calls made
with existing shared keys are not intercepted. OpenRouter native key limits apply to other
tools given its capped key. Direct OpenAI uses the project key and has no native
per-chat cap; its budget enforcement is in Nori's adapters. The wrapper passes
that key only from the main root `.env`, without management credentials. CLI code review uses the selected subscription route.
