# Nori contract location map

Nori's contracts are distributed across Go packages, HTTP handlers, TypeScript clients, persisted records, and the computer protocol. There is no single existing contract package that backend and frontend can adopt as the source for every boundary. The immediate coordination task is to identify which boundary a change crosses and which definitions and consumers must move together.

This map identifies locations and ownership, not new schemas, field lists, endpoint designs, or migration decisions. It accompanies the [complete implementation plan](assistant-integration-plan.md). Paths are relative to the repository; the inspected baseline is `c53931359ac2e0f95dacb5368c517cb3cde976a9`.

The separate [contract baseline draft](assistant-contract-baseline.md) now proposes shapes and behavior for these boundaries. It distinguishes existing behavior from recommended additions and flags decisions for Austin; this map remains the current-code location reference.

## The main frontend/backend boundaries

Two API/client surfaces coexist. The main assistant runtime uses the runtime API and its TypeScript definitions. The unified conversation API has a separate projection and client contract, used by the separate-chat client. Changes to one do not automatically update the other. The unified API can also project assistant conversations; its existence does not mean the main runtime UI has migrated to it.

| Boundary | Definitions and behavior on the server | Client definitions and consumers |
| --- | --- | --- |
| Main assistant runtime requests and snapshot | [api/http.go](../core/internal/api/http.go), [api/tasks.go](../core/internal/api/tasks.go); snapshot types in [conversation/runner.go](../core/internal/conversation/runner.go), assembly in [conversation/session.go](../core/internal/conversation/session.go), underlying records in [state/state.go](../core/internal/state/state.go). | [lib/types.ts](../web/src/lib/types.ts), [runtime/useRuntime.ts](../web/src/lib/runtime/useRuntime.ts), [runtime/methods.ts](../web/src/lib/runtime/methods.ts), [runtime/paths.ts](../web/src/lib/runtime/paths.ts), [NoriApp.tsx](../web/src/components/runtime/NoriApp.tsx). |
| Runtime event stream and reconnect behavior | [api/events.go](../core/internal/api/events.go), [state/events.go](../core/internal/state/events.go). | [runtime/useRuntime.ts](../web/src/lib/runtime/useRuntime.ts) and its runtime state consumers. The stream's handling is part of the contract, not just its event record types. |
| Unified conversation requests, projections, capabilities, and events | [api/conversation_contract.go](../core/internal/api/conversation_contract.go), [api/conversation.go](../core/internal/api/conversation.go), [api/conversation_events.go](../core/internal/api/conversation_events.go), [api/chat.go](../core/internal/api/chat.go). | [chat/contract.ts](../web/src/lib/chat/contract.ts), [chat/useChat.ts](../web/src/lib/chat/useChat.ts). The client contract label is `nori-chat-2`; the capabilities response also advertises `nori-conversations-1`. |
| Web proxy routing and authenticated scope | [api/users.go](../core/internal/api/users.go), [users/users.go](../core/internal/users/users.go), route registration in [api/http.go](../core/internal/api/http.go). | [runtime/forward.ts](../web/src/lib/runtime/forward.ts), [runtime/auth.ts](../web/src/lib/runtime/auth.ts), [runtime/credentials.ts](../web/src/lib/runtime/credentials.ts), and Next route adapters under [app/api](../web/src/app/api). A new backend endpoint is not automatically available through the proxy. |
| Message parts, saved results, attachments, and binary retrieval | [state/parts.go](../core/internal/state/parts.go), [state/results.go](../core/internal/state/results.go), [state/uploads.go](../core/internal/state/uploads.go), [api/uploads.go](../core/internal/api/uploads.go), and the API projections above. | [lib/types.ts](../web/src/lib/types.ts), [chat/contract.ts](../web/src/lib/chat/contract.ts), runtime/chat renderers, upload and result routes under [app/api](../web/src/app/api). |

Backend and frontend jointly own these boundaries; integration owns evidence that both sides agree. The Go projection declarations and hand-maintained TypeScript types are separate sources that currently have to stay aligned. The inspected tree and build scripts do not provide a shared API schema package or an API-to-TypeScript generation step. Next's route type generation is not such a step.

For parallel development, these are the first locations to settle ownership and synchronization. Whether to converge the two API surfaces, generate types, or introduce a new schema package is subsequent design work. This map does not decide those questions.

