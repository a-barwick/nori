# Backend status (nori-backend session)

Branch `claude/nori-assistant-backend-9c520e`. Local stack: core 8081, db `nori_backend`, namespace `nori-backend`, queue `nori-tasks-backend`, users 1111…/2222… (env in the session scratchpad `env.sh`).

## Landed (commit → what)

| Commit | Content |
| --- | --- |
| 8de4158 | W1 Extension registry (`internal/extensions`), built-ins as `nori/builtin/*`, `CallRef.Exec` pinning, OperationRecord refs |
| 67e01b3, a56c217 | D-001, D-006 no-computer settling |
| 0e0c152 | W2 Connections + local vault, C5 permissions/interactions, W3 executor (`task/external.go`, `invoke`), Google Calendar/Gmail/Contacts, /v1 routes |
| 2264300, cdaf81e | D-002 404 for missing, D-003 account omitted |
| 805b534 | W9/W10 Scheduler, automations, occurrences, delivery, inbox, goals, first-party tools; D-005 questions as interactions |
| 95a498e | provider Transport hook (replay) |
| df326da | W12 Library/search/memory |
| a7a0b17 | D-007..D-010, W5 MCP client (go-sdk v1.8.0) |
| (next) | approved-but-unperformed → unmet |
| 938d665 | A32 side-chat start_task; automation Work titles |
| a6c0e65 | r2.3 MCP credential input, auth/credential projection |
| 2a6c7e5 | D-011 reconcile before asking; ICS preview/import (A19) |
| 8731723 | W4 plugins (catalog, upload, install, review, update/pin/rollback/uninstall) and skills |
| 7d55f49 | D-012 side-chat events stay out of the assistant inbox; D-013 evidence by reference |
| 23afdf0 | W13 computer sessions (A34) |
| f66d81f | D-014 answers before the question is live are retried |
| 155bba0 | A13 Nori MCP endpoint, client tokens; D-015 |
| 648ebbd | D-016 search side chats/relevance; D-017 forgotten memories |
| 2dad57b | A07 guest sandbox + plugin executor; uninstall removes installation |
| 0d12281 | D-018 reopen run scope after question/takeover; upload_read_text |
| 522d244 | D-019 cgroup delegation; W15 diagnostics, repair, feedback, continuation |
| 4e165e1 … 1c04267 | D-021..D-023, NORI_MCP_COMMANDS (local stdio MCP), continuation titles |
| 90eb176, f0b55e5 | D-024 sandbox requests carry run_id; guest refusals recorded failed; Stop ends sandboxed programs |
| 0b70977 | D-013 cited results survive the outer task-context trim; D-025 conditional requests |
| 04d3b48 | Plugin pre_invocation/result hooks with deadlines (text-stats 1.1.0 demo) |
| e5f9ced | Chained continuations titled by the root task |
| 9bd0438 | D-026 sandboxed plugin failures are cleared (contained / did_not_run), no blocking question |
| 5fca7f9 | A14 compose (programmatic composition, durable nested operations); operation provenance fields now persisted |
| 5d7bbd7, 625c7f3 | Plugin review: network/filesystem changes, diffed against approved_access; available_version recomputed |
| eb75ff1 | W15 trace ids (automation, occurrence, goal, package version, parent op) and feedback provenance |
| 52185ce, f6ba814 | A28 Web Push + SMTP delivery, verified enrollment, honest receipts; push_public_key in capabilities |
| 889042e, b765587 | Partially granted accounts usable for granted capabilities (selection and inventory) |
| 9acf840 | Microsoft (Outlook calendar, mail, contacts) via Graph |
| b765587, 46608a2 | App-password connect path; iCloud Calendar over CalDAV |

**Not yet sent to integration (peer messaging paused):** 9acf840, b765587, 46608a2, the HEY assessment (backend-providers.md), and the contract proposal: ConnectionAction gains write-only `username`/`password` for Integration.auth `app_password`. A wrong pair returns 400 validation_failed.

## Not done yet (backend backlog)

