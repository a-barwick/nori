# r0 review: integration

Reviewer: integration and verification session (Opus 5.5). Object: r0 docs `2a5d5bd` and the r0-draft encoding in `contracts/assistant` at `ed6b020`. Question asked of every shape: can the twelve verification obligations in the baseline be exercised end to end with it, and where would Go and TypeScript drift without anyone noticing. Evidence is from the code at `ed6b020`.

## (a) Accepted as-is

- C1 conventions, `Change`/`ActionRequest`, receipts deduplicated by owner and request id, status codes, `GET /v1/mutations/{request_id}`, polling instead of a management event bus.
- C3 lifecycle semantics, C5 permission semantics and closed interaction payloads, C7 occurrence identity and generation fencing, C8 card parts by reference.
- The checker approach: one Go tool, schemas resolved locally, `index.json` declaring expected validity. It runs in under a second.
- Backend review `8c22c80` B1, B2, B4, B5, B6, B8, B9, B10 from the compatibility side. B3 (no workflow version gate) is acceptable **if** the replay evidence below passes; I will produce it.

## (b) Corrections and expansions needed

### I1. Scenario fixtures and a live mode in `contracts-check` (C1, all twelve obligations)

- Need: the twelve obligations are sequences (retry X returns the original receipt, Y expecting 4 conflicts, stale approval rejected). Single-document fixtures cannot express them, so today nothing checks them, and each stream would test them differently or not at all.
- Change: `contracts/assistant/scenarios/*.json`, each a list of steps `{user, method, path, body, expect: {status, schema, match}}` with `$captures` for ids returned by earlier steps. `contracts-check` validates every step body and expected response against the named schema offline (always), and with `-live http://127.0.0.1:8083` runs them against a core, addressing users through the existing `/users/{id}/…` prefix so two-user isolation is tested on the same path production uses. No second tool.
- First scenarios: connection update (revision 4 → 5, retry X, conflicting Y, X reused with a changed account), duplicate request id across two different targets, owner forgery (user B reads, updates and responds to user A's connection, interaction and automation ids → 404 each), stale approval (correction after interaction issued → approve 409 with the current interaction in the body), lost-wake acceptance (`503 accepted:true` then identical retry, same task id).
- Affected: none stored. Consumers: backend endpoints must pass them live; frontend gets real request/response pairs for fixtures.

### I2. Go round-trip is strict, and lives in `contracts-check` (C1–C8)

- Need: Go `encoding/json` drift that schema review does not catch: nil slices encode as `null` where the schema requires arrays (`granted_scopes`, `task_ids`, `items`); `omitempty` drops required `0`/`false` (`revision: 0`, `met: false`); missing struct fields silently drop fixture fields.
- Change: backend keeps a small registry in Go (`map[schemaRef]func() any`) beside the DTOs; `contracts-check` imports it and, for each valid fixture whose schema is registered, decodes with `DisallowUnknownFields`, re-encodes, re-validates, and fails on any field lost in the round trip. Invalid request fixtures are fed to the same request validator the HTTP handlers use and must be rejected. An unregistered schema is a warning, then a failure under `-strict` once the DTO exists. No `_test.go` files; the check is the command.

### I3. TypeScript drift check (C1)

- Need: generated types must not lag the schemas, and the generated types must accept the fixtures.
- Change: frontend owns the generator choice and its committed output under `web/src/lib/contracts/`. `contracts-check -emit-ts` writes `web/src/lib/contracts/fixtures.check.ts`: one `const` per valid fixture, the fixture inlined as a literal and annotated with its generated type, so `tsc` (already in `make check`) catches drift with literal types intact. A JSON import would widen enums to `string` and accept nothing. `make check` regenerates both and fails on `git diff`.

### I4. Missing invalid fixtures that matter (C1, C3, C5, C6, C7)

Owner forgery in every editable value (`owner_id` / `user_id` inside `ConnectionChange.value`, `AutomationChange.value`, `PermissionRuleChange.value`, `InteractionResponse.value`); `authority_ref` inside an `InteractionResponse`; an approval response with an empty digest; secret leaks in `MCPServer` (literal `env` value), `Installation.configuration` (`api_key`), `SignInPayload` (`code`, `access_token`), `DeliveryDestination` (`device_token`, `webhook_secret`); an `Automation` projection carrying `authority_ref` (backend B5). I add these when r1 lands.

### I5. `OperationProjection` must extend the existing operation wire shape, not rename it (C4)

- Evidence: `GET /v1/conversations/{id}/operations` and every chat message already return `operationView` = embedded `state.OperationRecord` (`id, call_id, name, effect_class, outcome, result_ref, revision, attempt, artifact_ids, action{digest,text,spec}, cleared`) plus `owner{type, id}` and `result` (`core/internal/api/conversation_contract.go:33`). `revision` there is the instruction revision. The chat client's `Operation` type (`web/src/lib/chat/contract.ts:7`) reads that shape. r0's projection instead requires `task_id` and `instruction_revision`, which no existing response contains.
- Change: `OperationProjection` keeps the existing names (`revision`, `owner`, `attempt`, `artifact_ids`, `action`, `result_ref`) and adds only new optional fields (`tool`, `connection_id`, `account_summary`, `authority_source`, `provider_refs`, `parent_operation_id`, `cost`, `error`). Drop required `task_id` / `instruction_revision`. Fixture: today's operation response captured from my local stack validates unchanged.
- Affected: the chat client and the conversation operations route. Compatible if done this way; a breaking rename otherwise.

### I6. Stale approval relies on the interaction revision; make that explicit (C5)

- `InteractionResponse` carries `expected_revision` (the interaction's revision) and `action_digest`, not the task's instruction revision. That is sufficient only if a task correction supersedes the interaction (status `superseded`, revision +1) in the same transaction that advances the instruction revision. Encode the rule in the schema description and prove it with the stale-approval scenario (I1). No shape change.

### I7. Receipts should name their resource schema (C1)

- `MutationReceipt.resource` is untyped JSON, so a receipt fixture proves nothing about the resource. Scenarios name the expected resource schema per step (`expect.schema: "connections.schema.json#/$defs/Connection"` applied to `resource`). No shape change; the runner does it.

## (c) Things in r0 I believe are wrong

1. `OperationProjection` field names (I5) break the existing operation wire shape.
2. `Automation.authority_ref` in a projection (agrees with backend B5).
3. The proxy note is accurate and wider than stated: `web/src/lib/runtime/forward.ts` `conversationRoute` allows only `events` and `messages` reads, so the operations read route and every new `/v1/...` family need explicit proxy entries. Each route family in the scenarios runs through the proxy as well as against core directly, otherwise a working core endpoint can be unreachable from the UI.
4. Effect vocabulary: the Go validator (`state/evidence.go:111`) accepts all five effects, but no built-in tool emits `reversible_write` or `sensitive_send` today (only `read`, `control`, `unknown`). Every permission and approval path for writes is new code; nothing existing exercises it.

## (d) D1–D4 as they affect verification (Austin decides)

- **D1.** Either custody choice is verifiable locally behind `Replace(expected_version)`. Backend recommends encrypted records in the database, which differs from r0's secret-store recommendation; that is a disagreement for the coordinator to surface. From the verification side, the database option has fewer failure modes to prove (refresh CAS, epoch advance and disconnect commit in one transaction; the secret-store option adds orphaned versions and a two-phase write to exercise). The real Google sign-in is blocked on Austin at the browser either way, and on the Google OAuth client having a redirect URI registered for the local web origin (`http://localhost:3213/...`), which only the Google console shows.
- **D2.** With backend's interim `confirm_writes`, unattended writes in A25, A30 and A44 can only be verified as interactions that expire at the execution window. Rows will say "verified under interim D2 mechanism" until Austin resolves it.
- **D3.** `missed_run` is required per definition, so all three values are verified, not a default. The scheduler race and missed-slot cases run for each.
- **D4.** Disconnect is verified with both flags off (admission closed, saved results retained as copies) and each flag on. Rows record which.
