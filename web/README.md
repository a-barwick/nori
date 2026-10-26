# Nori web

Nori uses the Nova design: a white conversation surface, lavender frame, purple actions, and the animated onigiri identity. Conversation and task history come from Firestore through the existing runtime API; the web app does not generate answers or execute tasks itself.

## Local preview

```sh
bun install --frozen-lockfile
bun --env-file=../.env run dev --port 3210
```

The local preview binds to loopback. Without a configured runtime, it shows a connection notice and disables Send. It does not substitute demo messages, task results, or screenshots. Real task execution requires the project to be resumed; previewing the frontend does not authorize resuming the computer or worker.

## Runtime configuration

Production uses the existing Cloud Run service configuration:

| Name | Purpose |
| --- | --- |
| `NORI_RUNTIME_URL` | Existing Go runtime base URL |
| `IAP_AUDIENCE` | Expected audience of Google's signed IAP assertion |
| `NORI_APP_ORIGIN` | Exact browser origin used for same-origin POST verification |

No project API key is needed in the web image. Keep any local project configuration in the ignored repository-root `.env`, mode 600; use existing approved server stores in production. Never bake credentials into images.

## Behavior and boundaries

- The onigiri stays visible on desktop and mobile, responds to typing and task state, and opens Activity. The header also reports connection and execution status.
- The composer and connection notices reserve their own space, including for multiline drafts. Saved structured replies use document cards with Copy, Open document, and Download.
- `/redesign` is a development-only concept with fictional data; production returns not found for it.
- The signed IAP assertion and exact owner email are verified before private routes are served. Unsigned identity headers are never trusted.
- `/api/session`, `/api/run`, `/api/cancel`, `/api/input`, and `/api/artifact` proxy the existing API contract with server credentials. Browser credentials are not forwarded.
- The conversation uses the current message bubbles, avatar/presence, document layout and reading view. Long saved replies can be opened and downloaded, with Back to chat preserving the conversation position.
- The activity panel shows real task events, shell/browser output, statuses, and protected screenshots. Stop requests cancellation and waits for runtime confirmation.
- History refreshes only while a task is active, the tab is visible, and task execution is resumed. Errors suspend automatic refresh until manual reconnection.
- The old Library/Goals/Ideas/Settings URLs redirect to chat. Their demo shell, seeded local store, scripted agent, attachments, voice, and local edit/delete controls have been removed. The old `/api/nori` endpoint returns HTTP 410.
- Durable tasks accept corrections and answers through the composer while running; these use the existing `/api/input` contract. Questions from the runtime appear in Activity and in the conversation status. The local outer-agent adapter has a separate task-update form.
- The UI does not invent response streaming: the existing contract returns saved messages and task activity.

## Build and deployment

`bun run build` produces Next.js standalone output; `bun run lint` checks the frontend. The Dockerfile builds only `web/` and excludes `.env*`, dependencies, and local build output from its context.

A deployment must update only the web image on the existing `nori-web` Cloud Run service. Preserve its IAP, identity, environment, private access, domain mapping, and min/max instance configuration. Use the [operator lifecycle](../core/infrastructure/poc/README.md) to pause worker capacity and stop the computer when live work finishes. Local preview does not start cloud capacity.