## Engine and persistence boundaries

These contracts matter even where the frontend never imports a type. Changing an HTTP projection alone does not change task admission, execution, stored history, or retry behavior.

| Boundary | Where the contract lives | Consumers and coordination implications |
| --- | --- | --- |
| Model-facing tool declarations and schemas | [transcript/tool.go](../core/internal/transcript/tool.go), [transcript/transcript.go](../core/internal/transcript/transcript.go); outer/chat declarations in [conversation-tools.json](../core/internal/prompts/conversation-tools.json) and [chat-tools.json](../core/internal/prompts/chat-tools.json). | Model adapters, [agent/model.go](../core/internal/agent/model.go), conversation command handling, and tool execution. This is where a registry must meet the existing model-facing representation. |
| Capability discovery, tool policy, and execution dispatch | [agent/catalog.go](../core/internal/agent/catalog.go), [agent/tools.go](../core/internal/agent/tools.go), [agent/agent.go](../core/internal/agent/agent.go). | Agent/tool execution and durable task execution. The current responsibilities are distributed; no single existing interface already represents the complete proposed plugin/connection/adapter system. |
| Conversation-to-task commands, acceptance, and delivery | [state/commands.go](../core/internal/state/commands.go), [state/run.go](../core/internal/state/run.go), [conversation/tasks.go](../core/internal/conversation/tasks.go), [dispatch/orchestrator.go](../core/internal/dispatch/orchestrator.go), [dispatch/dispatcher.go](../core/internal/dispatch/dispatcher.go). | Conversation handlers, storage, [task/client.go](../core/internal/task/client.go), and [task/workflow.go](../core/internal/task/workflow.go). This is the outer conversation/inner durable execution boundary. |
| External operation authority, evidence, and outcomes | [state/evidence.go](../core/internal/state/evidence.go), [task/tool_durable.go](../core/internal/task/tool_durable.go). | Tool admission/execution, retry/reconciliation, completion checks, and user-visible operation projections. Native providers, plugins, and MCP must connect here rather than establish unrelated outcome semantics. |
| Workflow-to-activity serialization and replay | [task/history.go](../core/internal/task/history.go), [task/activities.go](../core/internal/task/activities.go), [task/workflow.go](../core/internal/task/workflow.go), [taskworker/register.go](../core/internal/taskworker/register.go). | Temporal workers and stored workflow histories. These contracts are separate from public HTTP DTOs and can outlive a deployment. |
| Application-facing storage interfaces | [state/contracts.go](../core/internal/state/contracts.go), [state/chat.go](../core/internal/state/chat.go). | Conversation/task services and concrete storage implementations. `state/contracts.go` is a storage interface collection, not the frontend API specification. |
| Persisted domain record formats | [state/state.go](../core/internal/state/state.go), [state/run.go](../core/internal/state/run.go), [state/commands.go](../core/internal/state/commands.go), [state/evidence.go](../core/internal/state/evidence.go), [state/chat.go](../core/internal/state/chat.go), parts/results/uploads files above. | [state/records](../core/internal/state/records), API projections, recovery, and backend implementations. A stored record and a client projection are different compatibility boundaries even when they share fields today. |
| Record-store backend abstraction | [state/docstore/store.go](../core/internal/state/docstore/store.go). | Shared record implementation and Firestore/Postgres backends. This is below the product-level storage interfaces; new product capabilities do not automatically require expanding it. |
| User ownership and scope | [users/users.go](../core/internal/users/users.go), [api/users.go](../core/internal/api/users.go), scoping requirements in [state/contracts.go](../core/internal/state/contracts.go). | API routing, opened stores, workers, and all future per-user integration records. |
| Wake delivery | [wake/wake.go](../core/internal/wake/wake.go) and implementations under [wake](../core/internal/wake). | Dispatch and hosting adapters. This is an existing delivery seam, not a complete user-facing schedule/routine contract. |
| Secret resolution | [secret/secret.go](../core/internal/secret/secret.go) and implementations under [secret](../core/internal/secret). | Runtime/platform credential lookup. It is not already a multi-account OAuth connection lifecycle. |

Backend is the primary implementation owner for these boundaries. Integration shares responsibility for compatibility and behavioral evidence. Frontend participates wherever their outcomes become visible state, actions, errors, or recovery controls.

