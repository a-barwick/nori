# Nori core

The Go backend owns durable conversation handling, background task admission and execution, permission enforcement, scheduling, and MCP tool execution on top of Postgres. `cmd/nori` runs the HTTP API, the unified Postgres-native worker pool, or both (`--role=all|api|worker`, default `all`), and `cmd/nori-local` runs the loopback development equivalent. Production uses OpenAI directly (or a deployment-owned OpenAI-compatible model gateway) for replies, task reasoning, image inspection, and conversation summaries, with optional OpenRouter and advisory Jev classification. Hosted AWS deployments use S3 for artifacts, AWS Secrets Manager for secrets, OIDC user sign-in at the web tier, and HS256 service tokens between trusted services.

Storage sits behind contracts. `internal/state` defines records, `ConversationStore`, `TaskControl`, and write rules; `internal/state/records` applies them over `internal/state/postgres` serializable transactions with numbered migrations, transactional wake rows (`nori_task_leases`, `nori_user_wakes`), and post-commit `LISTEN/NOTIFY`. File and upload bytes live in `internal/state/s3` (`NORI_CLOUD=aws`) or `internal/state/localfs` (`NORI_CLOUD=local`). `internal/usage` computes reported model costs. Only `internal/platform` wires concrete backends: `platform.OpenStore(ctx, settings, user)` returns an immutable user-scoped `state.ConversationStore`, and every data operation keeps that scope. The operator pause remains global.

## Binaries

| Binary | Entrypoint | Role |
| --- | --- | --- |
| `nori` | `cmd/nori` | Unified hosted runtime (`NORI_CLOUD=aws` default, `--role=all\|api\|worker` / `NORI_ROLE`). Runs the HTTP API (`api`), the Postgres-native worker pool (`worker`), or both (`all`). |
| `nori-local` | `cmd/nori-local` | Loopback development runtime (`NORI_CLOUD=local` default, binds `127.0.0.1`). Runs both the HTTP API and the unified Postgres-native worker pool in-process. |
| `nori-user` | `cmd/nori-user` | Provisions users, manages invites, and binds an existing user to a verified sign-in identity. |
| `contracts-check` | `cmd/contracts-check` | Validates `contracts/assistant` JSON Schema 2020-12 definitions, fixtures (with strict Go round-trips), and scenarios. |

## Local development

Requires Go 1.26, Docker Compose (for Postgres), and configured model credentials.

From the repository root:

```sh
make deps   # starts isolated loopback Postgres 18
make dev    # starts loopback web and cmd/nori-local
```

See [local setup](infrastructure/local/README.md) and [infrastructure overview](infrastructure/README.md) for details.

From `core/`, run binaries directly:

```sh
go run ./cmd/nori-local
go run ./cmd/nori --role=all
go run ./cmd/contracts-check -strict
```

`make -C core run` starts `cmd/nori` (hosted mode), `make -C core build` builds `bin/nori`, and `make -C core check` runs `go vet` and `gofmt` checks.

Commands load the monorepo-root `.env` without overriding existing environment variables. Keep `.env` gitignored and mode `600`. `NORI_ENV_FILE` selects an explicit env file; `NORI_TASK_ENV_FILE` optionally layers task-specific settings. Every setting is documented in [`.env.example`](../.env.example).

## Users

The user directory stores each user's sign-in email, display name, IANA time zone, and verified sign-in identity. The hosted web server verifies the user's signed assertion, resolves the issuer and subject through `POST /api/users/resolve` using a signed service token, and prefixes upstream requests with `/users/<id>/`. Native Google sign-ins resolve against the same Google Account identifier. Loopback development uses `NORI_LOCAL_EMAIL` and requires a matching provisioned user row.

Provision a user from `core/`:

```sh
go run ./cmd/nori-user --email user@example.com --name 'User' --timezone America/Los_Angeles
```

Existing Gmail accounts can bind automatically at sign-in with a verified Google identity. For other existing accounts, verify the original account owner through a trusted channel and obtain the issuer and subject from the verified identity provider. Run the binding command from `core/` with the existing Nori user ID.

```sh
go run ./cmd/nori-user bind-identity --id "$nori_user_id" --issuer "$verified_issuer" --subject "$verified_subject"
```

Use `https://accounts.google.com` as the issuer for Google Account identities. The command rejects an identity already associated with another account and preserves an existing binding. An email address alone is insufficient to establish ownership of a legacy account.

Each user gets an isolated Postgres record scope, `users/<id>/` artifact prefix, and user-keyed lease/wake rows in `nori_task_leases` and `nori_user_wakes`. (`owner` is reserved for legacy unprefixed data paths.) Worker reconciliation enumerates users and opens bounded LRU-cached per-user `App` scopes.

