# Local dependencies

From the repository root, `make deps` starts Postgres 18 and Temporal's development
server with Docker Compose. Both bind only to loopback and keep data in named
volumes. `make deps-stop` stops them without removing data. These are development
services, not production configuration.

Configure the main checkout's gitignored, mode-600 `.env`:

```dotenv
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
