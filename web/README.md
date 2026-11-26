# Nori Web

Next.js web client and BFF proxy for Nori. The Go runtime (`core/`) owns conversations, durable task execution, MCP servers, automations, goals, library items, memory, BYOK model keys, Home AI connectors, and permissions.

## Local preview

```sh
bun install --cwd web --frozen-lockfile
make dev
```

Run these commands from the repository root. The preview binds to loopback (`http://127.0.0.1:3210`) and requires a configured runtime and a provisioned user. Use `make local-web` if the local runtime is already running.

## Runtime configuration

| Name | Purpose |
| --- | --- |
| `NORI_LOCAL_AGENT_URL` | Loopback Go runtime origin for local development |
| `NORI_LOCAL_EMAIL` | Provisioned user email for loopback development (`OWNER_EMAIL` fallback) |
| `NORI_RUNTIME_URL` | HTTPS Go runtime base URL (hosted / `prod-check.mjs`) |
| `NORI_CLOUD` | Deployment mode (`gcp`, `aws`, or `local`; hosted modes use HS256 service auth) |
| `NORI_AUTH_PROVIDER` | Auth provider (`oidc` or `iap` for hosted deployments) |
| `NORI_OIDC_ISSUER` / `NORI_OIDC_AUDIENCE` | Expected OIDC issuer and audience |
| `NORI_SERVICE_TOKEN_KEY` | HS256 signing key for hosted runtime service auth |
| `NORI_APP_ORIGIN` | Exact browser origin used to verify writes |

Keep local configuration in the ignored repository-root `.env`, mode 600. Never bake credentials into images.

## Routes and boundaries

- `/` runs the single persistent assistant thread via `/api/v1/snapshot`, `/api/v1/events`, and `/api/<operation>`.
- `/work`, `/memory`, `/more`, `/apps` (Apps & MCP), `/routines`, `/goals`, `/library`, `/settings` (`models`, `permissions`, `preferences`, `appearance`, `history`, `diagnostics`, `account`), and `/connector/activate` proxy through `/api/v1/...`.
- `src/lib/runtime/auth.ts` verifies local development, IAP, or OIDC identity and resolves the account to a provisioned user. `src/lib/runtime/forward.ts` prefixes upstream paths with that user's ID and attaches HS256 service credentials for hosted runtimes.
- `scripts/prod-check.mjs` runs a loopback OIDC + HTTPS front to verify a production `next start` build against a local core without relaxing auth checks.

## Build and checks

- `bun --cwd web run build` produces Next.js standalone output (`web/Dockerfile` builds from the repository root `.`).
- `bun --cwd web test src` runs unit tests; `bun --cwd web run test:browser` runs Playwright E2E regression tests.
- `make check` runs core checks, contract validation, `next typegen`, `tsc --noEmit`, `eslint src`, web unit tests, macOS tests, script tests, and persona-sim checks.
