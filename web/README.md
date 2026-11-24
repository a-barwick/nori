# Nori web (local test workbench)

Local test workbench for Nori. The Go runtime owns conversations, durable task execution, MCP servers, automations, goals, library items, and permissions.

## Local preview

```sh
bun install --cwd web --frozen-lockfile
make dev
```

Run these commands from the repository root. The preview binds to loopback and requires a configured runtime and a provisioned user. Use `make local-web` if the local runtime is already running.

## Runtime configuration

| Name | Purpose |
| --- | --- |
| `NORI_LOCAL_AGENT_URL` | Loopback Go runtime origin for local development |
| `NORI_LOCAL_EMAIL` | Provisioned user email for loopback development (`OWNER_EMAIL` fallback) |
| `NORI_RUNTIME_URL` | HTTPS Go runtime base URL (hosted / `prod-check.mjs`) |
| `NORI_CLOUD` | Deployment mode (`aws` default for hosted HS256 service auth) |
| `NORI_AUTH_PROVIDER` | Auth provider (`oidc` default for hosted deployments) |
| `NORI_OIDC_ISSUER` / `NORI_OIDC_AUDIENCE` | Expected OIDC issuer and audience |
| `NORI_SERVICE_TOKEN_KEY` | HS256 signing key for hosted runtime service auth |
| `NORI_APP_ORIGIN` | Exact browser origin used to verify writes |

Keep local configuration in the ignored repository-root `.env`, mode 600. Never bake credentials into images.

## Behavior and boundaries

- `/` runs the single persistent assistant thread via `/api/v1/snapshot`, `/api/v1/events`, and `/api/<operation>`.
- `/work`, `/apps` (MCP servers), `/routines`, `/goals`, `/library`, and `/settings` (`permissions`, `preferences`, `memory`, `history`, `diagnostics`) proxy through `/api/v1/<family>/...`.
- `src/lib/runtime/auth.ts` verifies local development or OIDC identity and resolves the email to a provisioned user. `src/lib/runtime/forward.ts` prefixes upstream paths with that user's ID and supplies HS256 service credentials for hosted runtimes.
- `scripts/prod-check.mjs` runs a loopback OIDC + HTTPS front to verify a production `next start` build against a local core without relaxing auth checks.

## Build and checks

`bun run build` produces Next.js standalone output; `make check` runs core checks, contract schema/fixture validation, `next typegen`, `tsc --noEmit`, and `eslint src`.