- Context/progress/lifecycle hooks (rejected as unsupported); skill_use agent tool; hosted managed stdio MCP (A10, local only).
- Provider verification blocked: Microsoft app registration, test Apple ID with app password, HEY OAuth client from 37signals (see backend-providers.md).
- W8 MCP-package connectors (Linear, GitHub, Slack, Notion, Drive, Dropbox, OneDrive) with capability matrices.
- A23/A30/A22 cross-app recipes on the recorded provider; push/email delivery adapters (A28).
- Latency metrics (A43).
- MCP: OAuth (go-sdk has OAuthHandler), resources/prompts read tools, elicitation into C5, managed stdio on the Computer runtime.
- Push/email live verification needs VAPID keys and an SMTP relay.
- Go DTO round-trip registry for contracts-check (integration asked for core/internal/api/contracts.go).
- Real Google OAuth round trip needs Austin to sign in (client accepts 127.0.0.1:3213 redirect per integration).

## Pending hand-off (peer messaging paused until Austin types in the backend session)

- **ack r2.7 ab5318e, r2.8 24816e2, r2.9 c0795ac.** Integration head 21b1428 is merged into the backend branch, and contracts-check passes at r2.9.
- **ad781f2 D-021:**
  - Cause: an answer given while the task was mid-step got ErrBusy ("task is not open"), which was treated as delivered.
  - Busy now leaves the answer for the delivery pass while the task runs.
  - Answers already marked delivered are redelivered when the task is still waiting on exactly that unresolved question, which heals 16eb64a8 on the next pass or deliver_answers.
  - The computer-resume path had the same flaw and is fixed too.
- **45431dd D-027:** an MCP definition's version is now `1-<12 hex content hash>`.
- **D-020: not a defect.** In 31ec25cf, browser_navigate returned `"headings": []` because the example.com variant the guest loaded has no h1–h3. The model's answer ("no main heading; title Example Domain") was correct. Verifying needs a page with real headings.
- **78cf85f (frontend .ics report):**
  - ReportSuccess no longer flips a needs_more_access account into a needs-attention state with a ready health code. That inconsistent state is what made the second inventory read say "Needs attention in Apps".
  - ICS duplicates now require an exact iCalUID match. The replay returned recordedevt1 for every lookup.
- **Still to relay:** 9acf840 (Microsoft), b765587 + 46608a2 (iCloud via app password), the HEY assessment, and the ConnectionAction username/password proposal. See backend-providers.md.
- **8915204 D-028..D-031:**
  - D-028 (approval in a script): the compose record is saved as confirmed with awaiting_interaction, and ToolRef turns it into needs_interaction, so the task asks. No invalid outcome is saved anymore.
  - D-030: divergence is checked for the whole step first. Calls that were not compared report skipped.
  - D-029: plugin review uses tool:<model_name>.
  - D-031: an address with push, or a subscription with email, gets 400.
- Integration's two observations are not fixed: a pending nested read still waits out its recovery window, and a graceful stop during a model request (recoverModelRef) still fails the task. Both are follow-ups.
- **6e14287 D-032:** approve or decline with no action_digest now returns 400 validation_failed. A different digest is still 409 interaction_stale.

## Turn: remaining list (coordinator message after 6e14287)

- **(1) HEY:** assessment is in backend-providers.md. Supported path: a 37signals-registered OAuth client and hey-sdk/go. Gap: no published third-party client registration.
- **(4) W15 diagnostics** were already built, in /v1/diagnostics and /v1/diagnostics/actions:
  - stuck_stop, stuck_task (including answered-but-undelivered), cleanup_pending, unresolved_write, missed_wake, delivery_failed, expired_subscription (52185ce), connection_attention, mcp_failed, plugin_problem.
  - Repairs go through the existing paths: resume_delivery, deliver_answers (fixed by ad781f2), run_scheduler, and reconnect/re_enroll via the normal connection and destination actions.
  - Unresolved writes are settled only by the task's question or by provider reconciliation, never by an operator shortcut.
- **(5) A12, 9d65da5:**
  - Servers with resources or prompts get read tools: resources_list (with templates), resource_read, prompts_list, prompt_get.
  - Elicitation during a call is answered cancel and returned to the task with a note.
  - Server diagnostics state what Nori doesn't support: sampling and roots aren't offered, and list-change and subscription notifications aren't received because sessions are per call; reconnect re-discovers.
  - Verified on DeepWiki: the tools were added, and a task ran two of them as reads.
