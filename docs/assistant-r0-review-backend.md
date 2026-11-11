# r0 review: backend

Reviewer: backend session (Opus 5.5). Object: r0 docs `2a5d5bd` and the r0-draft encoding in `contracts/assistant` at `ed6b020`. Evidence is from the code at `ed6b020`.

## (a) Accepted as-is

- C1 conventions: snake_case for new payloads, UUID `request_id`, `Change`/`ActionRequest`, `MutationReceipt` applied/queued, `APIError` with `accepted`, `{items, next_cursor}`, status codes, `GET /v1/mutations/{request_id}`, receipts deduplicated by owner + request id and persisted with the resource. Fits the docstore: one transaction writes `owners/{o}/<collection>/{id}` and `owners/{o}/mutations/{request_id}`.
- Polling for management resources, no new event bus. `nori-assistant-1` advertised only when implemented.
- C3 lifecycle semantics: one-time owner-bound auth attempts, disconnect closes admission first and advances an authorization epoch, refresh coordinated by conditional credential version, scope escalation as a new sign-in.
- C4: `OperationRecord` stays the only ledger; existing outcome vocabulary and `outcomeMayFollow` unchanged; `Prepare/Execute/Reconcile/Cancel`; uncertain blocks dependent writes; adapters never retry on their own.
- C5 permission semantics (empty selector matches nothing, deny wins, `unknown` needs explicit authority) and the closed interaction payloads keyed by `kind`. Atomic response acceptance.
- C7 occurrence identity/generation/outbox model, Delivery separate from execution, DST rule, `missed_run` stored per definition.
- C8 card parts by reference with fallback text.
- `OperationProjection`, `Interaction`, `InteractionResponse`, `Occurrence`, `Delivery`, `PackageManifest`, `Installation`, `MCPServer`, `ManagedProcess`, `ComputerSession` shapes as encoded.

## (b) Corrections and expansions needed

### B1. `ToolDefinition.model_name` must not allow `.` (C2, r0-draft)
- Need: production model calls go to OpenAI (`c539313`). OpenAI function names must match `^[a-zA-Z0-9_-]{1,64}$`; a `.` is rejected by the provider.
- Change: `model_name.pattern` → `^[A-Za-z0-9_-]{1,64}$`. Namespacing for MCP/plugin tools uses `__` (e.g. `gcal__events_list`, `mcp_<server-slug>__<tool>`), with a stable short hash suffix when the name would exceed 64. Fixture: `tools/tool-definition.dotted-name.invalid.json`.
- Affected: none stored yet. Compatible.

### B2. `Policy` must carry the execution metadata the workflow branches on (C2/C4)
- Need: `task/workflow.go runTool` reads `agent.TaskToolPolicy(name)` for `Runtime` (whether the call touches the computer, which drives `ComputerRevisions` and computer stop) and `InterruptedCostUSD/InterruptedCostUnknown` (accounting when the activity fails). `tool_durable.go` uses `Effect` and `InterruptedCostUnknown` for interruptibility. The encoded `Policy` has only effect/retry/timeout, so a dynamic tool cannot be executed without a global name lookup.
- Change: add to `tools.schema.json#/$defs/Policy`:
  `location: "runtime" | "computer"` (required), `interrupted_cost_usd: number ≥ 0` (default 0), `interrupted_cost_unknown: boolean` (default false), `cancellation: "cooperative" | "none"` (optional). Fixture: built-in `web_search` definition with `location: runtime, interrupted_cost_usd: 0.005`.
- Affected: none stored. Compatible.

### B3. Pin execution metadata on `CallRef`; no workflow version gate is needed (C4 Temporal compatibility; docs-only correction)
- r0 says "add a versioned workflow path … explicit workflow version gate where command sequences change". The command sequence does not change: `ModelRef` already returns `[]CallRef` as an activity result recorded in history. I add an optional `Exec *ExecMeta` (definition ref + binding digest + the B2 policy) to `CallRef`, filled in the activity. The workflow uses `call.Exec` when present and `TaskToolPolicy(name)` when nil. Old histories decode to nil and replay the old path exactly; new histories carry their pinned metadata; no live registry lookup in workflow code. `GetVersion` is only needed if a future change adds or reorders commands.
- Evidence: `history.go CallRef{ID, Name, Index}`, `finishModelRound`, `workflow.go runTool`. Workers are already pinned by Worker Deployment build (`taskworker/register.go`).
- Affected: Temporal history only (private). Verification: replay a retained pre-change history with the new worker (Temporal replayer) plus a new history with a dynamic tool.

### B4. Reuse `OperationRecord.Action` for the action digest (C4)
- `state/evidence.go` already has `Action{Digest, Text, Spec}` ("Digest covers the tool, account and Spec") and `PlanOperation` already treats a changed digest as an identity conflict. `PreparedAction.digest` should be stored there, not in a new `arguments_digest`.
- Additive `OperationRecord` fields I will add, all immutable once set and added to `PlanOperation`'s identity check: `tool` (DefinitionRef), `binding_digest`, `connection_id`, `authority_ref`, `parent_operation_id`, `provider_refs[]`. `arguments_ref` is unnecessary: arguments are already the call in the history chain, addressed by `ToolRef(index)`.
- Schema impact: none for public schemas; `Invocation.arguments_digest/arguments_ref` become optional in the private `Invocation`.