## Code layout

Processes are assembled in `internal/platform` and `internal/app`. Infrastructure boundaries sit behind small contracts so storage, leases/wakes, and secrets remain decoupled from core logic.

| Package | Role |
| --- | --- |
| `internal/api` | User-scoped HTTP routes (`/v1/*` assistant and management APIs, `/api/*` internal/turn routes); no infrastructure imports. |
| `internal/app` | Composes per-user `App` instances, LRU/TTL caching (`Service`), lease repair (`RepairLeases`), scheduling ticks, and answer redelivery. |
| `internal/platform` | The single wiring layer for Postgres, S3/localfs, Secrets Manager, model clients, and service auth. |
| `internal/settings` | Sole reader of environment variables, loaded and validated once at startup. |
| `internal/conversation` | Outer conversation runner, inbox ordering, leased turns, compaction, Jev intent classification, and task admission. |
| `internal/task` | Postgres-native inner task state machine (`Runner`), step activities, durable tool execution, completion verification, and paused-stop recovery (`StopPaused`). |
| `internal/worker` | Unified Postgres-native worker pool (`Pool`) driving `LISTEN nori_wake`, inner task leases, outer conversation/scheduler lanes, periodic repair, and graceful drain. |
| `internal/agent` | Built-in runtime tools (`web_search`, `upload_inspect`, `result_*`, `memory_read`, `conversation_*`, `capability_load`), capability catalog, and `agent/responses` client. |
| `internal/scheduler` | Recurring automations, goals, occurrences, and inbox item scheduling. |
| `internal/permissions` | Permission rules (`allow`/`deny`), action authorization checks, approvals, and pending user interactions (approval cards, questions, sign-in prompts). |
| `internal/connectors/mcp` | Streamable HTTP and local-dev stdio MCP client, tool/resource/prompt discovery, OAuth/bearer auth, and execution adapter. |
| `internal/connectors/nori` | Nori's built-in internal tools adapter for automations and goals. |
| `internal/connections` | User connection port/registry, preferences, and integration catalog contract. |
| `internal/extensions` | Sealed tool definitions, JSON Schema 2020-12 compilation/projection, and combined tool inventory registry. |
| `internal/invoke` | Adapter contract (`Adapter`, `Reconciler`, `Canceller`, `SignInChecker`), deterministic action digesting, and adapter registry. |
| `internal/state` | Core record types, `ConversationStore` / `ResourceStore` / `ArtifactStore` contracts, `state/records` transaction rules, `state/docstore`, `state/postgres` (with numbered migrations), `state/s3`, and `state/localfs`. |
| `internal/wake` | `Waker` contract, in-process waker, and `Repairer` over `WorkVersion`. |
| `internal/secret` | Secret lookup contract, `secret/rowstore` (AES-256-GCM envelope-encrypted Postgres credential rows), and `secret/awssecrets` (AWS Secrets Manager). |
| `internal/serviceauth` | HS256 signed service-to-service JWT minting and verification for hosted APIs and wake delivery. |
| `internal/netguard` | Outbound URL validation and SSRF-safe HTTP transport rejecting private, link-local, and metadata addresses. |
| `internal/modelgateway` | OpenAI-compatible gateway URL validation and transport helpers. |
| `internal/prompts` | Embedded system instructions, tool descriptions, and runtime templates. |
| `internal/usage` | Token tariff calculation (`openai.go`) and durable/HTTP usage metering. |
| `internal/observability` | Optional, fail-open Sentry and Arize OpenInference OTLP telemetry with credential redaction. |
| `internal/contextview`, `internal/transcript`, `internal/users`, `internal/httpjson`, `internal/httpserver` | Context budget helpers, transcript types, user directory contract, and strict HTTP/JSON servers. |

## Task ownership and execution coordination

A top-level task belongs to its user and has an independent lifetime. A subtask belongs to its `parent_id` and cannot create another level of subtasks. Work that must outlive another task is dispatched as a separate top-level task.

Completion, failure, and Stop record a finishing intent first. While finishing, execution and new subtasks are fenced while Stop and follow-up holds remain available. The terminal event and answer publish only after owned child tasks have ended and cleanup has succeeded. Cleanup errors keep the task open with `cleanup_error` so recovery retries cleanup without replaying model or tool calls.

Committed Postgres records are the source of truth for pending work; `nori_task_leases`, `nori_user_wakes`, and post-commit `pg_notify('nori_wake', ...)` wake the worker pool to advance it:

| Authority | Responsibility |
| --- | --- |
| Conversation inbox and leased turn | Order user input and task events; fence stale replies and commands. |
| Task commands and run revision | Accept instructions atomically in the same transaction as `nori_task_leases` wakes. |
| Authoritative `TaskControl` (`nori_records`) | Own monotonic task fencing (`Owner`/`Fence`/`LeaseUntil`), durable execution phase, and atomic spend + cursor commits (`AdvanceTaskStep`). |
| Discovery leases (`nori_task_leases` / `nori_user_wakes`) | `READ COMMITTED` `FOR UPDATE SKIP LOCKED` work discovery, `wake_seq` race guards, and post-commit `LISTEN/NOTIFY` fanout. |
| Publication transaction | Commit a task outcome and its conversation event together once routing and child-task conditions allow it. |
| Unified worker pool (`internal/worker`) + `app.RepairLeases` | Execute inner tasks, outer conversation turns, and scheduler ticks; run periodic lease repair and `StopPaused`. |

`WorkVersion` derives wake demand directly from committed records (unleased turns, pending commands, eligible publications, terminal cleanup, and paused Stops). Activities commit outcomes without queue I/O, and failed wakes remain retryable without turning a committed task result into a failure.

## Tool execution, MCP, and permissions

All external and built-in tools are represented as sealed `extensions.Definition` records and invoked through `internal/invoke` under durable operation receipts (`internal/task`).

- **MCP servers (`internal/connectors/mcp`):** Users register Streamable HTTP MCP servers (with `none`, `configured` bearer credential, or `oauth` auth) or, when `NORI_CLOUD=local`, declared stdio commands from `NORI_MCP_COMMANDS`. Discovery runs outside workflow code via `POST /v1/mcp/{id}/actions` (`connect` / `reconnect`) and exposes tools as `mcp_<server>__<tool>`.
- **Permissions (`internal/permissions`):** Every external call is prepared and checked against explicit `deny` rules, recorded approvals for `(run_id, revision, action_digest)`, matching `allow` rules, and task origin policy. External MCP tools default to `unknown` effect (server annotations are advisory only), so unruled MCP calls open an `approval` interaction card and pause the workflow until the user approves or declines via `POST /v1/interactions/{id}/responses`.
- **Credential storage (`internal/secret/rowstore`):** Setting `NORI_CREDENTIAL_STORE=database` stores connection and MCP credentials in Postgres encrypted with AES-256-GCM envelope encryption (AAD bound to `owner:connectionID`), wrapped with `NORI_CREDENTIAL_KEY`, `NORI_CREDENTIAL_KEY_SECRET` (AWS Secrets Manager), or `NORI_CREDENTIAL_KEY_FILE` (`NORI_CLOUD=local` only).

### Private Google Workspace testing

1. In a dedicated Google Cloud test project, enable the Calendar, Drive, Picker, Docs, Sheets, and Gmail APIs. In Google Auth Platform, use an external app in **Testing**, list the Google accounts that will grant access, and create a Web application OAuth client. Register `https://<test-web-host>/api/v1/connections/oauth/callback` as an authorized redirect URI. For a loopback Web test, use the exact `http://localhost:<port>` callback URL instead.
2. Configure `NORI_GOOGLE_WORKSPACE_CLIENT_ID`, `NORI_GOOGLE_WORKSPACE_CLIENT_SECRET`, and `NORI_GOOGLE_WORKSPACE_REDIRECT_URL` on Core. Set `NORI_GOOGLE_WORKSPACE_TEST_USERS` to the comma-separated **Nori sign-in emails** allowed to see the feature. Give API and worker services the same settings and configured Postgres credential encryption. The Web deployment needs its normal authenticated Core route. Keep the Google client secret on Core.
3. In Nori on Web and iOS, open **More → Apps & MCP → Connect Google**. Enable the services you want to exercise. Use **Choose files** for Google Docs, Sheets, and PDFs. The picker grants access to the files you choose so they can be referenced by name in later requests. Use designated test calendars and email recipients for tests that change or send data. Google Testing grants may expire after seven days. Use the connection's reconnect action to continue testing.

The Google callback uses a signed, expiring OAuth state to reach the correct user when an iOS browser session lacks a Web sign-in cookie. Core still checks the user's stored attempt and PKCE verifier. The picker uses its own `drive.file`-only authorization and the browser callback carries the result status. A Gmail send needs an approval for that specific send, including its recipients, body, and attachments.

## Managed model context

Saved history is immutable and authoritative. Model requests use bounded derived views maintained at execution boundaries without rewriting messages or task history:

- **Assistant conversation:** Before a reply, oldest-first extractive compaction catches up with the recent window without a model call, keeping the extractive index separate from idle semantic refinement.
- **Tasks:** Large tool outputs become operation references (`operation_read`); older call/result rounds compact into an evidence index while preserving user instructions, resolved questions/answers, loaded capabilities, and a recent suffix (`task_history_read` and `conversation_read` retrieve original sources on demand).

Application input budgets remain conservative byte caps (`40 KiB` assistant, `128 KiB` task view, `192 KiB` encoded request cap). An explicit provider context rejection allows at most one smaller retry; ordinary provider errors do not replay side effects.

## Pricing and task budgets

`internal/usage/openai.go` calculates standard-tier direct OpenAI tariffs (with implicit cache writes disabled). Cached prompt tokens are billed at **1/20th of the input token price**:

| Model | Input | Cached input (`1/20th`) | Output (incl. reasoning) | Notes |
| --- | --- | --- | --- | --- |
| `gpt-6.1-sol` | `$2.00 / 1M` | `$0.10 / 1M` | `$10.00 / 1M` | Standard context up to 272,000 input tokens; above threshold stays unknown. |
| `gpt-6.1-terra` | `$1.00 / 1M` | `$0.05 / 1M` | `$5.00 / 1M` | Up to 272,000 input tokens. |
| `gpt-6.1-terra` (long context) | `$2.00 / 1M` | `$0.10 / 1M` | `$7.50 / 1M` | Applies to the entire request when input exceeds 272,000 tokens (up to 1,050,000). |

New background tasks record `NORI_TASK_BUDGET_USD` (default `$1`) at acceptance, shared across the task and its subtasks. When cumulative family spend reaches or exceeds the recorded budget (`Spent + Others >= Budget`), the workflow **stops and fails incomplete** (`status: "failed"`, `expected_incomplete: true`) without issuing another model call and without opening a `continue` budget question. Outer conversation and Jev calls are metered separately and do not consume a task's budget. Missing or unmatched provider usage remains unknown cost without failing successful work.

## Configuration

`internal/settings` loads every setting once at startup; see [`.env.example`](../.env.example) for the full list.

Keep credentials in a private `.env` file or the deployment's secret manager. From a worktree, set `NORI_ENV_FILE` to the absolute path of the private file. `NORI_TASK_ENV_FILE` defaults to the same file. With `make deps`, the local Postgres URL is `postgres://nori@127.0.0.1:15432/nori?sslmode=disable`. If the main connection uses a transaction pooler, set `NORI_POSTGRES_LISTEN_URL` to a direct Postgres connection for wake notifications.

Users add keys from **More → Models** for OpenAI, Anthropic, Google, Vercel AI Gateway, or OpenRouter. Nori encrypts each key in the user's credential store. Chat and Work use account-wide model and reasoning settings. A missing key stops calls for that lane until the user replaces it or chooses another configured model.

Configure the encrypted credential store in both API and worker deployments. Existing user-owned Vercel AI Gateway keys continue to work. Accounts using deployment credentials need to save a user-owned key.

Postgres records migrations by filename and checksum. Applied migrations keep their original filenames, including `002_indexes.sql` and `002_sequences.sql`, which run in lexical order. Run `nori --migrate-only` before rolling out a new image when using an explicit migration job. Existing ledgers receive their first checksums on upgrade. New unattached uploads expire after seven days and incomplete uploads are eligible for cleanup after one day. Attached files and uploads saved before this change retain their existing lifecycle.

| Configuration | Purpose |
| --- | --- |
| `NORI_CLOUD`, `NORI_ROLE`, `AWS_REGION` | `aws` (default for `cmd/nori`) or `local` (default for `nori-local`); `NORI_ROLE` (`all`\|`api`\|`worker`); see [AWS adapters](infrastructure/aws/README.md). |
| `NORI_STORE_BACKEND`, `NORI_POSTGRES_URL` | Postgres (`postgres`) is the sole database backend; numbered migrations run automatically on startup. |
| `ARTIFACT_BUCKET`, `NORI_LOCAL_ARTIFACT_DIR` | Private S3 bucket (`aws`) or local private directory (`local`, requires loopback Postgres). |
| `NORI_SERVICE_TOKEN_KEY`, `NORI_SERVICE_AUDIENCE` | Shared HS256 secret (>= 32 bytes) and audience (default `nori`) authenticating hosted web/API calls. |
| `JEV_API_KEY`, `EXA_API_KEY` | Deployment credentials for Jev classification and Exa search. |
| `NORI_BACKGROUND_TASKS_ENABLED`, `NORI_JEV_ENABLED`, `NORI_TASK_BUDGET_USD` | Feature toggles and per-task USD spending ceiling (default `1`). |
| `NORI_CREDENTIAL_STORE`, `NORI_CREDENTIAL_KMS_KEY`, `NORI_CREDENTIAL_KEY`, `NORI_CREDENTIAL_KEY_SECRET`, `NORI_CREDENTIAL_KEY_FILE`, `NORI_OAUTH_REDIRECT_URL`, `NORI_MCP_COMMANDS` | Encrypted Postgres credential store (`database`), OAuth callback URL, and local-only stdio MCP command map. |
| `NORI_AUTOMATION_UNATTENDED`, `NORI_USER_SENDS` | Permission policies (`confirm_writes` / `require_rule`; `allow` / `ask`). |
| `NORI_USAGE_METER`, `NORI_USAGE_ENDPOINT`, `NORI_USAGE_TOKEN` | Usage metering (`store` or `http` webhook). |
| `BRAVE_SEARCH_API_KEY` | Enables built-in `web_search`; omitted reports search unavailable. |
| `BIND_HOST`, `PORT` | Hosted API bind address (default `127.0.0.1:8080`); `nori-local` always binds `127.0.0.1`. |