- **(3) A30, 9d65da5:** monitors already had checkpoints and quiet no-change results. A finding identical to the last checkpoint is now quiet too (dedup). The morning briefing is the built-in skill, nori/morning-briefing, which reads live authorized sources.
- **A22** already exists in the connectors:
  - Source-backed contact candidates: Google contacts_search, plus outlook_contacts_search covering saved contacts and ranked people.
  - Provider-specific mail organization: Gmail labels and archive in mail_modify; Outlook folders, categories and flags in outlook_mail_organize.
- **(6) A38 server side:** audited, and no change needed.
  - Every document is under owners/<data owner>, including generic resources, mutation receipts, runs, history, chats and uploads. The only shared document is system/control.
  - Nori MCP client tokens route by the user ID inside them but verify against that user's own client records. The hash covers the whole token, so a token can't be replayed under another user.
  - The local credential vault is a per-user directory with validated names.
  - Plugin, MCP and compose calls resolve installations, servers and tools only from the caller's own inventory, so a forged installation or server ID is not found.
  - Library search and results are per owner.
- **ack r2.10 7a1e041.** Merged the integration head (with aee91bb) into the backend branch; contracts-check passes at r2.10. The Connection projection has no username or password fields; they exist only on the action and in the credential store.
- **bcdc61a (frontend follow-ups):**
  - The ICS preview note now says each event was checked against the live calendar by iCalUID. The old note's phrase 'not a live calendar' was misleading the model.
  - needs_attention accounts saved before 78cf85f with health ready count as usable, and as partial when scopes are missing.
  - GET /v1/library honours task_id.

## Turn: D-034..D-037, DTO registry, follow-ups

- **917ffb3, D-034..D-037:**
  - D-034: safe reads refused with 429 or 503 are retried up to twice, honouring Retry-After (capped at 20 s). Google and Graph mark those refusals temporary; writes are not retried.
  - D-035: new tasks use Preferences.budgets.task_usd, capped at 50 USD.
  - D-036: a continuation can operation_read the tasks it continues, and its request lists their operations.
  - D-037: cited results include the continued task's.
- **beb80b6, DTO registry:**
  - `api.ContractTypes` covers 46 references, with Changes registered inside their envelope. `api.RoundTrip(ref, data)` decodes strictly and re-encodes.
  - All 86 registered valid fixtures decode, re-encode and re-validate.
  - Structs gained the contract fields they used to drop. The remaining 31 references are handler-local request structs.
  - contracts-check (integration-owned) still needs to call `api.RoundTrip` for each valid fixture whose schema is in `api.ContractTypes`.
- **4a57a0a, follow-ups:**
  - A safe read left pending by a crash runs again immediately instead of waiting out its window.
  - A model request cut off by a worker stop is asked again up to twice instead of failing the task, with the cost recorded as unknown.
- **Delivery adapters:** fully built behind the destination channel interface (scheduler.Channel). They are WebPush (VAPID with RFC 8291) and SMTP (STARTTLS or 465), wired in platform from NORI_VAPID_* and NORI_SMTP_* / NORI_EMAIL_FROM, with enrollment, verification, receipts, retries, quiet hours and the expired-subscription diagnostic. Only credentials stand between them and live verification.
- **Rest of W8 (Slack, Teams, Drive, Dropbox, OneDrive):** not packaged. A package whose server can't connect would mislead.
  - Slack, Drive and Dropbox need MCP OAuth (authorization code with PKCE and a pre-registered client) plus an app Austin registers with each provider.
  - Teams and OneDrive need Agent 365 preview access and an Entra app.
  - The matrices are in backend-providers.md.
- **5140563, D-040..D-043 (frontend verification):**
  - D-040: a package-contributed MCP server records source {kind: installation, installation_id}. Servers created before this keep "user" until the package is reinstalled or updated.
  - D-041: side chats read every text attachment through `agent.ReadAttached` (CSV as rows; calendar, text and Markdown as text).
  - D-042: plain action summaries with readable dates, plain iCloud capability reasons, and request guidance so titles start with the action.
  - D-043: the import tool's description and the attachment note make calendar_ics_import the way to add .ics events. Each imported event is read back before it is reported.
  - None of these has been run end to end yet; frontend's re-check covers them.
