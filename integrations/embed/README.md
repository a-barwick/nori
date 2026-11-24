# Embed Nori in a Host App

Import [`createNoriHandler`](adapter.mjs) from your host server code. Mount it on `GET` and `POST` under `/api/nori/*` and pass the original Fetch `Request`. The example uses placeholder names for your host app's session verifier and credential provider.

```js
import { createNoriHandler } from "./adapter.mjs";

export const handleNori = createNoriHandler({
  runtimeBase: config.noriRuntimeBase,
  bindings: config.noriBindings,
  async verifyIdentity(request) {
    const session = await requireVerifiedHostSession(request);
    return session ? { issuer: session.issuer, subject: session.subject } : null;
  },
  async serviceAuthorization(signal) {
    const token = await getNoriRuntimeToken({ signal });
    return token ? `Bearer ${token}` : "";
  },
});
```

Keep the runtime URL, bindings, and credential provider in server configuration. Use your host app's verified issuer and stable subject, including tenant context in the identity namespace where applicable. Each binding is `{ issuer, subject, noriUserId }` and points to a distinct user already provisioned in Nori. Missing sessions return 401 and unmapped identities return 403. Apply your host app's same-origin and CSRF policy at the mount, and keep the runtime behind its service authentication. Use the runtime's invoke base URL, including its path prefix if present. The credential hook supplies the runtime's full `Authorization` value. The adapter constructs each upstream request with server-selected credentials and scope.

## Routes

The adapter prepends `/users/{mappedUser}` to each Nori route. Each user has one persistent conversation at Nori's `assistant` route. Nori scopes Work, interactions, and receipts to the same user and returns 404 for another user's resource ID. Use the existing [assistant contract types](../../web/src/lib/contracts/assistant.ts) for Work, interactions, and mutation receipts.

| Host route | Nori route | What to use |
| --- | --- | --- |
| `GET /api/nori/conversation` | `GET /v1/conversations/assistant` | Conversation snapshot and event `position`. |
| `POST /api/nori/messages` | `POST /v1/conversations/assistant/messages` | Send `{ id, text }`. The response includes the conversation and acceptance state. |
| `GET /api/nori/events?after={position}` | `GET /v1/conversations/assistant/events?after={position}` | Stream Nori SSE frames. `Last-Event-ID` takes precedence when present. |
| `GET /api/nori/work` or `/api/nori/work/{taskId}` | `GET /v1/work` or `/v1/work/{task_id}` | Read `task_id`, `bucket`, `status`, and `result_ids`. The detail includes operation outcomes and provider references. |
| `GET /api/nori/interactions` or `/api/nori/interactions/{id}` | `GET /v1/interactions?status=pending` or `/v1/interactions/{id}` | Read the typed interaction. Follow `next_cursor` with `?cursor=…` if present. |
| `POST /api/nori/interactions/{id}/responses` | `POST /v1/interactions/{id}/responses` | Submit an approval decision. |
| `GET /api/nori/mutations/{requestId}` or `/api/nori/results/{id}` | `GET /v1/mutations/{request_id}` or `/v1/results/{id}` | Read the saved mutation receipt or result metadata. |

## UI state

- **Send.** Generate a UUID before the first POST. Keep the original ID and text for every retry of that submission. A 202 response with `state: "accepted"` means Nori accepted the message. A 202 response with `state: "queued"` means Nori saved it and could not confirm delivery. Retry with the same UUID and text after a queued response or a lost response. Use Work and conversation updates for progress.
- **Reconnect.** Start from the snapshot's `position`. Stream the response without buffering and resume after the last applied event `seq`. EventSource can resume with `Last-Event-ID`. On a frame with `kind: "reset"`, close the stream, fetch and apply a fresh snapshot, then open a new stream from its `position`. Refresh Work and pending interactions from their routes on startup, reconnect, and task updates. Clear the snapshot and cursor when the signed-in identity changes.
- **Approve.** Render the `prompt` and typed `payload` when the interaction has `kind: "approval"` and `status: "pending"`. POST `{ request_id, expected_revision, value: { kind: "approve", action_digest } }`. Use the displayed interaction's `revision` and `action_digest`. Use `kind: "decline"` for a decline. Keep the UUID and payload unchanged when retrying that decision. On `revision_conflict` or `interaction_stale`, refresh the interaction and ask the user to review its current state. A queued approval can be retried with its original payload. Its mutation receipt records the decision. Use the Work detail's operation `outcome` to distinguish confirmed effects from pending or uncertain ones. Display its `provider_refs` and `receipt_url` when present, and the Work item's `error` on failure.

The integration verification gate is the full message, approval, and result path under two authorized demo identities, including reconnect and isolation between their work.