## Computer and process boundary

| Boundary | Definition locations | Consumers |
| --- | --- | --- |
| Computer provisioning and lifecycle | [computer/provider.go](../core/internal/computer/provider.go), [computer/computer.go](../core/internal/computer/computer.go). | Platform providers and agent/task execution. |
| Host/guest RPC paths and capability negotiation | [computer/rpc/protocol.go](../core/internal/computer/rpc/protocol.go). | Computer client and [computerd/server.go](../core/internal/computerd/server.go). |
| Computer operations, jobs, and receipts | [computer/operations.go](../core/internal/computer/operations.go), [computer/jobs.go](../core/internal/computer/jobs.go), [computerd/receipts.go](../core/internal/computerd/receipts.go), operation handlers under [computerd](../core/internal/computerd). | Tool runtime, durable operations, guest execution, and eventual UI ownership/takeover controls. Payload definitions are not all centralized in the RPC protocol file. |

Managed stdio MCP and plugin processes will cross this boundary if hosted on the computer. Browser takeover also crosses the execution/UI boundary. Their precise hosting and ownership contracts remain design decisions; a new UI control alone cannot define guest behavior.

## Where the missing boundaries must connect

The following are planned seams, not implemented contracts. The location column identifies the adjacent existing code and affected owners. It does not assign speculative new filenames or settle a package layout.

| Missing or expanded boundary | Where it must connect | Engineers who must coordinate |
| --- | --- | --- |
| Registry-to-native/MCP/plugin adapters | `agent` tool discovery/dispatch, `transcript` tool definitions, `task` durable invocation. | Backend and integration; frontend for discoverable capability and availability projections. |
| Plugin package, installation, contribution, and executor lifecycle | New package/runtime definitions adjacent to registry work; `state` persistence; `api` management; computer/process boundary where used. | All three implementation streams. |
| User connection, account selection, grants, and repair | `users`, `secret`, `state`, API projections and proxy routes, web Apps/settings consumers. | All three. |
| Provider adapters and cross-provider workflows | Registry/invocation seam, per-user connections, durable operation evidence, API result/interaction projections. | Backend and integration; frontend for supported workflows and recovery. |
| MCP client/server protocol adaptation | Tool/schema representation and invocation, connection lifecycle, remote transport or computer process execution; new public server surface. | Backend and integration; frontend for setup, capabilities, and interactive requests. |
| User permission and pending-interaction lifecycle | Task admission/evidence, conversation commands, API snapshots/events, frontend action controls. | All three. |
| Schedules, occurrences, delivery, goals, and monitors | New domain records through application storage interfaces; `wake`/dispatch/task execution; API and web state/actions. | All three. |
| Library, search, and richer results | Existing results/uploads/parts, new indexing boundary, API retrieval and events, frontend viewers and navigation. | All three. |
| Computer ownership, takeover/resume, and input | Computer RPC/provider lifecycle, task/conversation state, API events and actions, frontend controls. | All three. |
| Shared compatibility fixtures and acceptance evidence | The definitions and consumers listed above, existing package checks, and W16's cross-stream verification. | Integration coordinates; backend/frontend supply and fix their respective behavior. |

Some of these are internal Go boundaries; others need synchronized Go/TypeScript representations; others cross deployment or third-party protocol boundaries. They do not all belong in one universal `contracts` directory. Choose the physical home of each new definition when its owners establish its contract, retaining existing package ownership where it works. Keep this map as the index so a developer can locate both the definition and its consumers.

## Documentation and the next design discussion

[API documentation](site/api.html), [execution contract](site/contract.html), [records](site/records.html), [authority](site/authority.html), [tasks](site/tasks.html), [computer](site/computer.html), [catalog](site/catalog.html), and [tools](site/tools.html) explain intended behavior and implementation. They are useful references, not generated specifications guaranteeing that Go and TypeScript agree.

The contract baseline draft addresses application API/projections/events; registry-to-adapter invocation; connection/authority-to-execution; rich results and pending interactions; schedule-to-task/delivery; and computer/process ownership. Independent engineering review should check each proposed shape against the definitions and consumers here. Internal implementation details remain with their owning engineer.

This location audit creates no new API, schema, package, migration, or thread. Any disagreement about the subsequent contracts follows the plan's Austin-guided decision process.
