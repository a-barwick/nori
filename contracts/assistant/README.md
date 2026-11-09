# Nori assistant contracts

Shared JSON Schema 2020-12 definitions and fixtures for the boundaries in [the contract baseline](../../docs/assistant-contract-baseline.md). Backend validates Go runtime values against these schemas, the web client generates its TypeScript types from them, and the integration stream keeps the fixtures and the checker current.

Current encoding: **r2**. r1 applied the agreed recommendations from the three r0 reviews (`docs/assistant-r0-review-*.md`): backend B1–B10, frontend F1–F11 and F13 with the preferences shape and readable rule summaries, integration I4–I6. r2 adds frontend F12 (Library mutations), F14 (memory, inbox), F15 (upload extensions), F16–F19 (plugin catalog, install and review, MCP server management, skill editing, computer session queue), F20 (Work detail result and events), the mutation routes and error codes below, and the scenario format. A revision is accepted when each affected consumer acknowledges its commit in [the coordination record](../../docs/assistant-coordination.md).

## Layout

| Path | Contents |
| --- | --- |
| `schemas/common.schema.json` | C1 primitives: IDs, revisions, instants, `Change`, `ActionRequest`, `MutationReceipt`, `APIError`, list envelope, the existing operation outcome vocabulary. |
| `schemas/tools.schema.json` | C2 `ToolDefinition`, `AvailableTool`. |
| `schemas/connections.schema.json` | C3 `Connection` projection, editable value, actions, resources. |
| `schemas/execution.schema.json` | C4 private `Invocation`, `PreparedAction`, `AdapterResult`; public `OperationProjection`. |
| `schemas/permissions.schema.json` | C5 `PermissionRule`, `Interaction` with closed per-kind payloads, `InteractionResponse`. |
| `schemas/automations.schema.json` | C7 `Trigger`, `Automation`, `Occurrence`, `Delivery`, `DeliveryDestination`, `Goal`. |
| `schemas/library.schema.json` | C8 `CardPart`, `LibraryItem`, `SearchHit`, `UploadAccept`. |
| `schemas/plugins.schema.json` | C6 `PackageManifest`, `Installation`, `MCPServer`, `ManagedProcess`, `ComputerSession`. |
| `schemas/domain.schema.json` | Shared calendar, mail and contact shapes with provider capability flags. |
| `fixtures/index.json` | Every fixture with its schema reference and expected validity. |
| `fixtures/<area>/*.json` | Example payloads. `*.valid.json` must validate; `*.invalid.json` must be rejected for the reason in its index note. |

Relative `$ref` values resolve against the `$id` host `contracts.nori.local`, which the checker maps to `schemas/`. Nothing is fetched over the network.

## Routes and errors (r2)

Core paths after the user-scoping wrapper; the browser reaches them as `/api/v1/...` through `assistantRoute` in `web/src/lib/runtime/forward.ts`.

| Operation | Request | Success |
| --- | --- | --- |
| Create in a family | `POST /v1/{family}` with `Change`, `expected_revision: 0` | `201` receipt |
| Update | `PUT /v1/{family}/{id}` with `Change` | `200` receipt |
| Command action | `POST /v1/{family}/{id}/actions` with `ActionRequest` | `200` applied or `202` queued receipt |
| Answer an interaction | `POST /v1/interactions/{id}/responses` with `InteractionResponse` | `200` receipt |
| Preferences | `PUT /v1/preferences` with `PreferencesChange` | `200` receipt |
| Automation preview | `POST /v1/automations/preview` with an `AutomationEditable` body, no request id | `200` `AutomationPreview` |
| OAuth callback | `GET /v1/connections/oauth/callback?state&code\|error` | `303` to the attempt's relative `return_to` |
| Receipt lookup | `GET /v1/mutations/{request_id}` | `200` receipt or `404` |

Errors use `APIError` with `code` from `ErrorCode`: `revision_conflict` and `interaction_stale` (`409`, with `current` holding the latest projection), `idempotency_conflict` (`409`, no `current`), `validation_failed` (`400`), `not_found` (`404`, also for another owner's id), `credential_store_unavailable` and `provider_unavailable` (`503`). A `503` may still carry `accepted: true` and the receipt.

## Scenarios

`scenarios/*.json` are ordered request/response sequences for the baseline's verification obligations. Each has `name`, `obligation`, optional `note` and `requires` (feature names from `/v1/capabilities.assistant_features`; `-live` skips the scenario until they are advertised), `users` (name to user id) and `steps`. A step has `name`, `user`, `method`, `path`, optional `body` and `body_schema`, `expect` and `capture`. `expect` holds `status` (allowed codes), `schema` for the response, `resource_schema` for a receipt's `resource`, `match` (JSON pointer to expected value), `absent` (pointers that must not exist), `count` (`"<pointer>|key=value"` to a number of matching array elements) and `same_as` (an earlier step whose response must be identical). `capture` maps a variable to a response pointer. Placeholders: `${uuid:name}` (one fresh UUID per name per run, so a retry reuses it), `${var:name}` (a captured value), `${user:name}`. A pointer segment `[key=value]` selects an array element. A step may `wait` by repeating until a pointer matches.

`histories/*.json` are Temporal histories retained from earlier builds. The checker replays each against the task workflow compiled into it.

## Check

```bash
cd core && go run ./cmd/contracts-check
```

```bash
cd core && go run ./cmd/contracts-check -live http://127.0.0.1:8083
```

`make check` runs the checker, regenerates `web/src/lib/contracts/fixtures.check.ts` (`-emit-ts`) and type-checks it.

`-strict` also fails when a `$defs` entry has no fixture. The checker is the first compatibility check; Go struct round-trips and generated TypeScript types are added by their owners against the same fixtures.

## Conventions carried from the baseline

- Request schemas (`Change`, `ActionRequest`, `*Editable`, responses) reject unknown fields, so an `owner_id` or `authority_ref` in a request is invalid. Resource projections tolerate additive fields.
- `OperationProjection` extends today's operation wire shape (embedded `state.OperationRecord` plus `owner`); `revision` there is the instruction revision.
- Account defaults live in `Preferences.account_defaults`; `Connection.defaults` is derived and read-only.
- `return_to` values are relative paths inside the web app.
- Projections never carry tokens, credential references, raw arguments or authority references; the schemas say so with `not` clauses and the invalid fixtures prove it.
- Existing names stay: operation outcomes, `SavedResult` fields, task command kinds, `instruction_revision`.
- `authority_ref` is server-issued. A request that supplies one is invalid.

## Changing a contract

Propose the change with the contract ID and the encoding revision, the consumer need, the schema diff, affected consumers and stored histories, compatibility consequences, and the fixtures that prove it. One editor lands the schema and fixture change in one commit; consumers acknowledge that commit in the coordination record. Chat alone does not amend a contract.