- **bceacbd D-044:** MCP packages appear in /v1/integrations (source mcp) with capability matrices, auth (api_key or none), limits and their installation ID. GET /v1/integrations/{id} resolves them too.
- **3a62297 native Google Drive:** files.read (drive.readonly: drive_search, drive_read) and files.write (drive.file: drive_create, drive_update) on the Google connection, requested through request-access. Creates are keyed and reconcilable, updates run under the read version, and results are read back. No Drive recordings exist yet.

## Unfinished or fragile (backend, end of list)

- **Not verified with real accounts:** Microsoft, iCloud, Drive, push and email.
  - Credentials needed: Entra app, test Apple ID, the user granting Drive access, VAPID keys, an SMTP relay.
  - drive.readonly is a Google "restricted" scope. A production OAuth app needs Google's verification (possibly a security assessment); test users work in testing mode.
- **MCP OAuth is not built.** Slack and Dropbox are blocked on it plus registered apps; Teams and OneDrive also need Agent 365 access.
- **Compose:** a crash and divergence are verified. Approval inside a script was fixed (8915204) but not re-verified.
  - Nested calls in a step run concurrently and each takes its own run admission transaction. Heavy fan-out may hit transaction conflicts on the run document; that hasn't been load-tested.
- **Model-request retry after a worker stop (4a57a0a):** depends on worker versioning keeping in-flight workflows on their build. A mixed-build replay of a new Interrupted outcome on an old worker would diverge.
- **D-021 redelivery** uses the run's current question as proof the earlier delivery never landed. That's sound for one question at a time; not exercised with holds and subtasks together.
- **Elicitation** is answered cancel and passed to the task. A server that requires an accepted elicitation to proceed can't be completed in one call.
- **The DTO registry covers 46 of 77 schema refs.** The rest are handler-local structs, and contracts-check still needs to call api.RoundTrip.
- **Library fields** automation_id, conversation_id, goal_id, data, data_kind, delete_effect and preview_text are declared but not populated.
- **Plugin MCP servers created before 5140563** still show source "user" until reinstalled.
- **The ics tool and prompt changes (D-043)** steer the model; they don't enforce. calendar_event_create can still be chosen.
- **aa802b2, final items:**
  - D-045: side-chat tasks receive the chat's attached files (the chat upload's storage ID; no copy), so upload_read_text and the ICS tools work there.
    - I could not reproduce the "Google not connected" claim. It most likely followed from the failed upload read, or from a needs_attention record saved before the partial-access fixes (889042e, b765587, bcdc61a). Please re-check after merging.
  - D-042: summaries use the user's time zone and plain dates. Request guidance keeps upload IDs out of titles.
  - D-047: Library items get conversation, automation and goal IDs from the task's origin, plus preview_text and delete_effect. Proposal: remove data and data_kind from LibraryItem, since results don't store typed domain objects.
  - D-040: package MCP servers report their installation as source on read.
  - Verified locally: the pre-fix Linear server now names its installation, and Library items carry chat, preview and delete effect.
- **Numbering:** the side-chat upload fix in aa802b2 is D-048 (it was relayed as D-045).
- **d08ca3e:**
  - D-045: a compose call left pending by a crash reruns without waiting out its own recovery window.
  - D-049: paid nested calls reserve their cost against the task's remaining budget before dispatch; a call that would overrun is skipped with that reason.
  - New local-only NORI_DELIVERY_TEST_CA_FILE: a PEM root trusted by the push and SMTP transports. Startup refuses it unless NORI_CLOUD=local. It's documented in .env.example.
- **D-044** was fixed at bceacbd (MCP packages in /v1/integrations with capability matrices). It needs merging, not new work.
- **D-026, answering the open question:**
  - Fixed at 9bd0438. A sandboxed plugin failure is cleared as "contained" (deadline, memory limit, nonzero exit, lost response). A guest or policy-hook refusal is cleared as "did_not_run". A cleared failure doesn't block later calls or ask the user, but it's still reported in the result and the operation record.
  - Operations recorded before 9bd0438 aren't cleared after the fact. A task already waiting on such a question still needs that answer.
  - Host-side failures that may have changed something still ask, as before.
