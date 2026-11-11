# r0 review: frontend

Reviewer: nori-frontend (Opus 5.5). Object reviewed: `contracts/assistant` r0-draft at `ed6b020`, read against `web/src/lib/types.ts`, `web/src/lib/chat/contract.ts`, `web/src/lib/runtime/*`, `web/src/components/**`, `core/internal/api/http.go`, `core/internal/api/chat.go` (capabilities), `core/internal/state/parts.go`.

Question asked of every screen in the plan: can it be built from these shapes without guessing, and does every action have a receipt the UI can show?

## (a) Accepted as-is

- C1 common: `Change`, `ActionRequest`, `MutationReceipt` (applied/queued + `work`), `APIError` (with `accepted` + `receipt` on 503), `ListResponse`, `GET /v1/mutations/{request_id}`. The client will persist `request_id` per pending mutation in sessionStorage (same pattern as today's pending turns and task commands) and resolve unknown outcomes through the receipt lookup.
- C1 sync rule: polling plus focus/reconnect refetch, `revision` comparison so older responses never overwrite newer state. Conversation SSE unchanged.
- C3 `ConnectionAction` (connect/repair/disconnect/enable/disable), `ConnectionChange`, `Resource` (with `selected`, `writable`).
- C5 `Interaction` kinds and closed payloads, `InteractionResponse` variants, 409 on stale with current state returned.
- C6 `Installation` + `InstallationAction` (update/rollback/pin/enable/disable/uninstall), `MCPServer.observed`, `ManagedProcess`, `ComputerSession` + `ComputerSessionAction` (take_over/renew/release/resume with lease and instruction_revision).
- C7 `Trigger` union, `AutomationAction` (incl. snooze), `GoalAction`, `Delivery` states, `DeliveryDestination`.
- C8 `Provenance` (generated/uploaded/copied/live + connection_status), `LibraryItem` versions over existing SavedResult, `UploadAccept`.
- Domain: `CalendarTime`, `CalendarEvent`, `Attendee`, `AvailabilitySlot` (unknown is not free), `MailMessage` (snippet vs full), `MailDraft` (full recipient lists always present), `ContactCandidate`.
- C4 `OperationProjection` with `authority_source`, `account_summary`, `action_summary`, `receipt_url`.

## (b) Corrections and expansions needed

Each item: contract and revision / consumer need / proposed change / affected consumers and histories / compatibility / verification. All are additive new defs or new optional fields unless marked.

### F1. Integration catalog entry (C3, r0-draft)
- Need: Apps is a catalog of integrations (Google Calendar, Gmail, HEY, Linear, custom MCP), each with zero or more Connections. r0 only has `Connection` (an account) and `AvailableTool` (a tool). The catalog, categories, "connect another account", per-purpose defaults and capability matrix cannot be built without guessing.
- Change: new `connections.schema.json#/$defs/Integration`:
  `{id, title, description, categories: string[], icon?: string (known icon key, not a URL), source: "native"|"plugin"|"mcp", installation_id?, auth: "oauth"|"app_password"|"api_key"|"none", multiple_accounts: boolean, purposes: [{id: "calendar.create", title}], capabilities: [{id, title, effect, supported: boolean, reason?: string, scopes?: string[]}], availability: "available"|"unavailable"|"blocked", availability_reason?: string, connection_ids: ID[]}`. Route `GET /v1/integrations` (+ `?q=&category=`), `GET /v1/integrations/{id}`.
- Affected: backend (catalog projection from registry + connections), frontend Apps. No stored history.
- Compat: additive. Verification: fixtures for a ready native integration, a blocked one (missing provider credential, e.g. Microsoft Graph), an MCP-sourced one.

### F2. Work item projection (C4/C5, r0-draft)
- Need: Work buckets Needs you / Working / Scheduled / Finished, cards with origin, pending interactions, cost, uncertain operations, results. Today's `Run` lacks origin, title and interaction links; Scheduled comes from automations.
- Change: new `execution.schema.json#/$defs/WorkItem`:
  `{task_id, title, status: Run.status enum, bucket: "needs_you"|"working"|"finished", instruction_revision, origin: {kind: "conversation"|"automation"|"goal"|"external", conversation_id?, message_id?, automation_id?, occurrence_id?, goal_id?}, interaction_ids: string[], progress?: string, operations: {total, uncertain, failed, submitted}, result_ids: string[], cost: Cost & {budget_usd?}, parent_task_id?, started_at, finished_at?, cancel_requested: boolean, error?: ErrorDetail}`. `GET /v1/work?bucket=&cursor=`, `GET /v1/work/{task_id}` returns WorkItem plus `operations: OperationProjection[]`, `interactions: Interaction[]`. Scheduled bucket is `GET /v1/automations?status=enabled` ordered by `next_at`; no need to duplicate.
- Affected: backend projection over existing runs; old runs map to origin conversation `assistant`. Compat: additive. Verification: fixtures for each bucket and a run with an uncertain operation.

### F3. Summary for badges (C1, r0-draft)
- Need: nav badges (Needs you count, Apps needing attention, unread inbox) on every screen without fetching four lists on each focus.
- Change: `common.schema.json#/$defs/AssistantSummary` `{revision, needs_you: int, working: int, apps_attention: int, inbox_unread: int, next_scheduled_at?}` at `GET /v1/assistant/summary`. Polled by the shell.
- Compat: additive. Verification: one fixture.

### F4. Feature availability per area (C1, r0-draft)
- Need: backend lands areas piecemeal; a single `nori-assistant-1` label is either premature or blocks finished areas. Screens must "check actual feature availability and never present unfinished capabilities as usable".
- Change: `/v1/capabilities` adds `assistant_contract: "nori-assistant-1"` and `assistant_features: {integrations, connections, plugins, mcp, interactions, work, automations, goals, library, search, permissions, preferences, memory, computer_sessions, inbox, operations}` booleans, each true only when that route family is implemented and verified. `upload_accept: UploadAccept` added there too (see F15).
- Compat: additive to an existing payload; old clients ignore it.

### F5. Automation projection: readable authority instead of authority_ref (C7, r0-draft) — correction
- Need: D2 requires "users see the exact ongoing authority they are granting"; Automations detail must show it. `Automation.authority_ref` is required on the public projection, which contradicts the README rule that projections never carry authority refs, and gives the UI nothing readable.
- Change: remove `authority_ref` from `Automation` and `Goal` projections; add `authority: {action_classes, tool_ids, connection_ids, resource_ids, recipients?, max_operations?, expires_at?, summary: string}` (server-rendered readable summary plus the exact selectors). Add `snoozed_until?: Instant` (snooze exists as an action with no visible state) and `last_occurrence?: {id, status, scheduled_at, summary?}`.
- Affected: backend projection only; no stored history (authority_ref stays internal). Compat: removal from an unaccepted draft; nothing consumes it yet.

### F6. Automation preview before save (C7, r0-draft)
- Need: "show the next occurrences before saving"; DST rules (earlier instant on repeat, shift forward on gap) are server-defined, so client RRULE expansion would diverge.
- Change: `POST /v1/automations/preview` body `AutomationEditable` → `AutomationPreview {next_runs: Instant[] (up to 5), timezone, authority: (as F5), missed_run_explanation: string, warnings: [{code, message}] (dst_shift, outside_working_hours, needs_connection, needs_permission)}`. Read-only, no request_id.
- Verification: fixture for a weekly rule crossing a DST boundary.

### F7. Occurrence history shows missed work (C7, r0-draft)
- Need: D3 says missed work is always visible; `skip` policy has no status to show.
- Change: add `"skipped"` to `Occurrence.status`, `reason?: "missed"|"superseded"|"outside_window"|"cancelled_by_edit"`, and inline `deliveries?: Delivery[]` on occurrence list responses to avoid N+1 reads.

### F8. Card resource read routes (C8, r0-draft) — correction
- Need: every card re-fetches current server state before acting. `CardPart.resource.kind` is a free string, and calendar events, mail drafts, availability proposals and files have no read route, so a calendar or draft card cannot re-fetch.
- Change: constrain `resource.kind` to an enum mapped to routes: `interaction` → `/v1/interactions/{id}`, `automation` → `/v1/automations/{id}`, `goal` → `/v1/goals/{id}`, `connection` → `/v1/connections/{id}`, `installation` → `/v1/plugins/installations/{id}`, `computer_session` → `/v1/computer/sessions/{id}`, `task` → `/v1/work/{id}`, `result` → `/v1/library/{id}`. Domain objects (calendar event, mail draft/message, availability proposal) are saved results: add `LibraryItem.data?` as `oneOf CalendarEvent | MailDraft | MailMessage | AvailabilityProposal` with `data_kind` discriminator. Add card kind `availability` and def `AvailabilityProposal {slots: AvailabilitySlot[], duration_minutes, attendees, status: "proposed"|"booked"}` (a proposed slot and a booked event differ).
- Affected: backend parts writer, Library projection. Compat: additive to Part (see F9).

### F9. Card parts inside existing message parts (C8 / state.Part, r0-draft)
- Need: cards must arrive on the messages the UI already reads (`/v1/snapshot` messages and conversation messages).
- Change: `state.Part` gains optional `schema_version`, `kind`, `resource`, `actions` when `type == "card"`; `fallback` required as today. Frontend renders known kinds and falls back to `fallback` text. Old clients already show fallback. Please confirm backend will emit cards there rather than on a new message shape.

### F10. Interaction resolution and replacement (C5, r0-draft)
- Need: after a 409 the UI must jump to the replacement interaction; resolved cards must say what was answered.
- Change: add `superseded_by?: ID`, `resolution?: {kind: InteractionResponse value kind, text?, connection_id?, at: Instant}`, `task_title?: string`. Make top-level `action_digest` the one source of truth; payload's copy must equal it (checker rule) or drop it from `ApprovalPayload`.

### F11. Connection health codes and repair (C3, r0-draft) — correction
- Need: health states in the plan (ready, connecting, disconnected, disabled, needs sign-in, needs more access, rate-limited, temporarily failing) drive distinct repair actions. `Health.code` is a free string, so the UI would guess.
- Change: `Health.code` enum: `ready|needs_sign_in|needs_more_access|revoked|rate_limited|temporarily_failing|provider_unavailable|credential_store_unavailable`; add `recovery?: "connect"|"repair"|"request_access"|"wait"`, `missing_scopes?: string[]`, `affected_capability_ids?: string[]`, `retry_after?: Instant`. Add `AvailableTool.recovery` values `request_access` and `install`.

### F12. Library mutations (C8, r0-draft)
- Need: rename, organize, revise, export, delete with stated effect. No mutation shapes exist.
- Change: `LibraryItemEditable {title, collection?: string}` via `Change`; `LibraryAction` oneOf `{action: "revise", instruction}` (queued, returns `work`), `{action: "export", format: "md"|"pdf"|"docx"|"csv"|"ics"|"original"}` (returns a file URL), `{action: "delete", scope: "version"|"all_versions"}`. Add `LibraryItem.collection?`, `preview_text?` (bounded), `delete_effect?: string` (server-stated effect, e.g. "Removes this saved copy. The Gmail message stays in Gmail."). `GET /v1/library?collection=&kind=&cursor=`.

### F13. Search jump targets and filters (C8, r0-draft) — correction
- Need: task, goal, automation and library hits have no conversation; `SearchHit.jump` requires `conversation_id`.
- Change: `jump` oneOf `{kind: "conversation", conversation_id, message_id?}|{kind: "task", task_id}|{kind: "library", item_id}|{kind: "goal", goal_id}|{kind: "automation", automation_id}`. Request: `GET /v1/search?q=&kinds=&from=&to=&cursor=`.

### F14. Preferences, memory, action history, inbox (C1/C7, r0-draft) — missing
- Preferences: `Preferences {revision, timezone, locale, working_hours: [{day: 0-6, start: "HH:MM", end}], quiet_hours?: {start, end}, default_destination_ids: ID[], budgets: {task_usd, monthly_usd?}, input: {dictation_language?}}` + `PreferencesEditable`/`Change` at `/v1/preferences`. Per-purpose account defaults stay on `Connection.defaults`; sender identities are resources of mail connections.
- Memory: `MemoryFact {id, revision, text, scope: "user"|"chat", origin: {conversation_id?, message_id?, task_id?}, created_at, updated_at}`; `GET /v1/memory`, edit via `Change {text}`, forget via `ActionRequest {action: "forget"}` with receipt. Forgetting does not claim transcript deletion; the UI says only what the receipt says.
- Action history: `GET /v1/operations?connection_id=&effect=&cursor=` listing `OperationProjection` across tasks; add `task_title?` and `recovery?: "none"|"reconciling"|"needs_review"|"resolved"`.
- Inbox (A28 deep links): `InboxItem {id, delivery_id?, title, body, link: CardPart.resource, created_at, acknowledged_at?}` at `GET /v1/inbox`, acknowledge via `ActionRequest`.

### F15. Upload accept discovery (C8, r0-draft)
- Need: composer `accept` and the uploader must agree (ICS today is rejected by `core/internal/state/uploads.go`).
- Change: deliver `UploadAccept` in `/v1/capabilities.upload_accept` (F4). Add optional `extensions: string[]` so the file picker can include `.ics`, `.md`, `.docx`.

### F16. Plugins: catalog, install, update review (C6, r0-draft) — missing
- Need: catalog cards, install flow, changed-permission review before update, dependents shown before uninstall.
- Change: `CatalogEntry {package: DefinitionRef, title, description, publisher: {id, name}, categories, latest_version, provenance: {source: "registry"|"repository"|"upload", verified: boolean}, requested_access, contributions_summary: {tools, skills, prompts, mcp_servers}, installation_id?}` at `GET /v1/plugins/catalog`. `InstallationCreate` via `Change` value `{package: {id, version}, approved_access, configuration?}`. `InstallationReview {from_version, to_version, tools_added, tools_removed, schemas_changed, access_added, access_removed, dependents: {automation_ids, goal_ids}}` at `GET /v1/plugins/installations/{id}/review?version=` (also used for uninstall with to_version absent).

### F17. MCP server management mutations (C6, r0-draft) — missing
- Change: `MCPServerEditable {name, transport, endpoint?, command_ref?, environment_refs?, auth, enabled, exposure}` via `Change`; `MCPServerAction` oneOf `connect|reconnect|sign_in|sign_out|start|stop|restart` and `{action: "set_exposure", tool: string, exposure}`; `GET /v1/mcp/servers/{id}/tools` → `[{name, title, exposure, definition: DefinitionRef, availability}]`; `observed.diagnostics?: [{at, level, message}]` (redacted). `GET /v1/mcp/servers/{id}/export` returns redacted config.

### F18. Skills management (C6, r0-draft)
- Change: `SkillEditable {title, description, instructions, inputs, required_tool_ids, examples?, scope}`; routes `/v1/skills` list/create/update (revision creates a new `ref.version`), import from text, export.

### F19. Computer session ownership queue (C6, r0-draft)
- Need: "explicit ownership when several tasks need the same session".
- Change: `ComputerSession.waiting_task_ids?: string[]`, `task_title?`.

## (c) Wrong in r0, with evidence

1. `Automation.authority_ref` (required) and `Goal.authority_ref` on public projections contradict `contracts/assistant/README.md` ("Projections never carry ... authority references") and give D2 nothing readable to show. See F5.
2. `SearchHit.jump` requires `conversation_id`, but `SearchHit.kind` includes `task`, `goal`, `automation`, `file`, which have no conversation. See F13.
3. `CardPart.resource.kind` is unconstrained and calendar/mail/file cards have no read route, so baseline C8's "Clients re-fetch current state before a mutation" cannot be satisfied for those kinds. See F8.
4. `Health.code` is a free string while the plan's health states map to different repair actions. See F11.
5. Snooze exists (`AutomationAction`) with no visible snoozed state; `skip` missed-run policy has no occurrence status to show. See F5, F7.
6. No create shape for installations, MCP servers, skills or library edits: those screens have no receipt-backed mutation. See F12, F16–F18.
7. `Interaction.action_digest` and `ApprovalPayload.action_digest` are two sources of truth. See F10.

## (d) D1–D4 as the user sees them (Austin decides)

- D1 credentials: invisible when it works. The only user-facing need is a distinct health code (`credential_store_unavailable`) so Apps can say an account cannot be saved rather than "sign-in failed". No preference between the two backings from the UI side; r0's recommendation is fine.
- D2 standing authority: creation shows the exact standing scope (accounts, actions, recipients, limits) from the preview (F6) with one control, "Ask me before sending", which removes `sensitive_send` from `requested_authority`. Until D2 is resolved that control starts on and the UI says so. I support r0's recommendation; the UI is the same either way, only the starting state differs.
- D3 missed runs: creation shows the missed-run choice in plain words ("Run once when I'm back" = latest, "Skip it" = skip, "Run every missed one" = all), with the server explanation from the preview. History always shows skipped and expired occurrences with reason (F7). I support r0's recommendation; whatever Austin picks becomes the preselected option, never a hidden default.
- D4 disconnect: the disconnect dialog has one primary action, "Disconnect", plus an unchecked option "Also delete content imported from this account". Saved items then show "Saved copy · account disconnected" from `provenance.connection_status`. Uninstall is a separate action in a separate place. I support r0's recommendation.
