# Nori web

The root page is the single-user Nori POC: persisted conversation, task activity, screenshot results, and Stop. It calls the private Go Agent Runtime through server-side POST routes. The web service makes no model calls and needs no model API keys.

## Local development

```sh
cd web && bun install
cd .. && make web
```

Run these commands from the monorepo root. Open `http://127.0.0.1:3210`. Development access is limited to loopback hostnames; API calls also require the exact same Origin. Production always requires a signed IAP assertion. Missing runtime configuration produces a visible error without automatic retries.

The root `.env.example` lists the configuration names. Keep their values in the monorepo's single gitignored, mode-600 root `.env` and load them through the root start script or Makefile; do not create a second credential file in `web/`. Local upstream access uses Google Application Default Credentials; Cloud Run uses its service account.

| Variable | Meaning |
| --- | --- |
| `NORI_RUNTIME_URL` | Native Agent Runtime invoke root, ending in `/api/`. The proxy appends `api/session`, `api/run`, `api/cancel`, or `api/artifact`; the resulting `/api/api/` is intentional. |
| `IAP_AUDIENCE` | Cloud Run signed-header audience: `/projects/PROJECT_NUMBER/locations/REGION/services/SERVICE_NAME`. Required in production. |
| `NORI_APP_ORIGIN` | Optional canonical HTTPS origin for same-origin checks, especially with a custom domain. Otherwise the request host is used. |

The production app verifies IAP's ES256 signature using Google's IAP public keys, issuer, audience, token timestamps, and the exact configured owner in `src/lib/poc/auth.ts`. Unsigned identity headers grant no access. Cloud Run's native IAP remains the first gate; configure its OAuth client and access policy before use. See [Google's signed-header requirements](https://docs.cloud.google.com/iap/docs/signed-headers-howto).

The service account needs permission to invoke the configured Agent Runtime. Browser credentials and IAP's `X-Serverless-Authorization` header are never forwarded upstream; the server obtains a fresh OAuth access token through `google-auth-library`.

## Container and checks

`bun run build` produces Next.js standalone output. The Dockerfile runs as the non-root `node` user on Cloud Run's `PORT` (default `8080`). `.dockerignore` excludes all `.env` files. `/api/health` is an unauthenticated liveness endpoint; it does not invoke the agent or check external services.

`bun run lint` checks the existing application. Polling runs only while a task is active and the tab is visible, stops after errors or session pause/expiry, and is bounded by the task deadline. Stop remains pending until the backend confirms cancellation; a lost connection does not imply the task stopped.

The older design screens under `/ideas`, `/goals`, `/library`, and `/settings` remain separate legacy demos and are not linked from the POC. Their former direct model endpoint `/api/nori` returns `410`.
