# Local dependencies

From the repository root, `make deps` starts Postgres 18 and Temporal's development
server with Docker Compose. Both bind only to loopback and keep data in named
volumes. `make deps-stop` stops them without removing data. These are development
services, not production configuration.

Configure the main checkout's gitignored, mode-600 `.env`:

```dotenv
NORI_CLOUD=local
NORI_STORE_BACKEND=postgres
NORI_POSTGRES_URL=postgres://nori@127.0.0.1:15432/nori?sslmode=disable
TEMPORAL_LOCAL=true
TEMPORAL_ADDRESS=127.0.0.1:17233
TEMPORAL_NAMESPACE=default
TEMPORAL_TASK_QUEUE=nori-local
NORI_LOCAL_ARTIFACT_DIR=/absolute/path/to/local-artifacts
```

The local database uses trust authentication on the loopback port. A hosted
Postgres connection needs its own credentials and TLS settings in the same private
env file. Postgres storage does not require Google credentials; other selected
services may still do so. Keep the existing model keys and model choices. Provision
a user and set `NORI_LOCAL_EMAIL` as described in [core setup](../../README.md#users),
then run `make dev NORI_ENV_FILE=/absolute/path/to/main/.env`. The Temporal UI is at
[localhost:18233](http://127.0.0.1:18233). The task worker and computer are separate
processes; `make deps` starts only the two dependencies.

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

Event streams use 250 ms polling in batches of 200. The durable sequence is the
resume cursor, and idle streams hold no database connection. This is sufficient
for the current personal app and avoids a second notification/reconnect path.
Firestore continues using snapshot listeners. Changing the backend setting does
not copy data between databases; existing production records remain in Firestore.

## Chat-only runtime

Set all five feature flags to `false` in the main checkout's `.env`:

```dotenv
NORI_BACKGROUND_TASKS_ENABLED=false
NORI_JEV_ENABLED=false
NORI_COMPUTER_ENABLED=false
NORI_CALENDAR_OFFERS_ENABLED=false
NORI_SCHEDULES_ENABLED=false
```

Keep `NORI_CLOUD=local`, the Postgres settings and `NORI_LOCAL_ARTIFACT_DIR` from
above. Only `OPENAI_API_KEY` is required for models; keep the prescribed outer model.
Temporal settings, Jev and OpenRouter keys are unnecessary in this mode. Start only
Postgres with `docker compose -f core/infrastructure/local/compose.yaml up -d postgres`,
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