- **gofmt:** gofmt -l is clean for core/internal and core/cmd at d08ca3e.

## Turn: new list after the review

- **The trailing defects were already fixed:** D-048, the D-042 remainder, D-047 and D-040-on-read in aa802b2; D-045 and D-049 in d08ca3e.
- **(1) MCP OAuth, c6cbeec + c6c2458:**
  - Protected-resource discovery from the 401 challenge or well-known locations, then auth-server metadata.
  - Dynamic client registration, reused per server.
  - Authorization code with PKCE (S256) and the resource parameter. The shared callback route hands MCP attempts to the MCP service: single-use, 15 minutes.
  - Refresh with write-back, and RFC 7009 revocation on sign-out.
  - Verified locally against a loopback OAuth-protected MCP server: registration, authorize and callback, tools with the bearer token, refresh after expiry, revoke.
  - Linear 1.1.0 uses OAuth. Notion: the provider docs don't state dynamic registration. If it lacks it, sign-in reports ErrNoRegistration ("needs a registered app"); not verified.
- **(3) Repair and resume, 1ab9221:**
  - An account needing sign-in (revoked, expired, refresh failed, 401) raises a sign_in interaction on the task. The payload follows the contract: auth_attempt_id, authorize_url, connection_id, return_to /work/<task>.
  - The callback runs Connections.OnConnected, which resolves the interaction and delivers the answer, so the task resumes and retries.
  - Not verified end to end; it needs an OAuth callback (the replay stack).
  - App-password accounts can't fit SignInPayload (authorize_url is required). Proposal: allow an app_password variant with integration_id and connection_id and no authorize_url. Until then those still end the call with recovery repair.
- **(4) Hosted wake adapters already exist since 805b534:**
  - Cloud Tasks: named scheduled tasks per user and minute, with duplicates collapsed. SQS: delays of 15 minutes or less, in hops.
  - What's missing is a run against the Cloud Tasks emulator (CLOUD_TASKS_EMULATOR_HOST is already read). That belongs in integration's verification.
- **(5) Fast acknowledgement is already in place:** POST /v1/conversations/assistant/messages returns 202 in 18–83 ms locally, measured 3 times, before the outer model runs. The reply arrives on the event stream.
- **(6) Authority: not changed.**
  - Letting a user's direct request authorize a send without a prompt changes D2, which is Austin's decision. The current setting is NORI_USER_SENDS=ask.
  - "Sufficiently specific" also needs a mechanism: either the model cites the instruction, or a server-side match of recipients against the user's own words. Each has failure modes.
  - I'll implement whatever Austin decides. The setting is already a single switch.
- **(2) Sandbox egress allowlist: not started.**
  - It needs guest networking: a veth into the sandbox's net namespace and a filtering CONNECT proxy, or nftables with resolved host sets. Packages are refused when enforcement is unavailable.
  - It's a self-contained piece of work.

## Turn: Austin's provider accounts, frontend's repair shape, D2

### For integration: Microsoft personal account

The Entra app registration needs these settings.

**Supported account types**
- "Personal Microsoft accounts only", with `MICROSOFT_TENANT=consumers` (new in 09e07b6).
- Or "Accounts in any organizational directory and personal Microsoft accounts", with `MICROSOFT_TENANT=common` (the default).

**Platform and redirect**
- Platform: Web. The redirect URI must equal `NORI_OAUTH_REDIRECT_URL` exactly; Google uses the same callback.
- Locally that is `http://127.0.0.1:3211/api/v1/connections/oauth/callback`.
- Caveat: Entra's Web platform may only accept plain http for `localhost`. If it refuses the 127.0.0.1 address, register `http://localhost:3211/api/v1/connections/oauth/callback` and set `NORI_OAUTH_REDIRECT_URL` to that; Google's client then needs the same URI too.
- A client secret is required (confidential web client). Keys: `MICROSOFT_CLIENT_ID`, `MICROSOFT_CLIENT_SECRET`, `MICROSOFT_TENANT`.

**Delegated Microsoft Graph scopes Nori requests**

