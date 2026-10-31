# Nori

Nori is a personal assistant you text. This repository holds the whole product:

- [core](core/README.md): the Go agent service and its infrastructure.
- [web](web/README.md): the Next.js desktop web interface.

Design:

- [Client contract](docs/client-contract.md): the protocol every client uses to talk to the agent service.
- [Separation of concerns](docs/separation-of-concerns.md): what the service owns, what clients own.

## Run

```sh
make dev     # local conversation on http://127.0.0.1:3210, Go runner on :8080
make check   # Go vet and gofmt, then TypeScript and ESLint
```

Install web's dependencies first with `cd web && bun install --frozen-lockfile`.
The local runner reads `OPENAI_API_KEY`, `OPENROUTER_API_KEY` and `JEV_API_KEY` from the existing
root `.env` by default. `NORI_ENV_FILE` selects an existing configuration file;
`NORI_TASK_ENV_FILE` selects a separate existing task configuration when needed.
Neither setting copies or moves credentials. Google Cloud access uses Application Default Credentials.
Use `make dev NORI_ENV_FILE=/absolute/path/to/the/existing/.env` to select a different
file or use a worktree without copying credentials. Keep the credential file
gitignored and mode 600. Required names and optional model settings are in
[.env.example](.env.example). The default conversation model is `gpt-6.1-sol`, low reasoning, through the
OpenAI platform API at standard tier. Austin selected this for conversation, `jev-latest` for intent hints,
and `openai/gpt-6.1-terra` through OpenRouter Responses for inner tasks. Agents must preserve these
choices; the model policy in [AGENTS.md](AGENTS.md) also applies to overrides.

Both Go entrypoints use the same durable conversation runner. Each message is
saved to Firestore before acceptance; model processing continues independently
of the browser. Local mode checks pending work at startup and uses an application
loop to recover after restart. Hosted mode schedules a Cloud Tasks notification
and processes it through a request-bound handler, so scale-to-zero does not
require a resident worker for conversation.

The first load of an empty conversation queues one introduction through that
same inbox. Nori generates a short, warm greeting with the real outer model;
the saved reply is reused on reload. The bootstrap instruction is internal and
does not appear as a message Austin sent. The composer stays disabled until the
greeting is saved and the system is ready. Inputs arriving from another client
still join the normal ordered input flow. The UI shows connection and
preparation status until a saved reply is available.

The runner gets a Jev hint, then calls the configured conversational model with
recent conversation, native tool-call results, and task command tools.
Classification remains advisory. Nearby messages are coalesced; new input can
supersede an unfinished answer up to three times. The fourth attempt finishes
its fixed input batch, leaving later messages for the next turn. The runner
rechecks accepted input before dispatch and publication, and preserves already
accepted actions when an answer is superseded.

Dispatch reuses Firestore task acceptance and Temporal delivery. It returns task
identity without waiting for execution. The existing worker executes tools and
saves results. Several independent tasks can run concurrently, each with its own
browser tab and Answer, Update, Continue, and Stop controls in Activity. Chat can
target an existing task when the reference is unambiguous; unrelated conversation
leaves tasks unchanged. Answers and budget continuations bind to the current
question, and retries reuse command receipts. Paused task execution does not
disable conversation.

Task questions and results enter the durable conversation inbox. Notification-only
turns cannot issue task commands. A newly accepted user message holds task-result
publication until routing completes, so an in-flight result cannot ignore a
pending correction. Task browser tabs share the retained profile's cookies and
storage; they provide cooperative ownership, not security isolation.

`POST /api/turn` accepts `{ "id": "UUID", "message": "text" }` and returns HTTP
202 with a session snapshot after durable acceptance and wake confirmation.
Retry an interrupted request with the same UUID; different text with that UUID
is rejected. `POST /api/session` restores saved history and pending state, and
supports a `before` cursor for older messages. The UI shows conversation work
and recorded model attempts, actions, and provider usage; task steps load on
demand. Provider cost stays unknown when no price is supplied.

Firestore holds the transcript, processing state, dispatch records, and traces.
Model context uses bounded recent history, with truncation shown in the attempt
trace. Browsing older messages does not expand model context; memory and
compaction remain separate features. The Go listener and web preview bind to
loopback. Defaults still select the configured GCP database and Temporal service;
use explicit Firestore and Temporal emulator settings for an isolated local run.

Two live conversation E2E journeys cover the generated greeting, asynchronous
acceptance, UUID replay/conflict handling, rapid follow-up ordering and
hypothetical replies without task dispatch. Point `NORI_E2E_API_URL` at a running
local Go API with a fresh isolated Firestore database, or `NORI_E2E_URL` at its
local web proxy, then run `bun test core/e2e/conversation.test.js`. They use the
selected real providers; do not point them at a saved personal conversation.
The older durable-task suite still targets the retired direct-run route and
does not validate this revision's conversation contract.

`NORI_LOCAL_AGENT_URL` selects the local adapter only in development. Production
keeps the owner-protected proxy and runtime IAM. `make core` starts the hosted
entrypoint locally and additionally requires Cloud Tasks configuration; see
[core setup](core/README.md). Hosted deployment uses the configured conversation
queue and scoped caller IAM, with OpenRouter/Jev credentials supplied through
native secret references. Follow [POC operations](core/infrastructure/poc/README.md)
for the operator deployment procedure.

## Durable tasks

The deployed task API uses Temporal Cloud. Tasks can wait across worker shutdown
and project pause; resuming the project wakes retained work. Model requests and
computer operations remain individually bounded. Uncertain tool work is never
automatically replayed. Each inner task starts with a $1 model budget; an exact
`continue` reply while waiting at that budget grants another $1. Outer
conversation and Jev usage are accounted separately. See [core](core/README.md)
and [operator lifecycle](core/infrastructure/poc/README.md) for the protocol and
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
