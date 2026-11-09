# Nori assistant contract baseline

Draft revision `r0`, grounded in application code at `c53931359ac2e0f95dacb5368c517cb3cde976a9` and the plan committed at `0d7655cc8732246097060405ad46a01cd0161986`. This is a proposed development baseline, not implemented behavior or an approved wire specification. No implementation threads have reviewed it yet; recommendations below are not represented as Astra–Sol consensus.

Keep Nori's Go application services, user-scoped storage, durable task commands, Temporal execution, and operation evidence. Extend those boundaries for the Extension registry, Connections, App connectors, MCP client, Automations, and the complete Nori app. Give frontend and backend one explicit set of new application contracts, while preserving the existing runtime API and historical task behavior during migration.

Read this with the [complete plan](assistant-integration-plan.md) and [contract location map](assistant-contract-locations.md). The plan owns product scope; this document proposes the shared shapes and behavior engineers need to work in parallel. Examples use TypeScript notation for readability; they are not generated clients or implemented schemas.

## Decisions for Austin

These decisions affect user expectations or credential custody. The recommendations remain pending. They do not block drafting interfaces, fixtures, or unrelated implementation. Do not silently implement a recommendation as an approved policy.

| ID | Decision and recommendation | Consequence and dependent work |
| --- | --- | --- |
| D1 | **Where should connected-account credentials live?** Recommend the deployment's managed secret store for OAuth refresh tokens, app passwords, and configured connection credentials, with owner-scoped references in application records. Local development would use a separately agreed local store. Project API keys remain in the project's `.env` under the existing rules. | Existing secret implementations only read secrets; they do not implement token creation, rotation, refresh coordination, or deletion. Confirm custody before storing real account credentials or choosing the writable backend. Alternative: encrypted credential records in the database, with a separately managed encryption key and its operational burden. |
| D2 | **How much standing authority should an automation have?** Recommend direct, explicit user requests authorize their specified actions without a second confirmation. Creating an automation grants only the actions, accounts, resources, recipients, and limits shown at creation. Execution within that grant proceeds unattended; expanded access or materially changed actions return to the user. | This controls recurring sends/writes and whether an automation can actually finish while the user is away. An alternative is confirming every external write, which makes those automations interactive. Connection consent alone grants neither behavior. Ordinary explicit requests already authorized by the user must not become repeated permission prompts. |
| D3 | **What should happen to missed automation runs?** Recommend catch up the latest read/summary run, deliver a reminder only within its configured useful window, and expire unattended writes/sends outside their explicit execution window. Show missed work in history. | The alternative is replaying every missed occurrence, which may produce stale messages or a burst of changes. Scheduler policy and creation previews depend on this choice. Each definition retains an explicit policy rather than inheriting a hidden global default. |
| D4 | **What should disconnecting an account retain?** Recommend keep deliberately saved Library results and action history, stop future access, and remove transient connector caches. Offer a separate explicit deletion of imported content. | Preserves existing work while making disconnect's privacy effect clear. An alternative is deleting all copied provider content on disconnect. Source authorization still governs new fetches; retained results must be clearly identified as saved copies, not live access. Cache cleanup and disconnected-source UI depend on this choice. |

Engineering recommendations below—schema format, route organization, generated types, event consumption, package placement, and replay handling—are for the engineers to evaluate. They do not need individual user approvals. Substantive Astra–Sol disagreements about them follow the plan's escalation rule.

## What the existing code establishes

