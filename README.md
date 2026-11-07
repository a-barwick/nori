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

Provision the sign-in email in the users table before starting the app, and set
`NORI_LOCAL_EMAIL` for loopback development. Existing installations must import the
current user's settings before deploying this version; [user setup](core/README.md#users)
includes the migration command. Retained records stay at their existing paths.

Start with the existing main checkout's configuration, including from a worktree:

```sh
make dev NORI_ENV_FILE=/absolute/path/to/main/.env
```

Use the project's ordinary provider keys. Do not copy credentials into worktrees.

With the development stack running, open `http://127.0.0.1:3210/`. Independent
chats are in the side panel's Chats tab, next to Activity.

Each chat saves a separate conversation, streamed replies, assigned files and recorded tool outcomes. Its foreground reader accepts CSV files up to 64 KiB, although the upload
limit is 10 MiB. It has a finite saved-context limit and does not include assistant
history, tasks or memories. Use the assistant for background work, shell, browser,
PDF or image analysis. See [Chats](docs/site/chat-client.html).

## Durable tasks

The deployed task API uses Temporal Cloud. Tasks can wait across worker shutdown
and project pause; resuming the project wakes retained work. Model requests and
computer operations remain individually bounded. Uncertain tool work is never
automatically replayed. Each new background task records the ceiling from
`NORI_TASK_BUDGET_USD` (default $1), shared with its subtasks. The limit is checked
between calls, so a call can exceed it. Reaching it ends the task incomplete
without another approval prompt. Missing provider cost stays unknown without
invalidating a successful result. Outer conversation and Jev usage are reported
separately. See [core](core/README.md)
and [operator lifecycle](core/infrastructure/app/README.md) for the protocol and
capacity controls.

Cloud lifecycle changes in source require separate deployment. Local validation
does not update the deployed services, worker images, or Terraform state.

## Model usage

Model callers share `usage.Meter`. `NORI_USAGE_METER=store` (default) records usage
in the selected database. Set it to `http` with `NORI_USAGE_ENDPOINT` and
`NORI_USAGE_TOKEN` to also send usage metadata to a host. The host receives
`{user_id, usage}` before dispatch (`pending`) and after the response, and upserts
by `(user_id, usage.id)`. A missing cost stays unknown. No prompts or files are sent.

Host admission failure prevents the model call. Final host delivery failure is
logged while the result and local usage record survive; there is no automatic
host-delivery retry queue. Hosts can reconcile from the durable records. Go hosts
can implement `usage.Meter` and select it with `usage.WithHost`/`usage.Wrap` at
startup. The workflow still enforces `NORI_TASK_BUDGET_USD` independently.

Provider usage remains recorded. Development commands use ordinary project keys;
there is no temporary-key issuer, per-chat allowance, progress fingerprint,
shared development budget database, or request reservation. Provider account
limits still apply independently. Changing local code does not change those
account settings or deployed services.
