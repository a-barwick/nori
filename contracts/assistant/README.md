# Nori assistant contracts

Shared JSON Schema 2020-12 definitions and fixtures for the boundaries in [the contract baseline](../../docs/assistant-contract-baseline.md). Backend validates Go runtime values against these schemas, the web client generates its TypeScript types from them, and the integration stream keeps the fixtures and the checker current.

Current encoding: **r2.12**. r2.12 adds `Capabilities`, the full `GET /v1/capabilities` payload: the existing chat fields (`contract`, `conversationContract`, `service`, `conversationModes`, `features`) plus the additive `AssistantCapabilities` fields. `AssistantCapabilities` itself is unchanged, so earlier fixtures stay valid. r2.11 gives `SignInPayload` a required `auth` (`oauth` or `app_password`): an OAuth sign-in requires its attempt id, authorize URL and expiry, an app-password sign-in carries only the integration, the connection and where to return; for an MCP server `integration_id` is `mcp` and `connection_id` is the server id. A sign-in resolves `signed_in` (set by the server when the connection or server becomes usable) or `decline` (the user's Not now, sent as `{kind: "decline"}` without an action digest); `signed_in` is never client-sendable. r2.10 lets `connect` and `repair` carry a write-only `username` and `password` (together) for integrations whose `auth` is `app_password`, such as iCloud; the pair is verified with the provider, stored as the credential and never returned or kept in receipts, and a rejected pair answers 400 `validation_failed`. r2.9 adds delivery-destination enrollment (A28): `DeliveryDestinationCreate` (email with `address`, or push with the browser's `PushSubscription`; both write-only and never projected), `DeliveryDestinationAction` (`send_verification`, `verify` with a six-digit code, `remove`), the `subscription_expired` error code (409), and `AssistantCapabilities.push_public_key`, present only when push is configured. Routes: `POST /v1/delivery-destinations`, `PUT /v1/delivery-destinations/{id}`, `POST /v1/delivery-destinations/{id}/actions`. r2.8 lets approved package access name `network` hosts and `filesystem` paths, matching what a package requests. r2.7 r2.7 adds `contained` to `OperationProjection.cleared` for sandboxed package calls that failed inside the sandbox. r2.6 r2.6 adds `WorkItem.origin.continues_task_id` for tasks started by the continue action. r2.5 r2.5 adds external MCP client tokens for the Nori MCP endpoint (A13): `MCPClient` (never the token or its hash), create-only `MCPClientChange`, `MCPClientCreated` (the token appears once, in the first response), and `MCPClientAction` revoke. Routes: `GET`/`POST /v1/mcp-clients`, `POST /v1/mcp-clients/{id}/actions`; the endpoint is `POST /mcp` on core with a bearer token. r2.4 r2.4 adds `SkillView` (the skills list item: `Skill` plus `id`, `revision`, `enabled`, and `installation_id` for read-only plugin skills) and the `skills` feature flag. r2.3 r2.3 adds the write-only `MCPCredentialInput` for `POST /v1/mcp/{id}/credential`, `MCPServer.auth` and `observed.credential` (`missing` or `present`, never the value). r2.2 r2.2 makes `Connection.account` optional: it is absent until the first successful sign-in, never an empty placeholder. r2.1 adds `LibraryItem.revision` and `InboxItem.revision` (resource revisions for `expected_revision`, distinct from content versions and from `state.Result.revision`, which is an instruction revision) and gives `CatalogEntry.requested_access` the full manifest shape with `network` and `filesystem`. r1 applied the agreed recommendations from the three r0 reviews (`docs/assistant-r0-review-*.md`): backend B1–B10, frontend F1–F11 and F13 with the preferences shape and readable rule summaries, integration I4–I6. r2 adds frontend F12 (Library mutations), F14 (memory, inbox), F15 (upload extensions), F16–F19 (plugin catalog, install and review, MCP server management, skill editing, computer session queue), F20 (Work detail result and events), the mutation routes and error codes below, and the scenario format. A revision is accepted when each affected consumer acknowledges its commit in [the coordination record](../../docs/assistant-coordination.md).

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