## API routes

User-scoped routes are mounted under `/users/{id}/` (resolved by the trusted web server via `POST /api/users/resolve`). Unscoped `GET /healthz` and `POST /api/healthz` report process health without database reads.

| Route group | Purpose |
| --- | --- |
| `GET /v1/capabilities` | Advertises live contracts, features, and upload limits. |
| `/v1/settings/models`, `/v1/settings/model-keys*` | Account-wide model settings and encrypted provider key management. |
| `/v1/conversations/assistant` (`GET`, `messages`, `operations`, `cancel`, `events`, `uploads`) | Durable assistant conversation snapshot, history pagination, message submission, cancellation, SSE events, and uploads. |
| `GET /v1/snapshot`, `GET /v1/events`, `/v1/uploads*`, `/v1/results*` | Assistant session snapshot, SSE stream, uploads, and saved result files. |
| `/v1/mcp*` | Register/update HTTP or local stdio MCP servers, run `connect`/`reconnect`/`set_exposure`/`sign_in`/`sign_out`, store bearer credentials, and list discovered tools. |
| `/v1/permissions*`, `/v1/interactions*` | Manage allow/deny permission rules and inspect or respond to pending approval cards, questions, and sign-in prompts. |
| `/v1/work*`, `/v1/operations`, `/v1/tools*` | Inspect tasks, operations, and available tool inventory; trigger task actions (`stop`, `continue`, etc.). |
| `/v1/automations*`, `/v1/goals*`, `/v1/inbox*` | Manage scheduled automations, goals, occurrences, and inbox notifications. |
| `/v1/integrations*`, `/v1/connections*`, `/v1/preferences` | Integration catalog, user connections, OAuth callbacks, and user preferences. |
| `/v1/library*`, `/v1/search`, `/v1/memory*`, `/v1/diagnostics*`, `/v1/feedback`, `/v1/mutations/{request_id}` | Saved results library, search, memory, diagnostics, feedback, and idempotent mutation receipts. |
| `POST /api/process`, `POST /api/resume`, `POST /api/turn`, `POST /api/command`, `POST /api/trace`, `POST /api/artifact`, `POST /api/delivered`, `POST /api/seen` | Internal wake processing, operator resume, turn/command submission, task traces, artifacts, and read/delivery marks. |

Unknown JSON fields, trailing JSON objects, and invalid UUIDs are rejected. Queue failures after acceptance return HTTP `503` with `accepted: true, retryable: true` so callers can retry safely with the same request ID.

## Observability

Sentry and Arize export are optional and fail open (`SENTRY_DSN`, `ARIZE_SPACE_ID`, `ARIZE_API_KEY`). Traces share OpenTelemetry trace IDs across HTTP and Postgres-native worker steps; successful health checks and polling are excluded. `SENTRY_TRACES_SAMPLE_RATE` defaults to `0.05`. Sentry receives operational attributes only (no bodies, headers, cookies, or prompts). Arize receives metadata by default; `NORI_ARIZE_CAPTURE_CONTENT=true` enables best-effort redacted, bounded prompt/reply excerpts. Set `ARIZE_PROJECT_URL` to attach `nori.arize.url` links on Sentry model spans.

## Container images

- Core image: [`infrastructure/Dockerfile`](infrastructure/Dockerfile) (built from `core/`, runs `cmd/nori --role=all|api|worker`).
- Web image: [`../web/Dockerfile`](../web/Dockerfile) (built from `web/`).

Go `core` and Next.js `web` share this monorepo and deploy with independent images and releases.