### B5. No `authority_ref` in `Automation` and `Goal` projections (C7)
- The README rule says projections never carry authority references, but `Automation.required` includes `authority_ref` and `Goal` has one. Replace with `authority_rule_ids: ID[]` — the PermissionRules created for that automation/goal, readable at `/v1/permissions/{id}` with an `automation_id`/`goal_id` selector. This also is exactly the D2 interim mechanism.
- Change: `Automation`: remove `authority_ref`, add required `authority_rule_ids`; `Goal`: replace `authority_ref` with optional `authority_rule_ids`; add `goal_id` to `PermissionRule`/`PermissionRuleEditable`. Update the automation fixtures; add invalid fixture with `authority_ref` on a projection.

### B6. Connection health needs a closed code enum and a scope view (C3)
- Need: the plan's health set (ready, connecting, disconnected, disabled, needs sign-in, needs more access, rate-limited, temporarily failing) has to drive repair UI without parsing messages; plan requires granted scopes stored separately from desired capabilities.
- Change: `Health.code` → enum `ok | needs_sign_in | needs_more_access | rate_limited | temporarily_failing | revoked | provider_error`; `Health.recovery` → `connect | repair | request_access | wait | none`; `Connection.missing_scopes: string[]` (desired minus granted) and `Connection.capabilities: string[]` (purposes this grant supports, e.g. `calendar.read`, `calendar.write`, `mail.send`).

### B7. Auth start and callback shapes (C3)
- Need: r0 says "auth start returns a bounded sign-in interaction" but there is no response schema and no callback contract, which frontend and proxy both need.
- Change: `ConnectionAction value {kind: "connect", capabilities?: string[], return_to?: string}` → `202 MutationReceipt{status: queued, resource: Connection(status: connecting)}` plus `auth: AuthAttempt{id, authorize_url, expires_at}` in the receipt (`MutationReceipt.auth`, optional). Callback: `GET /v1/connections/oauth/callback?state&code&error` (user-scoped through the web proxy; the attempt is looked up by `state` within that owner only) returns `303` to the attempt's `return_to` with `?connection=<id>&result=connected|cancelled|failed`. `return_to` must be a relative path inside the web app.

### B8. Account defaults belong in preferences, not on each connection (C3)
- A default per purpose is unique across connections, so setting it on one connection mutates another's revision. Keep `Connection.defaults` as a read-only derived field and put the source of truth in `/v1/preferences` as `account_defaults: {purpose: connection_id}` under one revision guard. `ConnectionEditable.defaults` is removed.

### B9. `AvailableTool` grouping fields (C2)
- UI and the capability explanation (A04) need grouping and integration identity without a second lookup. Add `capability: string` (stable capability id, e.g. `calendar`, `web_search`), `integration_id?: string`, `effect`, `exposure`.

### B10. Reserved names (C6)
- `ReservedToolName` lists control names only. Built-in model names (`shell`, `web_search`, `browser_navigate`, `result_*`, `memory_read`, `operation_read`, …) also live in the model namespace. Rule to encode: model names are unique in the effective tool set; built-ins win; external definitions colliding with any built-in or another installed tool are rejected at install. Add `operation_read`, `task_history_read`, `tool_search` to the reserved set.

## (c) Things in r0 I believe are wrong

1. "Approval mismatch … `continue/approve/decline` are not a working approval lifecycle" — correct, and further: the retired code at `ec38dfe6^` had an approval/claim path that was removed. The `ready/attempted/submitted/voided` outcomes and `LateWriteEvent` remain from it. New approvals should use those outcomes (`ready` = prepared and awaiting approval, `voided` = declined/superseded) rather than inventing new ones.
2. "Explicit workflow version gate" — see B3; not needed for this change, and adding one when the command sequence is unchanged only adds a permanent branch.
3. The retired Google integration stored the refresh token in plaintext on the user row (`users.Calendar.RefreshToken`, `SetCalendar`). r0's "existing secret implementations only read secrets" is accurate, but the only precedent for account credentials in this repo is the database, unencrypted. Relevant to D1.
4. Automation/Goal `authority_ref` in projections contradicts the projection rule (B5).

## (d) Engineering input on D1–D4 (Austin decides)

- **D1 credential custody.** Both options fit behind `Resolve/Refresh/Replace(expected_version)/Revoke`. My recommendation differs from r0: encrypted credential records in the existing database (AES-GCM envelope; key from GCP KMS in production, per-record data key). Reasons: (1) the refresh CAS, authorization epoch and disconnect must be atomic with the connection record — Secret Manager versions cannot be added conditionally, so a DB pointer row is needed anyway, which makes the secret store a second system with a two-phase write; (2) works the same on Postgres and Firestore; (3) no per-secret cost or per-user secret sprawl. Cost: KMS key management, and DB backups hold ciphertext. r0's Secret Manager option is viable with a DB version pointer plus cleanup of orphan versions. Interim: a local-only encrypted file store under `NORI_LOCAL_ARTIFACT_DIR` with a generated key file (mode 600), refused unless `NORI_CLOUD=local`. No production backend is written.
- **D2 automation authority.** Mechanism: creating an automation materializes PermissionRules with `automation_id` and explicit tool/connection/resource/recipient lists and `max_operations`; occurrence tasks carry only that authority (no live user request). Config `NORI_AUTOMATION_UNATTENDED=require_rule|confirm_writes`, defaulting to `confirm_writes` until D2 is resolved — the safer one, which turns unattended writes into interactions that expire at the execution window.
- **D3 missed runs.** `missed_run` is required in `AutomationEditable`, so the API needs no default at all; the frontend shows the suggested value at creation. Scheduler behavior per value is deterministic; missed occurrences are recorded `expired`.
- **D4 disconnect retention.** Agree with r0. Mechanism: disconnect `value {kind: "disconnect", purge_cache: boolean, delete_imported: boolean}`; both false by default in the API; admission closed first regardless.
