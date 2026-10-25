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
The local runner reads `OPENROUTER_API_KEY` from the existing `core/.env` by default.
Use `make dev NORI_ENV_FILE=/absolute/path/to/the/existing/.env` to select a different
file or use a worktree without copying credentials. Keep the credential file
gitignored and mode 600. Required names and optional model settings are in
[.env.example](.env.example). The default model is `meta/muse-spark-1.3-contributor`.

The runner makes one model call per message and exposes only `dispatch`. It keeps
one conversation and accepted work requests in memory; restarting clears them.
Accepted requests are visible in the web Requests panel and await a future task
executor. They are not executed by this runner. `POST /api/turn` accepts
`{ "id": "UUID", "message": "text" }`; it and `POST /api/session` return the same
`{ messages, dispatches }` snapshot. Identical successful turn IDs return the
current snapshot without calling the model again. A different message with the
same ID is rejected. The Go listener and web preview bind to loopback.

`NORI_LOCAL_AGENT_URL` selects this client adapter only in development. Production
continues to use the existing owner-protected runtime and task UI. `make core`
still starts the GCP-backed runtime, which uses Application Default Credentials
and private GCP infrastructure; see [core setup](core/README.md) and
[POC operations](core/infrastructure/poc/README.md).
