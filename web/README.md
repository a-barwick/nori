# Nori web

Nori uses the Nova design with a white conversation surface, lavender frame, purple actions, and an animated onigiri. The Go runtime owns conversations, task history, and execution.

## Local preview

```sh
bun install --cwd web --frozen-lockfile
make dev
```

Run these commands from the repository root. The preview binds to loopback and requires a configured runtime and a provisioned user. Use `make local-web` if the local runtime is already running.

## Runtime configuration

Production uses the existing Cloud Run service configuration:

| Name | Purpose |
| --- | --- |
| `NORI_RUNTIME_URL` | Existing Go runtime base URL |
| `IAP_AUDIENCE` | Expected audience of Google's signed IAP assertion |
| `NORI_APP_ORIGIN` | Exact browser origin used to verify writes |
| `NORI_LOCAL_AGENT_URL` | Loopback Go runtime origin for local development |
| `NORI_LOCAL_EMAIL` | Provisioned user email for loopback development. `OWNER_EMAIL` remains a local fallback |

No project API key is needed in the web image. Keep any local project configuration in the ignored repository-root `.env`, mode 600; use existing approved server stores in production. Never bake credentials into images.

## Behavior and boundaries

- The onigiri stays visible on desktop and mobile, responds to typing and task state, and opens Activity. The header also reports connection and execution status.
- The composer and connection notices reserve their own space, including for multiline drafts. Saved structured replies use document cards with Copy, Open document, and Download.
- `src/lib/runtime/auth.ts` verifies the signed IAP or OIDC identity and resolves the email to a provisioned user. The relay in `src/lib/runtime/forward.ts` prefixes upstream paths with that user's ID and supplies service credentials.
- `/api/<operation>` and `/api/agent/<operation>` share the operation allowlist in `src/lib/runtime/methods.ts`. The `agent` alias uses a loopback origin in development. Hosted calls use the configured HTTPS runtime.
- The conversation uses the current message bubbles, avatar/presence, document layout and reading view. Long saved replies can be opened and downloaded, with Back to chat preserving the conversation position.
- The activity panel shows real task events, shell/browser output, statuses, and protected screenshots. Stop requests cancellation and waits for runtime confirmation.
- The assistant loads `/api/v1/snapshot` and follows `/api/v1/events` while the tab is visible and the stream is awake. It can fetch older messages separately and resumes from the last event position.
- The Chats tab has independent conversations, streamed replies, and scoped CSV attachments. It restores the selected chat from the address. The assistant also accepts PNG, JPEG, WebP and PDF attachments.
- Durable tasks accept updates and answers through Activity. Questions appear in Activity and the conversation status.

## Build and deployment

`bun run build` produces Next.js standalone output; `bun run lint` checks the frontend. The Dockerfile builds only `web/` and excludes `.env*`, dependencies, and local build output from its context.

A deployment must update only the web image on the existing `nori-web` Cloud Run service. Preserve its IAP, identity, environment, private access, domain mapping, and min/max instance configuration. Use the [operator lifecycle](../core/infrastructure/app/README.md) to pause worker capacity and stop the computer when live work finishes. Local preview does not start cloud capacity.