| Current boundary | Verified behavior | Baseline consequence |
| --- | --- | --- |
| Two application clients | The main UI uses [runtime types](../web/src/lib/types.ts) and [useRuntime](../web/src/lib/runtime/useRuntime.ts). The other client uses [chat/contract.ts](../web/src/lib/chat/contract.ts). [Conversation projections](../core/internal/api/conversation_contract.go) expose more fields than that TypeScript client currently describes. | Do not assume a unified frontend contract already exists. Add complete projections and aligned consumers before migrating the main UI. |
| Proxy and core routes | [Core HTTP routes](../core/internal/api/http.go) include conversation operations, but [the web proxy's conversation allowlist](../web/src/lib/runtime/forward.ts) does not currently forward the operations read route. | A contract includes web routing and auth, not just a Go handler and a TypeScript type. |
| Task acceptance | [Commands](../core/internal/state/commands.go) have caller-stable IDs, payload conflict checks, ordered revisions, and transactional acceptance/delivery intent. [Tasks.AcceptBatch](../core/internal/conversation/tasks.go) delivers accepted work. [HTTP commands](../core/internal/api/tasks.go) can return `503` with `accepted: true` when waking fails. | Preserve acceptance separately from delivery and completion; never create another task just because a response or wake was lost. |
| Approval mismatch | The HTTP command handler recognizes `continue`, `approve`, and `decline`; `commandPrep.apply` in [state/commands.go](../core/internal/state/commands.go) handles start/steer, answer, and stop, and rejects other kinds. | From static inspection, those HTTP names are not a working approval lifecycle. Implement structured interactions through durable acceptance; don't claim support from the handler's vocabulary. |
| Tool definitions | [transcript/tool.go](../core/internal/transcript/tool.go) has a restricted schema representation. [agent/tools.go](../core/internal/agent/tools.go) and [catalog.go](../core/internal/agent/catalog.go) register and dispatch known tools statically. | Add canonical schemas and immutable tool bindings; do not silently squeeze external schemas into the current subset. |
| Side-chat execution | [chat/service.go](../core/internal/chat/service.go) explicitly accepts only `upload_read` and `history_read`, with its own read checkpoints. | Shared discovery alone will not give chats tool parity. Connect external work to the durable task path and route results to the originating chat. |
| Execution evidence | [state/evidence.go](../core/internal/state/evidence.go) defines operation identity, allowed outcomes/transitions, action digests, and evidence limits. [task/tool_durable.go](../core/internal/task/tool_durable.go) reuses saved receipts before admitting more work. | Extend these records and rules. Do not introduce a competing operation ledger or replace outcomes with a generic success boolean. |
| Workflow history | [task/history.go](../core/internal/task/history.go) carries references and bounded metadata. [runTool](../core/internal/task/workflow.go) looks up policy by name; the workflow already has a versioned cleanup branch. | Record dynamic binding/policy decisions outside deterministic workflow code and version the orchestration change. |
| Ownership and storage | [state/contracts.go](../core/internal/state/contracts.go) scopes stores immutably to a user. [platform/platform.go](../core/internal/platform/platform.go) composes record storage and artifact storage. | New records follow the same ownership and transaction boundaries; no client-supplied owner ID and no mutable current-user singleton. |
| Wake and computer | [wake.Waker](../core/internal/wake/wake.go) wakes compute without carrying task content. [computer.Provider](../core/internal/computer/provider.go) distinguishes waking `Open` from non-waking `Attach`. | Scheduling requires durable definitions and occurrences beyond a wake. Cleanup/reconciliation must not inadvertently wake stopped compute. |
| Secrets | [secret.Lookup](../core/internal/secret/secret.go) and AWS/GCP adapters resolve references. | A writable connection credential lifecycle is new work, subject to D1. |

## Contract ownership and physical homes

Keep internal Go contracts beside their responsibilities. Introduce one small schema source for public application DTOs and distributable manifests, not a second domain model for every Go struct.

| ID | Contract | Proposed definition home | Owners and consumers |
| --- | --- | --- | --- |
| C1 | Application APIs and state synchronization | New `contracts/assistant/` for JSON Schemas and request/response fixtures; `core/internal/api` for routes/projections; generated web types under `web/src/lib/contracts/`. | Backend + frontend; integration verifies web-to-core behavior. |
| C2 | Extension registry and Available tools | New `core/internal/extensions`; canonical tool schemas/manifests in `contracts/assistant/`; model adaptation remains beside `transcript`/model adapters. | Backend; frontend reads inventory; integration verifies schemas and version pinning. |
| C3 | Connections and credentials | New `core/internal/connections`; persisted records and store interfaces in `state`; credential interfaces in `secret`. | Backend + frontend; integration verifies lifecycle and account isolation. |
| C4 | Tool executor and execution records | Existing `task`, `agent`, `state/evidence.go`, and `task/history.go`; adapter interface owned by the invocation layer. | Backend + integration; frontend consumes operation projections. |
| C5 | Permission checks and pending interactions | New application permission service; `state` records/acceptance; `api` projections; existing task revision guards. | All three. |
| C6 | Plugins, MCP, and Computer runtime | Registry/package contracts; new adapter packages; existing `computer` and `computerd` protocol boundary. | Backend + integration; frontend owns management and takeover behavior. |
| C7 | Automations, Scheduler, goals, and delivery | New scheduling application package; `state` records/outboxes; existing dispatch and wake adapters. | All three. |
| C8 | Library, domain results, and input | Existing `state/parts.go`, `results.go`, uploads and artifact stores; new API projections and index interface. | All three. |

These new directories are recommendations, not directories created by this draft. Use JSON Schema Draft 2020-12 for new public payloads and manifests, with TypeScript types generated from that source; Go runtime values are validated against the same schemas and fixtures. Do not generate the existing persistence model from the public API. Treat generator/library selection as an implementation decision and demonstrate support for the schemas actually used. [JSON Schema specification](https://json-schema.org/draft/2020-12).

## C1 — Application APIs and state synchronization

### Common conventions

- Preserve existing JSON field names, IDs, command semantics, and contract labels. Existing conversation envelopes mix camelCase projections with snake_case state records; this draft does not rename them in place. New management payloads use snake_case, matching most state records.
- New independent resource IDs and mutation request IDs are UUID strings. Existing task/operation/result IDs remain opaque and unchanged. Never derive ownership from an ID or accept `owner_id` as authority.
- New mutable resources expose a positive integer `revision`. Wire numbers must remain JavaScript-safe integers. This resource revision is distinct from a task's instruction revision, an event cursor, a schema version, and a provider's concurrency token.
- Times are RFC 3339 instants; recurring local-time rules separately retain an IANA time zone. All-day calendar dates are date strings, not midnight instants.
- New request schemas reject unknown fields and invalid union variants. Read clients tolerate additive response fields and use explicit fallbacks for unknown display variants. Unknown commands, effects, or executable variants fail closed.
- List responses are `{items, next_cursor?}` with stable server ordering and opaque cursors. Cursor scope includes owner, collection, and filters. Absence of `next_cursor` means the end, not an error.

```ts
type ID = string;
type Revision = number;
type Instant = string;
type JSON = null | boolean | number | string | JSON[] | {[key: string]: JSON};

type Change<T> = {
  request_id: ID;
  expected_revision: Revision; // 0 for create; current revision otherwise
  value: T;                   // complete editable value, not an ambiguous merge patch
};
type MutationReceipt<T> =
  | {request_id: ID; accepted: true; status: "applied"; resource: T}
  | {request_id: ID; accepted: true; status: "queued"; resource: T;
     work?: {task_id: string; operation_id?: string}};
type APIError = {
  error: string;              // retain a human-readable string for current clients
  code: string;
  retryable: boolean;
  accepted: boolean;
  request_id?: ID;
  receipt?: MutationReceipt<JSON>;
};
```

For new management mutations, deduplicate by authenticated owner and request ID, and bind the ID to method, target, expected revision, and canonical payload. A matching retry returns its recorded receipt even if the resource has changed since; a different payload under that ID returns `409 idempotency_conflict`. Check prior acceptance before optimistic revision validation. Persist the resource mutation and receipt together. An external operation is queued durably before reporting acceptance; its external completion is separate. This adds management receipts without changing existing command receipt semantics.

A queued receipt contains the accepted resource projection, such as an installation in `installing` state, plus task/operation references when task execution owns the work. Poll that resource or referenced work for completion; the immutable acceptance receipt does not turn into a completion result. Deterministic management lifecycle work need not invoke a model. Its durable lifecycle state is owned by the resource, while external tool effects still use C4's execution evidence.

Create/update actions use `Change<T>`; command actions such as disconnect or pause use a typed `value` containing the action's parameters. No client-provided URL or executable snippet is an action. New endpoints return `200/201` for applied changes and `202` for queued work. Validation is `400`; authentication/authority failures `401/403`; owner-inaccessible objects appear as `404`; revision/idempotency/stale-interaction conflicts `409`; throttling `429`; temporary failure `503`. A `503` may still contain `accepted: true` and the saved receipt. Frontends must preserve the request ID and check the receipt, not relaunch work.

`GET /v1/mutations/{request_id}` returns a saved management receipt or `404`. A missing lookup after a network failure does not prove a still-running request was rejected; retry the identical mutation ID. Receipt retention must cover queued work and documented retry windows; implementation must not silently re-execute an old accepted request after expiring its deduplication record. Use a compact tombstone if full receipts are purged.

### Route families

Paths below are core paths after the existing user-scoping wrapper. The browser uses `/api/v1/...` through authenticated Next routes. They are proposed additions unless marked existing; each needs a proxy route, auth checks, and schema coverage.

| Resource | Read surface | Mutation surface |
| --- | --- | --- |
| Conversations | Existing `/v1/conversations`, `/{id}`, `/messages`, `/events`, `/operations`. | Existing create/message/cancel/delete; preserve task commands through their existing handler while adding complete projections. |
| Available tools | `/v1/tools`, `/v1/tools/{id}`; filters for search, source, connection, availability. | Exposure/configuration changes through their installation or connection owner. Discovery is not permission to invoke. |
| Plugins | `/v1/plugins/catalog`, `/v1/plugins/installations`, `/installations/{id}`. | Create installation; update configuration; enable/disable/update/rollback/uninstall actions on an installation. |
| Connections | `/v1/connections`, `/{id}`, `/{id}/resources`. | Create/update; connect/repair/disconnect actions. Auth start returns a bounded sign-in interaction, never credentials. Callback completion is server-validated, not a generic client mutation. |
| Permissions | `/v1/permissions`, `/{id}`. | Create/update/revoke scoped rules. |
| Interactions | `/v1/interactions`, `/{id}`. | `POST /v1/interactions/{id}/responses` using the typed response below. |
| Work | `/v1/work`, `/{task_id}`, `/{task_id}/operations`. | Existing start/steer/answer/stop acceptance, exposed through application services; no second task engine. |
| Automations | `/v1/automations`, `/{id}`, `/{id}/occurrences`. | Create/update; pause/resume/cancel/run-now actions. |
| Goals | `/v1/goals`, `/{id}`. | Create/update; pause/resume/complete/cancel actions, each respecting linked work. |
| Library | `/v1/library`, `/{id}`, `/{id}/versions`, `/{id}/file`. | Save/revise/export/delete through existing result ownership and new deletion/index rules. Existing result URLs remain valid. |
| Computer sessions | `/v1/computer/sessions`, `/{id}`. | Request takeover, renew/release ownership, resume task through the session contract. |
| Preferences | `/v1/preferences`, `/v1/delivery-destinations`. | Revision-guarded preferences and destination enrollment/removal; no plaintext notification secrets in responses. |

Do not add a public endpoint that accepts arbitrary tool bindings or bypasses the Tool executor. UI domain commands and the Nori MCP server call the same application services as conversation-driven work.

### State updates and compatibility

Keep the existing conversation SSE envelope and cursor rules: snapshot position, ordered events, duplicate suppression, and snapshot recovery after a gap/reset. Existing events hydrate current records, not historical versions. Do not conflate a resource revision with stream position. Never drop a position from a contiguous stream merely because a consumer doesn't display that event kind. [Server event projection](../core/internal/api/conversation_events.go), [client recovery](../web/src/lib/chat/contract.ts).

New management resources use mutation responses plus polling while a relevant screen is active or a mutation remains queued. Re-fetch on focus/reconnect; compare resource revisions so older responses cannot overwrite newer state. This satisfies the plan's stream-or-poll requirement without inventing another cross-product event bus. Notification delivery for an absent user is C7, not browser polling. If measured needs justify management SSE later, propose its cursor/snapshot contract explicitly.

Add an `assistant_contract` capability label for the new management surface while retaining `contract: nori-chat-2` and `conversationContract: nori-conversations-1`. A proposed initial label is `nori-assistant-1`; it is advertised only once implemented and verified. New UI screens check actual feature availability. Do not bump old labels for additive fields or present unfinished capabilities as usable.

Converge the main UI on complete unified conversation projections during the full implementation. Retain legacy `/api/*`, `/v1/snapshot`, and `/v1/events` until parity and active-client compatibility are demonstrated. No forced migration is required to begin independent management screens.

## C2 — Extension registry and Available tools

Separate installable definitions from user-resolved availability and from an invocation pinned to a particular implementation.

```ts
type Effect = "read" | "reversible_write" | "sensitive_send" | "unknown" | "control";
type DefinitionRef = {id: string; version: string; digest: string};
type ToolDefinition = {
  ref: DefinitionRef;
  extension: DefinitionRef;
  model_name: string;
  title: string;
  description: string;
  input_schema: JSON;
  output_schema?: JSON;
  binding: {adapter: "native" | "mcp" | "computer" | "plugin"; target: string; version: string};
  connection_type?: string;
  policy: {effect: Effect; retry: "safe" | "provider_key" | "reconcile_only"; timeout_ms: number};
  exposure: "direct" | "discoverable" | "programmatic" | "disabled";
};
type AvailableTool = {
  tool: DefinitionRef;
  title: string;
  description: string;
  connection_ids: ID[];
  availability: "ready" | "needs_connection" | "needs_permission" | "disabled" | "unavailable";
  reason_code?: string;
  recovery?: "connect" | "repair" | "enable" | "choose_account";
};
```

Definition IDs are namespaced by publisher/package/tool; versions and content digests identify immutable content. Their representation must be safe for the chosen storage keys and encoded routes; raw provider identifiers are not storage paths. Preserve legacy aliases for built-ins and reserve runtime control names. A manifest cannot claim a reserved name, register an external tool as `control`, or obtain trust from its own effect annotation. Nori validates effective policy; uncertain classification remains `unknown` and does not inherit read-only permission.

Preserve canonical provider schemas and validate actual arguments against them. Model-compatible schemas are projections with a recorded transform version, not replacements for validation. Reject unsupported schema constructs explicitly; bundle/resolve allowed schema references without arbitrary network fetching. An immutable definition digest includes schema, binding, and execution-relevant policy. A changed MCP description/schema or plugin package creates a new definition revision for future selection.

Available tools is computed from definitions, installation state, current Connections, and relevant permissions. It contains no credential references. Account selection happens before invocation; more than one eligible account without an explicit choice or applicable saved default produces an interaction. Never retry a failed write against a different account. Availability is advisory: the Tool executor rechecks before execution.

## C3 — Connections and account credentials

```ts
type Connection = {
  id: ID; revision: Revision;
  integration_id: string;
  title: string;
  account: {provider_id: string; label: string};
  status: "not_connected" | "connecting" | "connected" | "needs_attention" | "disabled" | "disconnecting" | "disconnected";
  granted_scopes: string[];
  selected_resource_ids: string[];
  health: {checked_at?: Instant; code?: string; message?: string};
  defaults: string[]; // named purposes, e.g. calendar.create; not an implicit global account
};
type ResourceRef = {
  connection_id: ID;
  kind: string;
  provider_id: string;
  provider_revision?: string;
};
```

The public projection deliberately excludes tokens, credential references, refresh errors containing provider payloads, and secret-bearing endpoint URLs. Connection configuration must distinguish ordinary configuration from secret inputs; export only redacted configuration and placeholders. Internally retain owner scope, credential reference/version, auth configuration, and a monotonic authorization epoch. Token refresh changes credential version; permission/resource changes change the authorization epoch. Changing a display label does not invalidate an otherwise identical approved action.

The Connections service owns `Resolve`, `Refresh`, `Replace(expected_version)`, and `Revoke`; replacement at expected version zero creates the initial credential. The secret backing store owns reading/writing/deleting credential material, not OAuth or account-selection policy. Actual backing-store writes depend on D1. Refresh is coordinated per connection across workers with conditional version updates to the active credential reference, not only a process mutex. Losing refresh responses, token rotation, and concurrent disconnects must not revive revoked grants. Scope escalation creates a fresh sign-in request, not an automatic retry with broader access.

An auth attempt is one-time, owner-bound, expiring, and bound to the intended integration and return destination. Verify provider response, state, and account before setting `connected`. A callback cannot choose another user's connection. Duplicate callbacks return the already-recorded result. API credentials supplied by the user stay outside chat transcripts and generic logs.

Disconnect first closes admission and advances the authorization epoch, then attempts provider revocation and secret cleanup. Show incomplete cleanup explicitly. Accepted but unattempted operations are denied; completed evidence remains readable according to D4. Already-in-flight effects may still finish, so disconnect cannot promise undo. Reconciliation requiring new provider access stops if authority was revoked; record an unresolved outcome rather than silently reauthorizing.

## C4 — Tool executor, task ownership, and execution records

### Pinned invocation

```ts
type Invocation = {
  task_id: string;
  instruction_revision: number;
  operation_id: string;
  call_id: string;
  tool: DefinitionRef;
  binding_digest: string;
  connection_id?: ID;
  arguments_ref: string;
  arguments_digest: string;
  authority_ref: string;
  parent_operation_id?: string;
};
type PreparedAction = {
  digest: string;
  summary: string;
  effect: Effect;
  resources: ResourceRef[];
  provider_precondition?: string;
};
type AdapterResult = {
  outcome: "confirmed" | "failed" | "submitted" | "uncertain";
  content: JSON;
  result_ids: string[];
  provider_refs: ResourceRef[];
  cost: {amount_usd?: number; status: "known" | "unknown" | "not_applicable"};
  error?: {code: string; message: string; retryable: boolean};
};
```

These are private invocation contracts, not arbitrary client-controlled HTTP bodies. IDs and authority references are supplied/resolved by trusted runtime code. Arguments and results stay in existing private history/artifact storage; Temporal history receives immutable references and bounded control metadata. For each tool, the adapter implements `Prepare`, `Execute`, and, where supported, `Reconcile` and `Cancel`. `Prepare` normalizes the action without external writes; any provider reads it requires still have ownership, accounting, and authority.

The executor loads the exact definition/binding, validates arguments, resolves account/resources, and computes the action digest over the tool/binding, account, normalized arguments, recipients/resources, and relevant provider precondition. It then performs Permission checks, durably admits the operation under the current task revision, and records an attempt before dispatch. Adapters receive only the credentials and scope needed for that attempt. They cannot create their own unowned retries or background work.

Use the existing `OperationRecord` as the ledger. Add optional immutable references for tool/binding, connection, argument digest, authority evidence, and provider receipts. Keep existing field names, effects, outcome names, and transition rules. Public projections can add readable account/action summaries; private credentials and raw sensitive arguments remain private. A `submitted` effect is accepted by the provider but not yet confirmed; `uncertain` is not failure and is never a blanket invitation to retry. Large evidence belongs behind `ResultRef`, within existing size limits.

`pending`, `ready`, `attempted`, `submitted`, `confirmed`, `failed`, `uncertain`, `skipped`, and `voided` remain the existing operation vocabulary. Adapter results describe provider observations; the executor owns ledger transitions. Preserve `outcomeMayFollow` unless a separately reviewed change proves a missing valid transition. A denied action before admission produces a typed denial/interaction, not a fictitious provider failure. Once prepared/admitted, superseded unattempted writes can be voided; attempted writes must be reconciled.

On retry, reuse a settled receipt before deciding whether to make any new external call. New attempts require current authority and an active task revision. Use provider idempotency only for the identical operation/account/action. Without a documented provider key or reconciliation path, an ambiguous write remains uncertain and blocks dependent writes. Independent reads can continue. A cancellation or timeout is never proof that a remote effect did not occur. Late outcomes still enter evidence after task cancellation, as the existing code permits.

### Temporal compatibility

Add a versioned workflow path that receives immutable execution metadata from an activity result before branching on adapter/policy. Include execution location, cancellation/recovery behavior, and interrupted-cost accounting currently represented by `ToolPolicy`, not just the tool's name and effect. Persist that metadata with the model/tool history checkpoint so retries do not re-resolve a pending call to a newer package. Do not replace `TaskToolPolicy(name)` with a live registry query inside workflow code. Old histories retain the old policy path; new histories use the recorded binding path. Workers must either retain the required implementation or explicitly fail a new attempt as unavailable, never substitute a different version.

Replay safety and authority are separate: historical metadata explains what was selected, while current Permission checks gate new external attempts. Use the existing worker deployment/versioning mechanism and an explicit workflow version gate where command sequences change. Demonstrate replay with retained history, not merely unit tests of a new branch. [Temporal Go versioning](https://docs.temporal.io/develop/go/workflows/versioning).

### Conversation and task ownership

Keep task commands and stable task IDs. Add optional origin metadata to task acceptance and persisted runs: originating conversation/message, automation occurrence, and goal as applicable. Old runs without origin map to the existing assistant conversation. Never infer origin from whichever chat is open.

Side chats dispatch external tool work through this same task engine, with command acceptance and result delivery targeted to their origin. Existing attachment/history reads can retain their current checkpoints; do not grow the side-chat loop into a second write engine. The unified conversation projection must include its linked tasks, results, operations, and interactions for either mode. Store result-delivery intent transactionally and deduplicate by destination plus event/result identity. The current assistant-only delivery assumptions need explicit implementation changes.

Keep the existing `reject_while_running` and `fold_into_turn` conversation policies until their consumers are deliberately updated. Task steering and answering are available through Work even when a chat's next conversational turn is rejected. Cancelling a foreground turn, stopping a task, and cancelling an automation are distinct commands; UI controls must name the one they perform.

## C5 — Permission checks and pending interactions

Provider scopes, user permission rules, explicit task instructions, and execution limits are separate inputs. Installation and discovery grant none of them.

```ts
type PermissionRule = {
  id: ID; revision: Revision;
  effect: "allow" | "deny";
  tool_ids: string[];
  connection_ids: ID[];
  resource_ids: string[];
  action_classes: Effect[];
  recipients?: string[];
  automation_id?: ID;
  expires_at?: Instant;
  max_operations?: number;
};
type Interaction = {
  id: ID; revision: Revision;
  task_id?: string;
  instruction_revision?: number;
  conversation_id?: string;
  kind: "question" | "approval" | "account_selection" | "sign_in" | "computer_takeover";
  status: "pending" | "resolved" | "expired" | "superseded";
  prompt: string;
  payload: JSON; // a closed, kind-specific schema; not arbitrary executable UI
  action_digest?: string;
  expires_at?: Instant;
};
type InteractionResponse = Change<
  | {kind: "answer"; text: string}
  | {kind: "approve" | "decline"; action_digest: string}
  | {kind: "choose_account"; connection_id: ID}
  | {kind: "resume_computer"; session_id: ID; lease_id: string}
>;
```

Each applicable rule selector is an explicit allowlist; an empty list matches nothing. Any wildcard must be represented and reviewed explicitly, not inferred from absence. Inapplicable selectors, such as provider resources for a tool that uses none, are distinguished by the tool's validated requirements. For actions addressing people, omitted recipients authorize nobody unless an explicit recipient wildcard is granted. Deny and narrower restrictions prevail. Task authority cannot exceed connection grants or deployment restrictions. Tool-provided effect annotations do not override this evaluation. `unknown` effects require explicit bounded authority; only trusted internal operations may be `control`.

The permission result is `allow`, `deny`, or `needs_interaction`, with a reason and a private authority reference containing provenance and the evaluated scope. Authority records cite exact user inputs, rule revisions, or automation authorization. Execution rechecks revocation and task revision immediately before dispatch. No distributed system can retract a request already sent; the record must distinguish admission from in-flight execution.

Specify closed interaction payload variants when authoring the schemas: questions carry input constraints; approvals carry the prepared action summary/account/targets; account selection carries eligible connection IDs; sign-in carries a server-created auth attempt and allowed redirect; computer takeover carries a session reference. Sign-in resolves through the validated callback, not a client assertion that login succeeded.

Acceptance of a response, consumed authorization, command/delivery intent, and interaction resolution must be atomic or use an equivalent idempotent outbox. Repeated identical responses return the recorded result. Changed answers under the same request ID conflict. A stale task revision, changed action digest/account/targets, resolved question, or expired interaction rejects the response and returns current state. Declining creates no write attempt. A free-text answer may resolve clarification; it is not automatically structured approval for an unrelated write.

Project the existing `state.Question` into the new interaction view while keeping legacy text answers functional. New approval variants require actual state-layer support and typed authority evidence; translating “approve” into an unbound string answer is insufficient. Standing authorization defaults depend on D2.

## C6 — Plugins, MCP, and Computer runtime

### Package and installation contract

A package manifest contains schema version, publisher/package ID, version/content digest, runtime compatibility, dependency versions, contributions, configuration schema, required permissions, and executor declarations. Contributions are typed unions for tools, skills, prompts, resources, hooks, and UI components; each has its own schema. Skills contain instructions and required tool references, not executable authority. Hooks must name supported hook points and run within the same authority/accounting model.

An installation contains ID/revision, package reference, desired enabled state, observed lifecycle state (`installing`, `ready`, `updating`, `disabling`, `disabled`, `removing`, `failed`), configuration revision, approved permission scope, and redacted diagnostics. Activation is atomic: validate package/dependencies/schema/configuration before exposing its contributions. Updates requiring new access stop for consent. Retain old immutable packages needed by existing histories. Disable stops new admission; uninstall revokes access and stops owned processes while preserving historical evidence and explicitly handling retained artifacts.

First-party compiled App connectors can run in-process. Third-party executable packages use isolated execution with filesystem, network, time, memory, and credential scope enforced by the host. The current Computer runtime's shell jobs are a transport/lifecycle building block, not proof that this isolation already exists. If an isolation policy cannot be enforced, do not advertise that package as safely constrained. UI contributions use known declarative component types or an isolated renderer, never unrestricted application script.

### MCP adapter contract

MCP server configuration contains ID/revision, source installation or user configuration, transport (`http` or `stdio`), endpoint or managed command reference, connection/auth association, enabled state, and exposure settings. Keep credentials out of exported configuration. Negotiated protocol version, actual server capabilities, discovery revision, and process/session health are observed state, not claims copied from configuration.

Discovery preserves original names, schemas, output formats, resources/templates, prompts, and metadata. Bind exposed tools to server identity plus definition digest. Tool annotations are advisory. Map structured content and resource links into typed results with source provenance; a protocol success alone does not prove a domain write succeeded. Separate transport errors from tool failures and unresolved external effects. MCP does not supply a universal idempotency guarantee. [MCP tool contract](https://modelcontextprotocol.io/specification/2026-07-28/server/tools).

Progress, cancellation, list changes, reconnects, resource subscriptions, elicitation, roots, sampling, and remote tasks must preserve owner/task/operation association. Interactive requests enter C5; sampling incurs the parent's model budget; remote tasks remain owned work with recovery and cancellation. Negotiate only implemented features and report unsupported capabilities. Do not assume every supported MCP protocol revision has the same handshake or session semantics; the adapter owns version-specific behavior.

The Nori MCP server maps authorized external requests to these application services. It uses scoped client authentication and the same acceptance/receipt semantics; it cannot bypass Permission checks, select another user, or expose secret-bearing internal DTOs. Its protocol session is not a Nori user identity.

### Managed processes and browser ownership

New process contracts identify owner, installation, process generation, desired/observed state, working-directory/root restrictions, network scope, resource limits, and redacted log references. The actual start request uses a runtime-resolved executable/argument vector and credential injection, not a model-selected arbitrary command masquerading as a declared plugin. Restarts use a new generation so stale responses cannot attach to replacement processes.

Reuse operation receipts and task/job ownership from [computer/operations.go](../core/internal/computer/operations.go) and [computer/jobs.go](../core/internal/computer/jobs.go). Extend [RPC health negotiation](../core/internal/computer/rpc/protocol.go) before using new guest features. Preserve `Open` versus `Attach`; cleanup and inspection never wake a stopped guest solely to discover whether work happened.

A computer session projection contains session ID/revision, task association, availability, controller (`agent`, `user`, `none`), and an expiring ownership lease. Takeover fences new agent input and establishes exclusive control; resume validates the lease/task revision, releases user control, and refreshes observed browser state. Lease expiry must not silently resume side-effecting browser actions. One session cannot be controlled concurrently by agent and user. Sign-in content stays outside generic telemetry; the product presents the actual managed session, not an unrelated browser tab.

## C7 — Automations, Scheduler, goals, and delivery

```ts
type Trigger =
  | {kind: "once"; at: Instant}
  | {kind: "recurring"; local_start: string; timezone: string; rrule: string}
  | {kind: "event_relative"; event: ResourceRef; offset_seconds: number}
  | {kind: "watch"; source: ResourceRef; predicate_ref: string; min_interval_seconds: number};
type Automation = {
  id: ID; revision: Revision; title: string; instruction: string;
  status: "enabled" | "paused" | "cancelled";
  trigger: Trigger;
  connection_ids: ID[];
  authority_ref: string;
  budget_usd: number;
  max_active_runs: number;
  missed_run: "skip" | "latest" | "all";
  execution_window_seconds: number;
  destination_ids: ID[];
  next_at?: Instant;
  end_at?: Instant;
  max_occurrences?: number;
};
type Occurrence = {
  id: string; automation_id: ID; automation_revision: Revision;
  scheduled_at: Instant; source_revision?: string;
  status: "pending" | "accepted" | "running" | "completed" | "failed" | "cancelled" | "expired";
  task_ids: string[]; delivery_ids: string[];
};
```

`authority_ref` is an opaque server-issued authorization reference, not a user-writable grant. Creation requests submit the intended scope; the service records authority according to C5/D2 and returns its reference. Watch predicates are validated typed conditions or references to explicit stored instructions, not arbitrary executable code in a request.

Persist occurrence intent before waking anything. Identity includes automation, the trigger's nominal occurrence/source event, and the applicable schedule revision; retries reuse that identity. A schedule edit advances a generation and atomically invalidates unaccepted occurrences from the prior generation. Already accepted tasks keep their identities and pinned instructions; changing or stopping them requires explicit steer/stop commands, not minting a replacement occurrence with another ID. Guard against old-generation acceptance racing an edit. The definition's consumed occurrence cursor prevents the edited schedule from duplicating an already accepted slot.

The Scheduler commits task command intent with occurrence acceptance, or uses a transactional outbox that repeatedly submits the same command ID. Reuse the existing dispatch path and Waker. Do not put instructions or credentials in wake payloads. Polling/repair rediscovers due work from durable state; duplicate wakes cannot duplicate tasks. No promise of exactly-once external effects is implied by deduplicated acceptance.

Recurring local times retain timezone and rule. Proposed deterministic DST behavior: run once at the earlier instant in a repeated local time, and move a nonexistent local time to the first valid instant after the gap. Show the next occurrences before saving. One-time instants do not shift with a user's later timezone change. Event-relative triggers retain source identity/revision and react to move/cancel updates; already attempted effects cannot be retroactively undone. Missed-run defaults depend on D3. Overlap is bounded by `max_active_runs`; work exceeding the execution window expires visibly rather than accumulating indefinitely.

Delivery is a separate record: ID, occurrence/result IDs, destination, state (`pending`, `attempting`, `accepted`, `delivered`, `failed`, `uncertain`), attempt identity, receipt, and optional user acknowledgement. Provider acceptance is not user acknowledgement; channels without delivery receipts must not report `delivered` merely because submission succeeded. Deduplicate per logical result/destination; retries follow transport idempotency or reconciliation. Quiet hours affect delivery timing, not silently cancel execution. Delivery destinations require verified enrollment and user preferences.

Goals hold ID/revision, desired outcome, completion criteria, status, linked task/automation/result IDs, progress, next action, blockers, and follow-up authority. Goals reuse task execution and automation triggers; they do not create an unbounded second scheduler. Completion cites evidence and unmet criteria. Monitors are automations with source checkpoints and change-detection policy; checkpoints advance only with durable occurrence/delivery intent so crashes cannot lose meaningful changes.

## C8 — Library, domain results, and input

Preserve [state.Part](../core/internal/state/parts.go), [Result](../core/internal/state/results.go), and upload IDs. Add typed card parts by reference to authoritative server resources, with `fallback` text for older clients. A part identifies its schema version, kind, resource ID/revision, and known action kinds; it never carries executable handlers. Clients re-fetch current state before a mutation. Missing or revoked resources render an honest unavailable state instead of broken controls.

Use existing result root/version/revision relationships for Library versions. Extend provenance with connection/provider resource references, retrieval time, and copied-versus-live content status. Search returns result IDs, bounded snippets, provenance, and freshness; index entries are derived from owner-scoped records. Deletion/access changes propagate to the index and bytes, not just the visible list. A stale index hit must still pass authoritative access checks before retrieval. D4 determines disconnect cleanup, independently from explicit content deletion.

Domain contracts remain typed and preserve provider differences:

| Domain | Minimum shared shape and invariants |
| --- | --- |
| Calendar | Event reference includes connection/calendar/event identity and provider revision. Time is a discriminated union of all-day date range or timed start/end/timezone. Recurrence edits require explicit scope (`instance`, `following`, `series`) and occurrence identity where applicable. Preserve attendee/organizer roles, response status, recurrence exceptions, and supported-operation flags. Unsupported provider operations return a typed capability error; do not silently approximate an edit. |
| Email and contacts | Message/thread/draft references include account/provider identity. Draft and send are distinct operations; approval binds the exact draft content, recipients, sender, attachments, and reply target. Contacts return source-backed candidates; an ambiguous name produces selection rather than a guessed recipient. |
| Files, projects, messaging | Results retain source IDs, revisions, deep links, and operation capability. Updates use provider preconditions where supported. Preserve distinctions such as archive versus delete, labels versus folders, and post versus reply. |
| Rich tool output | Normalize text, structured data, media/files, links, and source references. Enforce size limits; store large content as artifacts. UI presentation metadata is validated separately from untrusted provider content. |
| Voice and attachments | Dictation produces an editable draft; submission uses the existing stable message ID and upload references. Voice conversation feeds the same conversation/task/interaction services. Interrupting playback is not automatically cancelling a task or retracting a send. Binary uploads/downloads retain authenticated ownership, bounded size, and explicit MIME handling, including ICS. |

Provider-specific schemas live with their adapter and are referenced by immutable tool definitions. These shared shapes give frontend stable rendering and action semantics; they are not permission to flatten all provider features into an unvalidated `metadata` bag. Each provider's supported capability matrix remains part of W6–W8 completion.

## Contract examples and verification obligations

These examples describe required behavior for fixtures and integration checks. They are proposed checks, not claims that the implementation currently passes them.

| Case | Required sequence and observable result |
| --- | --- |
| Connection update | Read connection revision 4; submit request X expecting 4; record revision 5 and receipt atomically. Retry X returns the original receipt. Request Y expecting 4 conflicts. Reusing X with a changed account conflicts. |
| Acceptance with lost wake | Task/automation intent is saved, wake fails, response reports accepted with its identity. Retrying that identity schedules delivery again without another run. A missing HTTP response alone never causes a new task ID. |
| Calendar write | Resolve explicit account/calendar; pin tool and arguments; prepare action with provider revision; check authority; record attempt; invoke. A lost provider response enters reconciliation or uncertainty. A changed event revision requires re-preparation, not overwriting the concurrent edit. |
| Stale approval | Interaction names task revision 8 and action digest A. User correction advances the task to 9 or changes action to B. Approving A fails as stale; it cannot authorize B. The UI refreshes the pending interaction. |
| Revoke during execution | Revoke before dispatch prevents a new external attempt. Revoke after send does not erase the attempt; late provider evidence is recorded. No retry occurs solely to recover a lost success message. |
| Registry update | Package v2 arrives after a call pinned v1. The pending call uses v1 or reports its implementation unavailable. New selections can use v2. Current authority applies to both. |
| Side-chat tool use | A calendar request from a side chat creates linked durable work. Operations, questions, and results appear in that chat and Work, without leaking into another conversation or executing through a separate write loop. |
| Stream recovery | Duplicate event is ignored; gap/reset fetches a snapshot and resumes. A slower management GET cannot overwrite a newer mutation response. Unknown display parts show fallback text. |
| Schedule edit/retry | Two schedulers observe one due slot. One occurrence/command identity wins; the other reuses it. Editing the schedule fences the prior generation. Missed/expired slots are visible. |
| Plugin process failure | Owned process fails after sending a remote write. Restarting the process does not automatically replay the action. Registry visibility, process health, and operation uncertainty remain separate. |
| Computer takeover | Agent input stops when a user lease is granted. Stale lease or stale task revision cannot resume execution. Cleanup on a stopped computer uses non-waking inspection. |
| Retention and delivery | Disconnect stops fetching; saved-versus-live artifacts follow D4. Delivery retry does not duplicate the same notification where the channel supports idempotency, and uncertain channels report that limit honestly. |

Before parallel implementation treats this as a baseline, encode the shared DTOs and fixtures, align Go and TypeScript representations, and verify the existing compatibility paths. The integration thread owns the fixture/acceptance record with backend/frontend contributions. Required evidence includes HTTP proxy coverage, owner isolation, mutation conflicts, approval staleness, historical workflow replay, and representative real provider actions/recovery. API type agreement alone does not prove those behaviors.

## Adoption and change control

This draft does not narrow W1–W16 or replace A01–A44. It defines shared boundaries; exact provider payloads, library choices, internal functions, and screen implementation remain engineering work. Creating the schemas and their generator/checks is implementation work, not something this Markdown claims to have completed.

Resolve D1–D4 where their dependent behavior is being implemented. Have the backend, frontend, and integration engineers independently review the proposed shapes and compatibility assumptions. Record accepted contracts by commit and retain explicit pending decisions. Neither the existence of this draft nor an absent objection counts as acceptance.

An expansion proposal names the contract ID, baseline commit, consumer need, concrete diff, compatibility/history impact, and verification. Assign one editor and have affected consumers adopt the same revision. Every substantive Astra–Sol disagreement goes to Austin with both arguments; dependent work waits and independent work continues. No thread is launched by writing or accepting this document.
