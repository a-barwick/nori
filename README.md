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
`core/.env` by default. Task credentials load from the existing root `.env`, or
from `NORI_TASK_ENV_FILE`. Google Cloud access uses Application Default Credentials.
Use `make dev NORI_ENV_FILE=/absolute/path/to/the/existing/.env` to select a different
file or use a worktree without copying credentials. Keep the credential file
gitignored and mode 600. Required names and optional model settings are in
[.env.example](.env.example). The default model is `meta/muse-spark-1.3-contributor`.

The runner gets a Jev intent hint, then makes one Muse call with the original
message, full available conversation, and only the native `dispatch` tool.
Classification never selects a prompt or forces a route. The shared prompt asks
for reply text alongside tool calls; a tool-only reply is an explicit failure
before dispatch. Reply text is published only after all dispatches are accepted.
A failed dispatch produces an accurate failure reply and a paired tool result.

Dispatch reuses Firestore task acceptance and Temporal delivery. It returns task
identity without waiting for execution. The existing worker executes tools and
saves results; the current Activity panel shows them. Ordinary chat stays
available during a task. Stop and the panel's explicit task-update form target
that task directly; a chat message never automatically becomes a task update.
The existing one-active-task limit and authorized GCP session deadline still apply.

Outer-turn history stays in memory; saved task history stays in Firestore.
`NORI_CONVERSATION_SEED` can carry a saved transcript through a development restart
without re-executing its old requests. Full context is currently untrimmed, so
token cost and latency grow until provider context limits are reached. Selection,
memory, and compaction are separate features. `POST /api/turn` accepts
`{ "id": "UUID", "message": "text" }`; it and `POST /api/session` return the same
conversation/task snapshot, including `messages`, `runs`, and `dispatches`.
Repeated turn IDs reuse the recorded outcome; a different message with the same
ID is rejected. The Go listener and web preview bind to loopback. Conversation
remains available when task execution is paused.

With the local app running, opt into two live conversation checks with
`NORI_E2E_URL=http://127.0.0.1:3210 bun test ./core/e2e/conversation.test.js`.
They use the configured Jev and conversational model and do not request real work.

`NORI_LOCAL_AGENT_URL` selects this client adapter only in development. Production
continues to use the existing owner-protected runtime and task UI. `make core`
still starts the GCP-backed runtime, which uses Application Default Credentials
and private GCP infrastructure; see [core setup](core/README.md) and
[POC operations](core/infrastructure/poc/README.md).