| When | Scopes |
| --- | --- |
| Always | `openid`, `email`, `offline_access`, `User.Read` |
| calendar.read and calendar.availability | `Calendars.Read` |
| calendar.write | `Calendars.ReadWrite` |
| mail.read | `Mail.Read` |
| mail.draft and mail.organize | `Mail.ReadWrite` |
| mail.send | `Mail.ReadWrite`, `Mail.Send` |
| contacts.read | `Contacts.Read`, `People.Read` |

A first connect asks for the read capabilities only. Write capabilities are requested with request-access.

**Personal-account behaviour**
- The account label ends "(personal)", decided by `GET /organization` failing for MSA accounts.
- Free/busy for other people (`getSchedule`) is refused up front for personal accounts, with that reason.
- Contacts search tolerates `/me/people` being unavailable.
- Revocation isn't offered by Microsoft. The user removes the grant at account.live.com/consent/Manage.

### Drive additional access in testing mode (8d34329)

- Requesting `files.read` adds `drive.readonly` (a restricted scope); `files.write` adds `drive.file`. The request uses `include_granted_scopes=true`.
- Testing mode lets listed test users grant restricted scopes; they see an unverified-app warning.
- If the user unticks the Drive scope, the callback stores the scopes actually granted, and the account becomes needs_more_access with calendar and mail still usable.
- If the user cancels outright, the account now stays exactly as it was. Before 8d34329, a partially granted account was marked needs-sign-in.
- Not run against the real account. It needs Austin's sign-in.

### Repair and resume per frontend's proposal (77bbf5d), building on 1ab9221

- SignInPayload is `auth: oauth` (auth_attempt_id, authorize_url, expires_at, requested_scopes) or `auth: app_password` (no URL), plus integration_id, connection_id and return_to `/work/{task}`.
- needs_more_access raises the same interaction, asking for the capability's scopes.
- Resolution is keyed on the connection: any sign-in or repair (from the task, Apps or an app password) resolves pending sign_in interactions with resolution `signed_in`, and the task retries.
- Decline ("Not now") resolves the interaction, and the task carries on without that account.
- This needs r2.11 for `auth`, `requested_scopes`, and the resolution kinds `signed_in` and `decline` on sign_in.
- Not verified end to end; it needs a recorded 401 mid-task, as frontend suggested.
- Frontend's `reply_progress` streaming of the outer model's first words isn't done. It needs streaming in the outer loop's model call.

### D2 (18d6e3b)

- `NORI_USER_SENDS` now defaults to `allow`: a direct request from the user authorizes the send it asks for.
- Reversible writes in user tasks were already authorized.
- Unattended automation runs keep `NORI_AUTOMATION_UNATTENDED=confirm_writes`. Deny rules still apply. Unknown-effect third-party tools still ask.

### Still mine

- Egress allowlist and hosted stdio isolation: not started.
- The open defects are all landed (aa802b2, d08ca3e).

## Turn: decided D1–D4, MCP OAuth finish, row store

- **42eecbd, D2/D3/D4 defaults:**
  - NORI_AUTOMATION_UNATTENDED defaults to require_rule.
  - Creation guidance: latest for reads and summaries; reminders get latest within a window that defaults to an hour; skip (expire) for writes and sends. missed_run stays required.
  - Disconnect purges the cache unless purge_cache is false (now a pointer, so an absent value counts as true). delete_imported_content stays false.
- **MCP OAuth is finished: c6cbeec, c6c2458, 01c2ef6.**
  - A tool call on a signed-out OAuth MCP server raises a task-bound sign_in interaction. The adapter interface is invoke.SignInChecker.
  - The MCP callback resolves it with signed_in, and the task resumes.
  - Verified end to end locally with a real task and a fake OAuth MCP server: the sign-in was raised, the callback resolved it, the task resumed, and after approval the echo tool returned "echo: hello nori".
