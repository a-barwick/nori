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
The local runner reads `OPENROUTER_API_KEY` and `JEV_API_KEY` from the existing
root `.env` by default. `NORI_ENV_FILE` selects an existing configuration file;
`NORI_TASK_ENV_FILE` selects a separate existing task configuration when needed.
Neither setting copies or moves credentials. Google Cloud access uses Application Default Credentials.
Use `make dev NORI_ENV_FILE=/absolute/path/to/the/existing/.env` to select a different
file or use a worktree without copying credentials. Keep the credential file
gitignored and mode 600. Required names and optional model settings are in
[.env.example](.env.example). The default model is `anthropic/claude-opus-5.5`.
Austin selected Claude Opus 5.5 for conversation, `jev-latest` for intent hints,
and `gemini-3.8-flash` on Vertex for inner tasks. Agents must preserve these
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
recent conversation, native tool-call results, and the `dispatch` tool.
Classification remains advisory. Nearby messages are coalesced; new input can
supersede an unfinished answer up to three times. The fourth attempt finishes
its fixed input batch, leaving later messages for the next turn. The runner
rechecks accepted input before dispatch and publication, and preserves already
accepted actions when an answer is superseded.

Dispatch reuses Firestore task acceptance and Temporal delivery. It returns task
identity without waiting for execution. The existing worker executes tools and
saves results. Ordinary chat stays available during a task. Stop and the panel's
explicit task-update form target that task directly; a chat message does not
automatically become a task update. One task can be active at a time. Paused task
execution does not disable conversation.

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
[core setup](core/README.md). This source revision has not been deployed. Hosted
rollout needs the queue/IAM configuration, server OpenRouter/Jev credentials, and
verification of native Runtime retry/status forwarding. See
[POC operations](core/infrastructure/poc/README.md) for the existing deployment.

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
