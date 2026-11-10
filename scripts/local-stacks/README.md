# Local assistant stacks

Two local stacks for integration work. Both use the Compose Postgres and Temporal from `make deps`. The host needs neither `psql` nor the `temporal` CLI: the script runs both inside the containers. Keys are read from the main checkout's `.env` through `NORI_ENV_FILE` and are never copied or printed. Process environment wins over the file, so the script's settings override `.env`.

| Stack | Core | Web | Database | Namespace / queue | Providers | Credential vault |
| --- | --- | --- | --- | --- | --- | --- |
| `recorded` | 8083 | 3214 (optional) | `nori_integration` | `nori-integration` / `nori-tasks-integration` | hand-written recordings (`contracts/assistant/recordings`), made-up Google client | `~/.nori-stacks/recorded/artifacts/credentials` |
| `real` | 8085 | 3213 → 8085 | `nori_real` | `nori-real` / `nori-tasks-real` | real providers, no replay | `~/.nori-stacks/real/artifacts/credentials` (new, empty on 2026-11-10) |

Users: recorded has `integration-a@`, `integration-b@`, `scenario-a@` and `scenario-b@nori.local`; real has `real-a@nori.local` (`66666666-…`). The web signs in as the local test user, so no personal address appears in any command or file.

## Start from nothing

```bash
make deps
```

```bash
scripts/local-stacks/stack.sh recorded setup && scripts/local-stacks/stack.sh real setup
```

Then, for each stack, run each of these in its own terminal or as a background job. Logs are up to you; the integration session writes them to `~/.nori-stacks/<stack>/{core,worker,web}.log`.

```bash
scripts/local-stacks/stack.sh recorded core
```

```bash
scripts/local-stacks/stack.sh recorded users
```

```bash
scripts/local-stacks/stack.sh recorded worker
```

```bash
scripts/local-stacks/stack.sh recorded set-current
```

```bash
scripts/local-stacks/stack.sh recorded check
```

Replace `recorded` with `real` for the real stack, and add `scripts/local-stacks/stack.sh real web` for 3213.

`nori-local` does not host the task worker. Without a worker whose build id (`int-<sha>` or `real-<sha>`) is set current, tasks are accepted but never run. After a code change, restart the worker and run `set-current` again. Running workflows stay on the build that started them.

`stop` stops only that stack's ports and the workers polling its namespace. Other sessions run their own stacks on this machine, so never stop by program name (`pkill -f nori-local` kills their cores too).

```bash
scripts/local-stacks/stack.sh real stop
```

## Without model keys

Core refuses to start without `OPENAI_API_KEY`, and the worker refuses without `OPENROUTER_API_KEY`. `NORI_STACK_NO_MODEL=1` fills either one with a placeholder only if `.env` has no value for it. It also sends all outbound HTTP(S) to a dead local proxy, so nothing leaves the machine. API scenarios run; model turns and real provider calls fail. It checks plumbing and is never evidence about a model or provider.

## Machine-local files (not in the repository)

- `~/.nori-stacks/<stack>.env`, mode 600, optional. It is loaded by `core` and `worker` after `.env`. On this machine `real.env` holds the generated VAPID pair.
- `~/.nori-stacks/<stack>/artifacts/`: artifacts and the credential vault (`vault.key` is created on first use).