- **01c2ef6, frontend copy defect:** package descriptions are plain sentences, and preconditions live only in the integration matrix. Apps no longer repeats them, and doc paths are gone.
- **ef40952, D1 row store:**
  - NORI_CREDENTIAL_STORE=database: envelope-encrypted rows under owners/<user>/r_credential_rows. AES-GCM per row with a fresh data key bound to the connection ID.
  - Data keys are wrapped by Cloud KMS (NORI_CREDENTIAL_KMS_KEY) or, locally only, by a key file (NORI_CREDENTIAL_KEY_FILE). Versions are resource revisions.
  - Verified locally with the key file: an owner-scoped row with no plaintext, decrypted on read. Cloud KMS isn't exercised.
  - The file vault remains NORI_CREDENTIAL_STORE=local.

### r2.11 proposal for integration (repair and resume, as built in 77bbf5d + 01c2ef6)

- **SignInPayload:** add `auth` (enum `oauth` | `app_password`, required) and `requested_scopes` (string array, optional).
  - Make `auth_attempt_id`, `authorize_url` and `expires_at` required only when auth = `oauth`.
  - For `app_password`, require only `integration_id`, `connection_id` and `return_to`.
  - For MCP servers, `integration_id` is `mcp` and `connection_id` is the MCP server ID.
- **Interaction resolution kinds for sign_in:**
  - `signed_in` is server-set, when the connection or MCP server becomes usable from any sign-in or repair.
  - `decline` is the user's "Not now".
- **InteractionResponse:** allow `{kind: "decline"}` for sign_in (no action_digest). `signed_in` isn't client-sendable.
- **Fixtures to cover:**
  - an oauth sign_in with requested_scopes
  - an app_password sign_in without a URL
  - a resolved signed_in
  - a declined sign_in

### Still mine

The egress allowlist and hosted stdio isolation, not started. The next turn starts there.

- **Correction:** D1–D4 are the coordinator's working defaults, not Austin's decisions. All of them stay configurable:
  - NORI_USER_SENDS (allow|ask)
  - NORI_AUTOMATION_UNATTENDED (require_rule|confirm_writes)
  - missed_run, chosen per definition, and execution_window_seconds
  - purge_cache and delete_imported_content, per disconnect
  - NORI_CREDENTIAL_STORE (database|local)
  Commit messages 18d6e3b and 42eecbd say "Austin's decision"; read that as the coordinator's working default.
- **Personal identifiers:** no account emails in commands, tool inputs or written files. I removed the one occurrence from this note.

## Turn: reversal of D1–D4, D-051, D-052

- **Reverted per the coordinator's reversal**, with git revert, so history isn't rewritten:
  - 0e3dd34 removes the D1 row store. The local file vault stays the development store, and there's no production store.
  - 1dac231 restores the earlier defaults: NORI_AUTOMATION_UNATTENDED=confirm_writes; missed_run required with no creation default and no reminder window default; purge_cache and delete_imported_content both false.
  - NORI_USER_SENDS=allow (18d6e3b) stays.
- **8063c59, D-051:** a source that can't be read is named as unavailable in the answer and isn't listed as unmet, unless the user asked for that source specifically. So one refused iCloud calendar no longer ends an answered agenda as failed (A43).
- **D-052 is not an adapter bug.** The recorded Book club is 18:30 America/Los_Angeles, which is 02:30 the next day in the user's Europe/London zone.
  - Task 2e0fd9dd's "events outside the requested range" comes from the replay returning the same events for every range.
  - The recording, or the test user's time zone, should match the scenario.
- **r2.11 proposal:** the full text is in the previous section of this file. My messages to integration are still blocked by the pause; please relay it.
- **Next:** the egress allowlist and hosted stdio isolation.
- **Final state after the reversals** (the coordinator's latest message wins):
  - The interim defaults are restored in 1dac231:
    - NORI_AUTOMATION_UNATTENDED=confirm_writes, with require_rule still available.
    - purge_cache and delete_imported_content are both false and remain explicit flags.
    - missed_run is required with no preselection and no reminder window default.
  - d6ed945 reapplies the D1 row store as opt-in only (NORI_CREDENTIAL_STORE=database). The file vault stays the local default, and no production store is selected.
  - d6ed945 also rewords the automation tool: missed_run has no default (ask the user), and what to weigh per kind is text only.
  - NORI_USER_SENDS=allow stays.
  - For integration: merge 1dac231, 8063c59 and d6ed945 on top of c46a1f7. Skip 0e3dd34; d6ed945 undoes it.