Errors use `APIError` with `code` from `ErrorCode`: `revision_conflict` and `interaction_stale` (`409`, with `current` holding the latest projection), `idempotency_conflict` (`409`, no `current`), `subscription_expired` (`409`, a push subscription the provider no longer accepts; enroll again), `validation_failed` (`400`, also a wrong or expired verification code), `not_found` (`404`, also for another owner's id), `credential_store_unavailable` and `provider_unavailable` (`503`). A `503` may still carry `accepted: true` and the receipt.

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

`recordings/<provider>/*.json` are hand-written provider exchanges for the local replay transport (`NORI_PROVIDER_REPLAY=<repo>/contracts/assistant/recordings`, local stack only). An exchange matches on method, host, path (exact or prefix `*`), query, `body_contains` (all must appear) and optional `basic_auth` (made-up credentials, never real ones), `query_prefix` and `headers` (each value must start with the given string, for example a `timeMin` date or a recorded bearer token); `after`, `times`, `delay_ms` and `drop` order and shape replies. `capture: NAME` saves the matched request so a later reply can echo it with `{{capture:NAME.path}}`, `.stem`, `.body` or `.body_xml`, for a read-back that returns exactly what was written. `recordings/samples/` holds files to upload in recorded workflows.

## Core route manifest

[`routes.json`](routes.json) is generated from Core's route registrations,
including optional route families. It declares caller types, client scope,
request limits in bytes, path parameters, and response media types. Scoped
routes live under `/users/{id}` inside Core. A registration needs a rule to
compile, and Core rejects an empty rule when constructing the router.
Run `go run ./cmd/contracts-check -emit-routes` from `core/` after editing
a rule. The normal contract check fails if the checked-in manifest is stale.

The first manifest leaves the existing web forwarding behavior in place.
Its differences from the prior `forward.ts` policy are recorded here for
the proxy migration.

| Previous browser proxy | Core's declared policy |
| --- | --- |
| The management allowlist accepted any listed family and up to four more segments. | The manifest specifies exact methods and patterns. Feature-disabled routes still return 404. Tool IDs retain a trailing multi-segment parameter. |
| Conversation IDs used a loose 36-character match. Operations were unavailable. | IDs are canonical UUIDs or `assistant`. Only independent conversations support DELETE. Conversation operations can use the same signed-in proxy as the other conversation reads. |
| Legacy `/api/session`, diagnostics and feedback had no browser routes. | These remain available to local tooling. Health, user resolution, and queue processing are separate from the browser forwarding surface. |
| JSON writes used 64,000 characters locally and up to 128,000 in hosted or management routes. | Core already decodes at most 64 KiB. Use that byte limit consistently for JSON. Uploads retain their 10 MiB limit. The MCP transport uses a 1 MiB limit when enforcement lands. |
| General reads allowed JSON and SSE. Operations allowed JSON and PNG in hosted mode and any type locally. | Only event routes accept SSE. The artifact operation accepts PNG. Other runtime operations return JSON. |
| File responses used family-level filters. Calendar and Markdown uploads could be saved but not downloaded through all browser routes. | File routes accept the supported attachment media types and octet-stream. Metadata routes return JSON. Download and sandbox headers are relayed. |
| OAuth callback used a dedicated redirect handler. | The manifest declares JSON and HTML for the callback. The web proxy still checks navigation and constrains the redirect target. |

## Client credentials

Connected Clients in Settings issues credentials for MCP and API clients and
can revoke or rotate them. The browser generates the secret and sends its
SHA-256 verifier over the API. The issuance response contains only the owner
and client IDs and public metadata. Credentials created with the original
`nori_` format continue to work. New credentials use `nori2_`; treat the
entire token as opaque. Scopes are `read` and `mutate`. Last use is recorded
at most once a minute per client.

The CLI starts a ten-minute approval request at `POST /v1/client-authorizations`.
It generates the credential secret and a separate polling proof and sends
only their verifiers in that request. Open
`/settings/clients?authorize=<challenge id>` in the web app, confirm the
code printed by the CLI and choose scopes. The CLI sends the proof in a
JSON body to `POST /v1/client-authorizations/status` at the returned interval.
After approval it gets the owner and client IDs, then constructs its
credential locally. Repeating the poll returns the same IDs. CLI credentials
issued through this flow expire after 90 days. Keep credentials and verifiers
out of URLs, logs and mutation receipts.
