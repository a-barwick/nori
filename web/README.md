# Nori web

The current-main chat UI connected to the deployed Go runtime. Conversation and task history come from Firestore through the existing runtime API; the web app does not generate answers or execute tasks itself.

## Local preview

```sh
bun install --frozen-lockfile
bun run dev --port 3234
```

The local preview binds to loopback. Without a configured runtime, it shows a connection notice and disables Send. It does not substitute demo messages, task results, or screenshots. A real task requires an explicitly authorized active session; previewing the frontend does not authorize resuming the computer or worker.

## Runtime configuration

Production uses the existing Cloud Run service configuration:

| Name | Purpose |
| --- | --- |
| `NORI_RUNTIME_URL` | Existing Go runtime base URL |
| `IAP_AUDIENCE` | Expected audience of Google's signed IAP assertion |
| `NORI_APP_ORIGIN` | Exact browser origin used for same-origin POST verification |

No project API key is needed in the web image. Keep any local project configuration in the ignored repository-root `.env`, mode 600; use existing approved server stores in production. Never bake credentials into images.

## Behavior and boundaries

- The signed IAP assertion and exact owner email are verified before private routes are served. Unsigned identity headers are never trusted.
- `/api/session`, `/api/run`, `/api/cancel`, `/api/input`, and `/api/artifact` proxy the existing API contract with server credentials. Browser credentials are not forwarded.
- The conversation uses the current message bubbles, avatar/presence, document layout and reading view. Long saved replies can be opened and downloaded, with Back to chat preserving the conversation position.
- The activity panel shows real task events, shell/browser output, statuses, and protected screenshots. Stop requests cancellation and waits for runtime confirmation.
- History refreshes only while a task is active, the tab is visible, and the session remains active. Errors suspend automatic refresh until manual reconnection.
- Attachments, voice, local message edits/deletion, demo Library/Goals/Ideas/Settings, and simulated agent actions are not exposed. Their old URLs redirect to chat. The old `/api/nori` demo generation endpoint is retired.
- Durable tasks accept corrections and answers through the composer while running; these use the existing `/api/input` contract. Questions from the runtime appear in Activity and in the conversation status.
- The UI does not invent response streaming: the existing contract returns saved messages and task activity.

## Build and deployment

`bun run build` produces Next.js standalone output; `bun run lint` checks the frontend. The Dockerfile builds only `web/` and excludes `.env*`, dependencies, and local build output from its context.

A deployment must update only the web image on the existing `nori-web` Cloud Run service. Preserve its IAP, identity, environment, private access, domain mapping, and min/max instance configuration. Backend and Temporal changes are outside this frontend update. Observe the authorized session deadline and the maximum three-hour limit for explicitly authorized runtime sessions.
