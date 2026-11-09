# Nori assistant contracts

Shared JSON Schema 2020-12 definitions and fixtures for the boundaries in [the contract baseline](../../docs/assistant-contract-baseline.md). Backend validates Go runtime values against these schemas, the web client generates its TypeScript types from them, and the integration stream keeps the fixtures and the checker current.

Current encoding: **r1**. It applies the agreed recommendations from the three r0 reviews (`docs/assistant-r0-review-*.md`): backend B1–B10, frontend F1–F11 and F13 with the preferences shape and readable rule summaries, integration I4–I6. Queued for r2: frontend F12, the rest of F14, F15–F19, and the scenario fixtures and live mode (I1). A revision is accepted when each affected consumer acknowledges its commit in [the coordination record](../../docs/assistant-coordination.md).

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

## Check

```bash
cd core && go run ./cmd/contracts-check
```

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
