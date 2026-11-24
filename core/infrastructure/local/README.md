# Local dependencies

From the repository root, `make deps` starts Postgres 18 with Docker Compose. It binds only to loopback and keeps data in a named volume. `make deps-stop` stops it without removing data. This is a development service, not production configuration.

Configure the main checkout's gitignored, mode-600 `.env`:

```dotenv
NORI_CLOUD=local
NORI_STORE_BACKEND=postgres
NORI_POSTGRES_URL=postgres://nori@127.0.0.1:15432/nori?sslmode=disable
NORI_LOCAL_ARTIFACT_DIR=/absolute/path/to/local-artifacts
```

The local database uses trust authentication on the loopback port. A hosted
Postgres connection needs its own credentials and TLS settings in the same private
env file. Keep the existing model keys and model choices. Provision
a user and set `NORI_LOCAL_EMAIL` as described in [core setup](../../README.md#users),
then run `make dev NORI_ENV_FILE=/absolute/path/to/main/.env`. `cmd/nori-local`
runs both the HTTP API and the unified Postgres-native worker pool in one process.

Postgres applies numbered files in `core/internal/state/postgres/migrations/`
automatically when opening the database. Concurrent startups serialize migration
work with a transaction-scoped advisory lock. Applied filenames are recorded in
`nori_migrations`; add a new file for a schema change instead of editing one that
has shipped. Startup never resets the operator pause.

Records use collection path plus document ID as their primary key and retain the
existing saved field names in JSONB. The shared persistence code owns leases,
write claims and event heads. Postgres uses serializable transactions and retries
serialization conflicts. Event numbers live in transactional records, not SQL
sequences, so a rolled-back transaction does not leave a gap.

Event streams fan out over a shared Postgres `LISTEN nori_watch` connection with a 20 s fallback poll in batches of 200. The durable sequence is the resume cursor.

## Chat-only runtime

Set both feature flags to `false` in the main checkout's `.env`:

```dotenv
NORI_BACKGROUND_TASKS_ENABLED=false
NORI_JEV_ENABLED=false
```

Keep `NORI_CLOUD=local`, the Postgres settings and `NORI_LOCAL_ARTIFACT_DIR` from
above. Only `OPENAI_API_KEY` is required for models; keep the prescribed outer model.
Jev and OpenRouter keys are unnecessary in this mode. Start Postgres with `make deps`,
provision a user, then run `go run ./cmd/nori-local` from `core/`. Set `NORI_ENV_FILE`
to the main checkout's existing env file when working in a worktree.

The local loop processes accepted chats and resumes saved work after a restart.
The runtime accepts trusted server requests at `/users/<id>/v1/conversations`;
use the existing web proxy or your host's authenticated backend. It deliberately
rejects direct browser requests. Assistant routes return `501` with
`code: "not_enabled"`. Turning features off does not delete their records or stop
an already-running worker process; apply the same configuration to every process
and stop that deployment's worker when selecting chat-only mode. Re-enabling uses
the retained work. Chats remain separate from assistant tasks in either mode.
