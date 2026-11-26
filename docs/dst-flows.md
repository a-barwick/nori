# Nori Functional E2E Flows & DST Invariants Specification

This document enumerates every end-to-end user and system flow in Nori, defines the safety and liveness invariants each flow must satisfy under adversarial faults, and maps deterministic simulation testing (`core/internal/sim`) coverage.

Every flow below is driven through real production code under seeded fault injection, virtual clocks, and deterministic replay.

---

## Coverage Summary

| Flow | Domain | Current DST Status | Open Bugs Covered |
| --- | --- | --- | --- |
| F1 | Identity, Auth & Multi-Tenant Isolation | Covered (Stages C–D: multi-tenant HTTP, auth/observability middleware, redaction, web proxy) | #17, #20, #22 (resolved) |
| F2 | Outer Conversation Lifecycle & Compaction | Covered (Stage B + Coupled Memory Wave 2: keyed outer store faults, `flush_and_compact`, AutoDream, concurrent replica merge) | #27, #33, #42, #64, #65 (resolved) |
| F3 | Prompt-Injection Guard & Outer Command Dispatch | Covered (Stage B + Wave 2: `hold`, injection blocking, `OuterInputBytes` 128 KiB command-plan trimming, mixed `dispatch`/`steer` + `remember`/`forget` vetting, `web_search` framing) | #16, #23, #27 (resolved) |
| F4 | Real Background Task Execution (Postgres-Native `task.Runner`) | Covered (Stage A: real `task.Runner` & `worker.Pool` over `sim/memstore` leases, `sim/inner`, and `FullWorld`) | #24, #26, #33, #42 (resolved) |
| F5 | Subtasks & Shared Family Budgets | Covered (Stage A: family budgets, multi-tool batches, `compose` subtask budgets, tariffs) | #25, #33 (resolved) |
| F6 | Durable Tools, Recovery & Script Composition (`compose`) | Covered (Stage A + Wave 2: `compose`, built-in runtime & memory recall tools `memory_read`/`memory_search`/`memory_get`/`conversation_find`/`conversation_read`/`operation_read`/`task_history_read`, >128 KiB `fitContext`->`projectContext`, recovery, cleared-write completion) | #24, #25, #26 (resolved) |
| F7 | Permissions, Rules & Approval / Interaction Cards | Covered (Stages C–D: scoped allows, case-insensitive denies, atomic decline, >200 pagination, UI) | #15, #30, #41 (resolved) |
| F8 | MCP Servers, Credentials, OAuth & Outbound Egress | Covered (Stage C: cred revocation, netguard, OAuth replay/redirect, bounds, rowstore) | #11, #12, #13, #18, #19, #22, #28, #32 (resolved) |
| F9 | Automations, Goals, Schedules & Inbox Delivery | Covered (Stage C: empty-tz fallback, DST, RRULEs, missed-run skip, >100 drain, monitor framing) | #16, #29 (resolved) |
| F10 | Uploads, Library, Results & Search | Covered (Stage C + Wave 2: download headers, screenshots, `all_versions`, Unicode search, `/v1/search` `"memory"` hits with `ContextFrom` & tenant isolation) | #14, #28, #31, #66 (resolved) |
| F11 | Operator Pause, Recovery, Wake Delivery & Graceful Shutdown | Covered (Stage D + Wave 2: `cmd/nori` HTTP & `worker.Pool` graceful drain, `nori_task_leases` / `nori_user_wakes` `LISTEN/NOTIFY` wakes, delayed `NextConversationWake` fast idle flush `+2m` / session-idle extract `+5m` / AutoDream `+15m` wakes, `localfs`, `StopPaused`, Postgres conformance) | #34, #35, #36, #37, #38, #39, #40, #64 (resolved) |
| F12 | Edge Surfaces: Embed Adapter & Web Workbench | Covered (Stage D + Wave 2: real Node embed adapter E2E DST, web proxy/UI fixes, unified 3-tab `/memory` screen, contract scenarios) | #13, #20, #21, #41, #43, #44, #66 (resolved) |
| F13 | Coupled 3-Tier Memory, Pre-Compaction Flush, AutoDream & FTS | Covered (Wave 2: `INV-MEM-01..08`, `memstore` ↔ Postgres 18 `MemoryIndex` conformance, Scenarios 1–8, `FullWorld` memory/burst/idle-wake fuzzing) | #64, #65, #66 (resolved) |

---

## F1 — Identity, Auth & Multi-Tenant Isolation

### Flow
1. Request arrives at the API (scoped under `/users/{id}/...` with HS256 service auth, or local loopback email resolution).
2. Request executes across conversations, tasks, operations, interactions, rules, MCP servers, credentials, automations, goals, library items, uploads, search, and SSE streams.
3. Multiple users interleave operations concurrently against the shared Postgres storage backend and lease/wake store.

### Safety Invariants
- **No cross-tenant read or write:** User A can never observe, mutate, cancel, approve, decline, download, or search any record, artifact, credential, task run, lease, or event belonging to User B.
- **Tenant isolation.** Runs, commands, messages, interactions, automations, occurrences, and artifacts stay within their owner's storage scope. Credential encryption and application-level artifact encryption authenticate the owner with additional authenticated data (AAD).
- **Strict auth before telemetry/work:** Unauthenticated or malformed HS256 requests are rejected with 401 before triggering trace sampling, trace flushing, or storage reads.
- **Hosted route observability:** Multi-user `/users/{id}/...` paths resolve to canonical route metrics/spans without leaking user IDs or secrets.
- **Strict JSON & size enforcement:** Unknown JSON fields, trailing payloads, null change values, and oversized bodies are rejected with deterministic 400/413 errors.

### Liveness / Functional Invariants
- Valid authenticated requests for every user succeed and remain isolated under concurrent multi-user load.
- Redaction removes every configured and dynamically loaded secret before telemetry or error export, even across truncation boundaries.

### Adversarial Faults
- Replayed/expired/wrong-audience/wrong-alg HS256 tokens, unauthenticated `traceparent: ...-01` floods, cross-user resource IDs, path traversal (`%2e%2e`, `.`, `..`, `/users//...`), malformed JSON/null values, oversized bodies.

---

## F2 — Outer Conversation Lifecycle & Compaction

### Flow
1. Optional first-time introduction turn.
2. User submits messages (with optional uploads, retries with identical ID, conflicting duplicate IDs, mid-turn follow-ups, and cancellations).
3. Competing API workers race to lease the turn, renew or lose leases, stream replies, compact history (extractive + idle semantic refinement), and publish assistant messages.
4. Client reads snapshot and subscribes/resumes/resets via SSE events.

### Safety Invariants
- **Monotonic counters & fencing:** `Received`, `Consumed`, `UserReceived`, `UserRouted`, `Fence`, `EventPosition`, and `ReadMark` never decrease; `Consumed <= Received`, `UserRouted <= UserReceived <= Received`.
- **Single publication & immutability:** Each turn produces at most one assistant message, never while still active, and published messages never mutate or disappear.
- **Stale worker rejection:** A worker whose lease expired or was stolen cannot commit messages, memory changes, or task commands.
- **Restart freeze bound:** Mid-turn restarts never exceed `MaxTurnRestarts`; subsequent inputs queue for the next turn.
- **Event log continuity:** Event sequence numbers are contiguous (`1..EventPosition`) with zero gaps or duplicates.
- **Compaction consistency:** Extractive summary always commits even if semantic refinement fails; `RefinementThrough <= Through`; `len(Items) <= MaxSummaryItems`; concurrent replicas never silently drop a refinement batch.

### Liveness / Functional Invariants
- Once faults clear and workers drain, every accepted user message is stored, consumed, and routed (`Consumed == Received`, `Active == ""`, `UserRoutingPending == false`).
- Interrupted/errored turns preserve `UserRoutingPending == true` and cleanly replay on the next user input.
- Compaction failure backoff pauses retries for the backoff window, then recovers and produces a refined summary once healthy.
- **Under store faults:** All safety and liveness invariants still hold when memstore injects `Get`/`Query`/`Apply`/`TxConflict`/`CommitThenError` faults during `Runner.Process`.

### Adversarial Faults
- Store errors/conflicts/commit-then-error during active turns, competing worker lease takeovers, mid-flight cancellations, mid-flight user inputs, provider errors, context overflows, stream disconnects, concurrent compaction replicas, summary model failures.

---

## F3 — Prompt-Injection Defense & Outer Command Dispatch

### Flow
1. A turn contains both fresh user input and untrusted background task output (task completion, question, or work request).
2. Outer model proposes actions: `dispatch` (top-level or subtask), `steer`, `answer`, `stop`, `hold`, `remember`, `forget`, `show_result`.
3. Command verification (`planUserCommands`) evaluates user-authored commands against a view free of untrusted task output.
4. Accepted commands are written atomically to the command log and relayed to the task engine.

### Safety Invariants
- **No untrusted command smuggling:** Untrusted task output cannot cause unauthorized top-level `dispatch`, `steer`, `remember` (system-prompt persistence), `forget`, `answer`, `stop`, or subtask `dispatch` — neither standalone nor piggybacked alongside a legitimate user command.
- **Schema-to-decoder parity:** Every tool/action offered in the outer model's structured JSON schema (including `hold`) is validly decoded and executed without turn errors.
- **Command receipt idempotency:** Retrying an accepted command batch returns identical receipts and never creates duplicate runs or double-advances revision/position.

### Liveness / Functional Invariants
- Legitimate user `dispatch`, `steer`, `answer`, `stop`, and `hold` commands succeed and take effect — including placing a `hold` on a running or finishing task.
- Even when conversation history + active/recent tasks exceed 40 KiB, `commandPlanMessages` trims oldest context cleanly instead of permanently failing user `dispatch`/`steer`.

### Adversarial Faults
- Prompt-injection payloads inside task results/questions proposing `remember`, `forget`, `stop`, `answer`, `dispatch`, or `steer`; piggybacked extra calls alongside a valid `dispatch`; conversations exceeding 40 KiB of history + tasks; duplicate command retries under `CommitThenError`.

---

## F4 — Real Background Task Execution (Postgres-Native `task.Runner`)

### Flow
1. A task command creates a `nori_task_leases` row and runs a real `task.Runner` execution (driven by `worker.Pool` in `cmd/nori` and by `core/internal/sim/inner` / `FullWorld` in DSTs), wired to real `task.Activities`, `agent.Service`, `task.Executor`, `permissions.Service`, and `sim/memstore` (`docstore.Backend`, `docstore.LeaseStore`, and `docstore.WakeListener`).
2. `task.Runner` claims a fenced lease in `TaskControl` and `nori_task_leases`, renews it periodically, and loops through `SyncHistory` -> budget check -> `Ask` (if question) -> `ModelRef` -> `ToolRef` / `CheckCompletion` -> `PublishRef` -> `Cleanup`, persisting its cursor in `TaskControl` at every checkpoint so any worker replica can resume cleanly.
3. External wakes arrive mid-run via Postgres `LISTEN/NOTIFY` (`nori_wake`) and `nori_task_leases` updates: steer, stop, answer, approval response, routing-resolved, global pause/resume, subtask completion, and graceful drain.

### Safety Invariants
- **Deterministic multi-worker recovery & handoff:** Every simulated `task.Runner` run resumes bit-for-bit deterministically across multi-turn worker drain handoffs, crash takeovers, and fenced `TaskControl` lease transitions without duplicating model or tool side effects.
- **Revision fencing:** A steer mid-turn bumps revision, skips remaining stale calls in the batch, and never publishes under a stale revision.
- **Accurate transcript on boundaries:** Superseded or paused tool boundaries never record misleading `"uncertain"` outcomes for tools that never ran.
- **Finishing barrier:** No parent publishes a terminal outcome until all owned subtasks have ended and cleanup has succeeded; cleanup retries never replay model or tool calls.
- **Held publication:** Terminal task outcomes wait while `UserRoutingPending == true` and release once routing clears.
- **Overflow retry viability:** When a model returns context overflow on a continued task with large prior-task background (up to 64 KiB), the reduced-budget retry still constructs a valid non-empty request and progresses instead of looping/failing unconditionally.
- **Disabled-tasks safety:** When background tasks are disabled, task-related API routes return clean errors rather than nil-pointer panics.

### Liveness / Functional Invariants
- Every started task reaches a valid terminal state (`completed`, `failed`, or `cancelled`) with `Finishing == nil`, `CleanupPending == false`, `CleanupError == ""`, `FinishedAt != nil`, and its `nori_task_leases` row deleted after faults clear.
- Work tasks continued from prior runs (`continueWork`) return valid work receipts even on idempotent duplicate retries.

### Adversarial Faults
- Activity transient errors and retries, mid-batch steer/stop/pause, worker crashes and lease-renewal failures requiring `worker.Pool.RepairOnce` / lease-expiry takeover, stale-fence writes, wake-sequence races, cleanup failures, routing holds, >32-turn worker drain handoffs, model context overflow with large prior-task background, provider interrupted responses.

---

## F5 — Subtasks & Shared Family Budgets

### Flow
1. Top-level parent task asks for delegated work (`request_work`).
2. Outer loop spawns 1..N child subtasks (depth capped at 1; optional read-only constraint).
3. Parent and subtasks run concurrently/interleaved, consuming model and tool spend under one shared family budget.
4. Child outcomes flow back to parent via subtask result commands; stopping parent cascades stop to children.

### Safety Invariants
- **Depth limit:** Subtasks can never spawn grandchildren.
- **Shared family ceiling:** Neither parent nor any subtask ever issues a model call (or starts a new tool call inside a multi-tool batch or `compose` script) once cumulative family spend (`parent.Spent + parent.SubtaskSpent`, computed via `FamilyCost`) reaches or exceeds `Budget`.
- **Expected incomplete termination:** Budget-exhausted runs terminate as `failed` with `ExpectedIncomplete == true` and `BypassRouting == false` (unless cancelled first).
- **Read-only subtask enforcement:** Read-only subtasks never execute non-read tools, directly or inside `compose`.
- **Tariff accuracy:** Standard and dated-snapshot provider model IDs (e.g. `gpt-6.1-sol-2026-...`, `gpt-6.1-terra-...`) compute exact costs rather than silently falling back to unknown cost; missing provider usage remains unknown without failing work.

### Liveness / Functional Invariants
- Stopping a parent cancels all active children, waits for child completion and cleanup, and then finalizes the parent.
- Completed/failed/cancelled subtasks always deliver their result back to the parent so the parent resumes.

### Adversarial Faults
- Multiple concurrent subtasks racing against the shared budget, subtask running `compose` when parent has already spent most of the budget, multi-tool model batches crossing the budget mid-batch, parent stopped while subtasks are running or crashed, dated OpenAI model names.

---

## F6 — Durable Tools, Recovery & Script Composition (`compose`)

### Flow
1. Model invokes built-in runtime tools (`web_search`, `upload_inspect`, `memory_read`, `conversation_read`, `task_history_read`, `operation_read`, `results_find`, `result_read`, `result_link`, `capability_load`), external tools, or multi-step `compose` scripts.
2. Tools execute under durable operation receipts with retries, crash recovery, reconciliation of uncertain external side effects, and completion verification.

### Safety Invariants
- **Single external side effect:** No write or send `(RunID, OperationID)` executes externally more than once, even across crashes, retries, `CommitThenError`, or uncertain outcomes.
- **Single operation settlement:** Once settled as `confirmed` or `failed`, an operation's outcome and `ResultRef` never change.
- **Unified unresolved-operation semantics:** `unresolvedWrite`, `clearable`, `CheckCompletion`, `afterTool`, and `composeTool` agree on what counts as unresolved:
  - Cleared uncertain writes (`Cleared != ""`) do **not** block completion or subsequent writes.
  - Un-cleared uncertain or failed writes **do** block completion and subsequent writes.
  - Read operations never falsely trigger uncertain-write blocks or completion failures.
  - Control ops (e.g. `result_link`) never open un-clearable uncertain-action questions.
- **Safe `compose` concurrency & lifecycle:**
  - `for_each` parallel reads run concurrently; writes serialize so sibling writes never self-block on each other's `"pending"` op.
  - `ErrCancelled`, `ErrPaused`, and `ErrSuperseded` propagate out of `compose` instead of being swallowed into `"uncertain"` ops.
  - Read-only `needs_input` inside `compose` pauses for input instead of settling `"confirmed"`.
  - Replay divergence (different args/tool at same call position) halts without repeating side effects.
- **Safe runtime read-tool recovery:** Recovering an in-flight built-in read tool waits for its receipt deadline or re-runs safely instead of immediately failing and racing attempt 1.
- **Valid `ContextFrom` & clean parse errors:** `memory_read` respects `control.ContextFrom`; malformed provider response items do not append partial messages to task history.

### Liveness / Functional Invariants
- After a user answers an uncertain-action question, the write is cleared and the task can subsequently call `complete` and finish with status `completed`.
- Transient read errors retry and succeed; uncertain writes whose provider reconciler confirms execution settle without prompting the user.

### Adversarial Faults
- Activity crashes mid-tool (before admit, after admit before execute, during execute, after execute before settle), concurrent `for_each` writes in `compose`, steer/stop/pause during `compose`, uncertain external writes (both reconcilable and non-reconcilable), user answering uncertain-action question then completing, replay with diverged args.

---

## F7 — Permissions, Rules & Approval / Interaction Cards

### Flow
1. User creates, updates, lists, and revokes permission rules (allow and deny, scoped by tool, connection/wildcard, resource, action class, recipients, expiry, max operations, automation/goal).
2. Task prepares an external write/send/unknown action -> `permissions.Check` evaluates deny rules, existing approvals, allow rules, and policy -> either authorizes with an immutable authority record, denies, or opens a pending interaction card.
3. User responds (approve/decline/answer, possibly with stale revision, wrong digest, retries, or concurrent requests) and the answer is delivered to the waiting task.

### Safety Invariants
- **Zero unapproved writes:** Every executed write/send/unknown call has a durable authority record matching `(RunID, Revision, ActionDigest)`, backed by either an active non-expired allow rule or a resolved `"approve"` interaction bound to the exact `(RunID, Revision, ActionDigest)`.
- **Deny always beats allow:** A matching deny rule always blocks, including case-insensitive recipient matches (`Boss@example.com` matches `boss@example.com`).
- **Strict scoped allow matching:** An allow rule specifying non-empty `Recipients` or `ResourceIDs` never approves an action with empty `req.Recipients` or `req.ResourceIDs`.
- **Monotonic rule usage & valid limits:** Updating a rule preserves its `Used` count; negative `MaxOperations` is rejected; `RevokeRule` never exposes internal `used` fields.
- **Atomic decline side effects:** `OnDecline` cleanup never runs on stale-revision or rejected responses (only after transactional resolution commits).
- **Stale/superseded/expired rejection:** Approving an expired card, a card with mismatched `ActionDigest`, a card with stale `expected_revision`, or a card superseded by task steer/cancel is always rejected.

### Liveness / Functional Invariants
- Concurrent `MaxOperations` exhaustion on one allow rule falls through to remaining matching allow rules or opens an approval card instead of hard-failing the action.
- Idempotent response retries return the exact prior receipt and preserve `DefaultPurpose`/`DefaultConnection`.
- `UndeliveredAnswers` paginates across all resolved interactions (including >200 historical resolved cards) so no answered interaction is ever stranded undelivered.
- Expired interactions are consistently reflected in filtered queries (`status=pending` vs `status=expired`).

### Adversarial Faults
- Case-varied recipient addresses, empty targets against scoped allow rules, concurrent requests racing on `MaxOperations=1`, rule updates while in use, >250 historical resolved interactions followed by a failed delivery, stale-revision `decline` racing with `OnDecline`, mid-approval task steer/cancel, expired cards, duplicate response retries.

---

## F8 — MCP Servers, Credentials, OAuth & Outbound Egress

### Flow
1. User registers/updates an HTTP or local-stdio MCP server, connects/discovers tools/resources/prompts, configures exposure, executes tools, or connects/refreshes/signs out via OAuth.
2. Credentials are encrypted in Postgres (`secret/rowstore`), and all outbound HTTP requests pass through `netguard`.

### Safety Invariants
- **No credential exfiltration on endpoint change:** Changing an MCP server's `Endpoint` or `Transport` revokes/deletes the stored credential before any request can go to the new host.
- **Complete SSRF blocking (`netguard`):** Outbound requests never honor proxy env vars, never dial any non-public IPv4 or IPv6 range (including `0.0.0.0/8`, `192.88.99.0/24`, `::/96`, `::ffff:0:0:0/96`, `100::/64`, `2001::/32`, `2002::/16`, `fec0::/10`, CGNAT, NAT64, link-local, loopback unless explicitly allowed), and reject multi-dot internal hostnames (`localhost..`, `metadata.google.internal..`).
- **Strict OAuth safety:** `returnTo` rejects external, protocol-relative, backslash-prefixed (`/\evil.com`), query/fragment, and control-character paths; OAuth state consumption is atomic (single-use, no concurrent replay); PKCE verifier and client secret are protected and expired attempts are cleaned up; `SignOut` never mutates non-OAuth servers into `"oauth"` mode.
- **Bounded remote MCP payloads:** Discovery and execution enforce hard caps with loop `break` on tools, resources, templates, prompts, elicitations, content parts, and `StructuredContent`; remote tools starting with `@` cannot hijack synthetic protocol tools.
- **Crypto & crash safety:** Corrupted credential nonces return errors cleanly without panicking; cross-owner AAD decryption always fails; `Owner: true -> false` downgrade is rejected; missing stdio `CommandRef`, nil OAuth tokens, and malformed `RevokeURL` return errors without panicking; stdio child processes respect context cancellation.

### Liveness / Functional Invariants
- Valid MCP server registration, discovery, exposure control, tool execution, OAuth sign-in/refresh/sign-out, and credential rotation all succeed and reflect accurate `Observed` status.
- Concurrent OAuth refreshes handle version conflicts safely without losing rotated single-use refresh tokens.

### Adversarial Faults
- Endpoint change after saving bearer/OAuth token, malicious MCP server returning 10,000 paginated tools/resources or huge `StructuredContent` or `@resource_read` tool name, concurrent OAuth callbacks with same `state`, concurrent token refreshes, proxy env vars set, DNS rebinding / mixed public+private IPs, corrupted ciphertext/nonce rows, hung stdio child process with canceled context.

---

## F9 — Automations, Goals, Schedules & Inbox Delivery

### Flow
1. User creates, previews, updates, pauses, resumes, and deletes one-shot, recurring (RRULE), monitor, and event-relative automations and goals.
2. Scheduler ticks advance due automations, create idempotent occurrences, submit tasks (under unattended permission policy), settle occurrences on task completion, update monitor checkpoints, and publish inbox notifications.

### Safety Invariants
- **Idempotent occurrence creation:** No duplicate occurrence keys or duplicate task IDs per `(automation, generation, scheduled_at)`, even under repeated ticks, crashes, or `CommitThenError`.
- **Monotonic cursors & generations:** Automation `Generation` and schedule cursors never regress.
- **Timezone parity:** When `Trigger.Timezone` is empty, both `Preview` and execution use the user's configured timezone (falling back to UTC only when user timezone is also empty) — never silently diverging between preview and runtime.
- **Accurate recurrence & missed-run semantics:**
  - `missed_run: "skip"` skips missed slots even when `ExecutionWindowSeconds == 0`.
  - RRULE honors `WKST`, intersects `BYMONTHDAY` and `BYDAY`, interprets floating `UNTIL` in the schedule's timezone, and handles spring-forward/fall-back DST transitions cleanly.
- **Monitor prompt safety:** Prior monitor `Checkpoint` text is framed as untrusted data, never raw trusted instructions.
- **Valid UTF-8 & nil safety:** Text clipping never splits multi-byte runes; nil `Permissions` never panics.

### Liveness / Functional Invariants
- Every due enabled automation advances `NextAt > now` on drain.
- Every pending occurrence is admitted and every occurrence whose task reached a terminal state settles cleanly — even when >100 pending occurrences or >200 automations/goals/inbox items exist (full pagination).

### Adversarial Faults
- Empty trigger timezone with non-UTC user (`America/Los_Angeles`, `America/New_York`), DST spring-forward/fall-back boundaries, complex RRULEs (`WKST=MO`, `BYMONTHDAY=13;BYDAY=FR`, floating `UNTIL`), clock jumps testing `skip` vs `latest` vs `catch_up`, >150 queued occurrences, concurrent ticks, store faults during occurrence admission and settlement, untrusted monitor output.

---

## F10 — Uploads, Library, Results & Search

### Flow
1. User uploads files (validated size/type) and downloads them back or attaches them to messages/tasks.
2. Tasks save structured results, files, and screenshots to the library (including multi-version revisions sharing `RootID`).
3. User lists, exports Markdown, downloads raw files/screenshots, searches across library/messages/tasks/memory, and deletes single versions or `all_versions`.

### Safety Invariants
- **Download security headers:** Every upload and library file/screenshot download sets `Content-Security-Policy: sandbox; default-src 'none'`, `X-Content-Type-Options: nosniff`, and `Cache-Control: private, no-store`.
- **Deletion completeness & isolation:** Deleting with `scope: "all_versions"` hides every version sharing `RootID`; deleted items are refused by download endpoints and excluded from list and search results.
- **Artifact store error parity:** `localfs`, `s3`, and `memstore.Artifacts` return identical sentinel errors (`state.ErrFileExists`, `state.ErrNotFound`) for both `PutPNG`/`GetPNG` and `PutFile`/`GetFile`.
- **Unicode-safe search:** Search snippet extraction never panics or produces invalid UTF-8 on multi-byte or case-expanding Unicode characters (e.g. `İ`, emojis, CJK).

### Liveness / Functional Invariants
- Saved results, files, and screenshots are retrievable via both `/v1/results/{id}/file` and `/v1/library/{id}/file`, including Markdown export.
- Multi-version items list, revise, and delete cleanly under both `version` and `all_versions` scopes.

### Adversarial Faults
- HTML/SVG result files downloaded via `/v1/library/{id}/file`, screenshot results downloaded via `/v1/library/{id}/file`, deleting `all_versions` on a 3-version chain, searching documents containing `İ` (U+0130) and multi-byte UTF-8 near snippet boundaries, duplicate `PutPNG`/`PutFile`, missing `GetPNG`/`GetFile`.

---

## F11 — Operator Pause, Recovery, Wake Delivery & Graceful Shutdown

### Flow
1. Operator pauses the system globally (`system/control.paused = true`) -> no new task execution or paid model call may start, while `Stop` commands still settle via `task.StopPaused` and delete the corresponding `nori_task_leases` row.
2. Operator resumes -> `worker.Pool.RepairOnce` (leader-elected via Postgres advisory lock) and `app.App.Resume` / `RepairLeases` reconcile `nori_task_leases` and `nori_user_wakes` against authoritative `nori_records`, deliver pending commands, re-arm due wakes, and resume runnable tasks.
3. Unified binary `cmd/nori` (`--role=all`, `--role=api`, or `--role=worker`) receives `SIGTERM` while handling active HTTP requests, inner `task.Runner` turns, and outer conversation/scheduler lanes.
4. Real Postgres runs serializable transactions, `nori_task_leases` / `nori_user_wakes` claims, and `LISTEN/NOTIFY` wake fan-out under contention, duplicate inserts, panics, and cursor queries.

### Safety Invariants
- **Zero starts while paused:** No new task execution starts while `Control.Paused == true`; accepted `Stop` commands complete via `task.StopPaused` without invoking paid models.
- **Graceful HTTP shutdown:** `SIGTERM` stops accepting new connections while allowing in-flight requests to complete within the configured grace period (request contexts are NOT canceled prematurely by `BaseContext`).
- **Graceful worker pool drain:** `SIGTERM` signals `worker.Pool`'s `drain` channel before canceling active task contexts; in-flight `task.Runner` turns stop at the next checkpoint boundary and release their `nori_task_leases` rows (using a bounded detached shutdown release timeout) for immediate takeover by another replica.
- **Reliable Postgres wake delivery & repair:** State mutations emit `LISTEN/NOTIFY` (`nori_wake`) wakes alongside durable `nori_task_leases` and `nori_user_wakes` rows; missed notifications recover via `worker.Pool` polling and `wake.Repairer` (without holding locks across wake calls), while W3C `traceparent` propagates across HTTP edge-to-API requests.
- **Postgres transaction integrity:**
  - Callback panics always roll back the transaction and return the connection to the pool (zero leaked idle-in-transaction connections).
  - Duplicate `Create` returns `docstore.ErrExists` immediately without retrying 5×.
  - `attempts <= 0` behaves consistently with `memstore` (defaults rather than silently doing nothing).
  - Nested dot-path `StartAfter` cursors match `memstore` exactly.
- **Multi-user isolation & bounded app cache:** One failing user during `worker.Pool` claim/repair sweeps never aborts reconciliation for other users; transient DB errors back off and retry rather than causing a silent exit-0 shutdown; `app.Service` bounds cached per-user apps via `IdleTTL` / `MaxCachedApps` eviction while preserving active references.

### Liveness / Functional Invariants
- After unpausing and draining with faults disabled: zero pending commands, zero stuck finishing/cleanup/publication tasks, full `nori_task_leases` / `nori_user_wakes` ↔ `nori_records` quiescence (`CheckLeaseQuiescence`), zero unserved cancelled runs past `stopUnservedAfter`, and empty `WorkVersion(true)`.
- `sim/memstore` and `postgres` pass identical conformance assertions across CRUD, duplicate `Create`, panics, nested cursors, sequences, watches, task leases, and user wakes.

### Adversarial Faults
- Pause/resume toggled while Stop commands, expired leases, and finishing parents exist; `SIGTERM` mid-HTTP-turn and mid-`worker.Pool` execution; transaction callback panics; duplicate `Create` under contention; dropped `LISTEN/NOTIFY` wakes requiring poll/repair recovery; transient DB errors during `worker.Pool` claim/repair loops; concurrent `RepairOnce` replicas racing on advisory locks; multi-user repair sweeps with one failing user.

---

## F12 — Edge Surfaces: Embed Adapter & Web Workbench

### Flow
1. A host app embeds `integrations/embed/adapter.mjs` under `/api/nori/*`, authenticating users via `verifyIdentity`, minting HS256 service tokens, forwarding chat/work/interactions/results/SSE to Go core.
2. Local workbench (`web/`) authenticates users via OIDC or loopback email, proxies `/api/v1/*` and `/api/nori/*`, and renders Assistant, Work, Apps (MCP), Automations, Goals, Library, and Settings screens.

### Safety Invariants
- **Bounded streaming body reads:** Both embed adapter and web proxy enforce body caps while streaming chunks, rejecting oversized chunked bodies with `413` before buffering unbounded RAM.
- **Strict path & redirect safety:** All dynamic proxy route segments (`uploads/[id]`, `results/[id]`, `assistantRoute`, `adapter.mjs`) reject `.`, `..`, `%2e%2e`, and non-allowlisted characters; OAuth and link redirects reject `//` and `/\`.
- **Clean error classification:** Identity/service-auth failures return `503` (never misclassified as `502` upstream errors); upstream body-read failures return sanitized `502` (never unhandled `500`).
- **No legacy connection deadlocks:** With `connections` disabled until enterprise connectors exist, Settings permission creation targets `*` or registered MCP servers cleanly, and live contract scenarios cover the actual assistant surface (chat, SSE, work, interactions, results, MCP, permissions) rather than removed Google connections.

### Liveness / Functional Invariants
- Full round-trip through embed adapter and web proxy: send message -> stream SSE -> inspect work -> approve/decline interaction -> inspect/download result -> create/revoke permission rule -> add/connect local loopback or HTTPS MCP server.
- Fresh local startup succeeds cleanly with documented env setup and without leaking backend-only secrets into the Next.js server environment.

---

## F13 — Coupled 3-Tier Memory, Pre-Compaction Flush, AutoDream & FTS

### Flow
1. **3-Tier Memory & Budgets:** Core Memory (`owners/<owner>/memories/*`, `<= 24 KiB`, `<= 2000` B/item, deterministic `Pinned DESC, CreatedAt ASC, ID ASC` prefix order), Daily Episodic Logs (`owners/<owner>/memory_logs/<YYYY-MM-DD>`, `<= 64 KiB` / `128` entries per auto-sharded day), and Working Memory (`owners/<owner>/conversation/summary`, `<= 16 KiB`, `80` items, `4000` B gist, watermarks `EffectiveFlushedThrough <= EffectiveExtractedThrough` & `EffectiveFlushedThrough <= Through`, lossless `Excerpts` for `(FlushedThrough, Through]`).
2. **Three-Tier Out-of-Band Flush, Compaction & Session-Idle Extraction:**
   - **Coupled Pre-Compaction Flush (`flush_and_compact`):** Triggered immediately at soft threshold (`>= 12` turns or `>= 32 KiB` after `EffectiveFlushedThrough()`, keeping newest `CompactKeep = 5` verbatim) or via delayed `NextConversationWake` fast idle flush (`IdleFlushDelay = 2m` in `nori_user_wakes` once `>= 6` turns accumulate). Runs out-of-band with a `500ms` `UserRoutingPending` preemption watcher, skips re-extracting durable memories for turns `<= EffectiveExtractedThrough()` (preventing double-extraction while still summarizing them into Working Memory), commits Core upserts + demotions + Daily Log entries + `FitSummaryWithArchive` evictions + `nori_memory_chunks` FTS + `EventMemory` + `FlushedThrough` / `ExtractedThrough` in one `SERIALIZABLE` transaction, and falls back to lossless raw-log extraction after 3 consecutive failures (`2m` / `5m` / `10m` backoffs).
   - **Decoupled Memory Extraction (`ExtractTurnThreshold = 5` or `IdleExtractDelay = 5m`):** Fires immediately once `>= 5` unextracted completed turns accumulate, or via `NextConversationWake` at `LatestAt + 5m` for shorter `1..4`-turn chats whenever `>= 1` completed turn has `StartedAt > EffectiveExtractedThrough()`. Under the same `500ms` preemption watcher, Nori extracts durable Core Memory / Daily Log facts and advances only `ExtractedThrough` atomically while leaving `Through`, `FlushedThrough`, `Items`, `Gist`, `Excerpts`, and all `1..5` verbatim recent turns completely intact (with 3-strike raw-log fallback on `ExtractFailures`).
3. **Rapid Burst Hot Window & Idle AutoDream:** Rapid chat bursts (`> 20` turns / `48 KiB`) advance `Through` with lossless `Excerpts` while leaving `FlushedThrough` unchanged until the next idle beat. Triple-gated AutoDream (`!Pending && FlushedThrough == Through`, `>= 12h` since `LastDreamAt`, `>= 10` user-sourced log entries since `DreamThrough`, `+15m` quiet delay) or manual `POST /v1/memory/actions` (`consolidate`) merges duplicates, resolves superseded facts, promotes recurring user facts, and demotes stale unpinned Core items atomically.
4. **Isolated FTS, Tail Auto-Recall & Inner Recall Tools:** Dedicated 32-way hash-partitioned `nori_memory_chunks` (dual-dictionary `english` + `simple` GIN `tsvector`, `READ COMMITTED READ ONLY` `< 2ms`, 30-day half-life decay with `1.15` pinned / `1.0` evergreen Core, MMR `lambda = 0.75`, `<= 700` B snippets) powers outer tail `advisory` auto-recall (`<= 4 KiB` / TopK=8), inner `memory_search` / `memory_get` (alongside `memory_read`, `conversation_find`, `conversation_read`, `operation_read`, `task_history_read`), and `/v1/memory/search` + `/v1/search`. Completed tasks append bounded (`<= 600` B) `task_outcome` entries to today's Daily Log in `Publish` without mutating Core Memory.
5. **Unified `/v1/memory*` API & Durable Receipts:** All memory mutations persist durable `mutation_receipts` (`GET /v1/mutations/{request_id}`), enforce `409 idempotency_conflict` and `409 revision_conflict` (with `current`), emit SSE `event: memory`, and enforce `ContextFrom` and cross-owner isolation.

### Formal System Invariants (`INV-MEM-01..08`)
Checked continuously in `core/internal/sim/invariants/` (`CheckSafetyAt` after every chaos step + `CheckQuiescence` post-drain):

| ID | Invariant | Formal Property |
| --- | --- | --- |
| **`INV-MEM-01`** | **Atomic Watermark** | `From <= EffectiveFlushedThrough <= EffectiveExtractedThrough` (both monotonic per `ContextFrom` epoch), and `EffectiveFlushedThrough <= Through` whenever `!Through.IsZero()` (`ExtractedThrough` may lead `Through` or exist while `Through` is zero after 5m session-idle extraction); every completed turn in `(FlushedThrough, Through]` is represented in `Summary.Excerpts` (within `OuterInputBytes`); neither `FlushedThrough` nor `ExtractedThrough` advances on failed/canceled calls. |
| **`INV-MEM-02`** | **No Silent Summary Eviction** | Any item removed from `Summary.Items` across steps (same epoch) is either in `DismissedItemIDs`, semantically superseded by a newer batch, or preserved as a `summary_evicted` `MemoryLogEntry`. |
| **`INV-MEM-03`** | **Core Budget & Pinning** | `sum(len(m.Text)) <= 24 KiB`, `1 <= len(m.Text) <= 2000` valid UTF-8, `m.CreatedAt <= m.UpdatedAt`, and `Pinned` items are never auto-demoted by background passes. |
| **`INV-MEM-04`** | **Provenance & Injection Isolation** | Zero `task_outcome` items in `owners/<owner>/memories`; every `user_explicit` / `pre_compact_flush` item (unless `SourceInputID == "ui"`) matches an existing `IsUser()` `conversation_inputs` doc. |
| **`INV-MEM-05`** | **Turn Non-Application** | Non-completed/superseded attempts leave zero mutations in `memories`, `memory_logs`, or `MemoryIndex`; `resolvedReply` checkpoint recovery creates zero duplicates. |
| **`INV-MEM-06`** | **Store ↔ FTS Bijection** | Exact 1-to-1 bijection at every step between `owners/<owner>/memories` ∪ `owners/<owner>/memory_logs/*` and `MemoryIndex` chunks. |
| **`INV-MEM-07`** | **Tenant & `ContextFrom` Isolation** | Zero cross-user references; `memory_read`, `memory_search`, `memory_get`, `conversation_find`, `conversation_read`, `/v1/memory*`, and `/v1/search` return zero pre-`ContextFrom` log/turn items. |
| **`INV-MEM-08`** | **Post-Drain Quiescence** | After advancing virtual clock past `IdleFlushDelay` (`2m`) / `IdleExtractDelay` (`20m`) / `compactAfter` and running `DrainToQuiescence`, `EffectiveFlushedThrough == Through` (when compacted), zero completed turns remain unextracted past `IdleExtractDelay`, and `nori_user_wakes[user, LaneConversation]` has `Owner == ""`, `LeaseUntil == nil`, and (`!Runnable || WakeAt > now`). |

### Conformance, Scenarios 1–8 & `FullWorld` Fuzzing
- **`memstore` ↔ Postgres 18 `MemoryIndex` Conformance (`core/internal/sim/memstore/conformance_test.go`, `make sim-pg`):** Verifies identical `docstore.MemoryIndex` behavior across `memstore` and Postgres 18 (`005_memory_search.sql`), including dual-dictionary tokenization (code symbols, UUIDs, emails), 30-day recency decay, pinned boost, MMR diversity reranking (`lambda = 0.75`), `CreatedAfter` filtering, same-tx rollback, `BackendSnapshot.Digest()`, and `0` `nori_records` seq scans.
- **8 Dedicated DST Scenarios:**
  1. `TestDST_FlushBeforeCompactAtomicityAndFailureRecovery` (`sim/outer/memory_compaction_sim_test.go`): provider errors, `missingTool`, `CommitThenError`, and 3-strike raw-log extractive fallback.
  2. `TestDST_MidFlushUserArrivalPreemption` (`sim/outer/memory_compaction_sim_test.go`): deterministic `500ms` watcher tick preempting mid-flush without advancing `FlushedThrough`.
  3. `TestDST_RapidBurstHotWindowAndIdleCatchup` (`sim/outer/memory_compaction_sim_test.go`): rapid 50-turn burst staging lossless `Excerpts` + `+2m` idle wake drain.
  4. `TestDST_CoreOverflowDemotionAndPinnedProtection` (`sim/outer/memory_compaction_sim_test.go`): `> 32 KiB` Core writes auto-demoting oldest unpinned items beyond `24 KiB` while protecting `Pinned` items and preserving FTS recall.
  5. `TestDST_PromptInjectionAndProvenanceGuard` (`sim/outer/memory_compaction_sim_test.go`): blocking malicious `task_outcome` -> Core injection while verifying mixed `dispatch + remember` and `TaskEvent`-coalesced `remember` turns succeed.
  6. `TestDST_AutoDreamConsolidationAndConcurrentReplicaMerge` (`sim/outer/memory_compaction_sim_test.go`): competing worker replicas + concurrent UI `EditMemory` + 2-run SHA-256 determinism.
  7. `TestDST_InnerContext128KiBAndMemoryRecallTools` (`sim/inner/tools_completion_sim_test.go`): `> 128 KiB` inner task transcript exercising `fitContext` -> `projectContext`, all 7 recall tools (`memory_read`, `memory_search`, `memory_get`, `conversation_find`, `conversation_read`, `operation_read`, `task_history_read`) across a `ContextFrom` boundary, and bounded (`<= 600` B) `task_outcome` Daily Log append on `Publish` with zero Core mutation.
  8. `TestDST_MemoryAPIIdempotencyReceiptsAndSearch` (`sim/api_memory_sim_test.go`): full `/v1/memory*` HTTP surface over `httptest` + `memstore`, durable `GET /v1/mutations/{request_id}` receipts, `409 idempotency_conflict` / `409 revision_conflict` (with `current`), `task_outcome` promotion guard, `consolidate` idempotency, SSE `event: memory`, `/v1/memory/search` & `/v1/search` `ContextFrom` + cross-owner isolation, and `> 24 KiB` Core overflow auto-demotion preserving `Pinned` items.
- **`FullWorld` Full-Stack Chaos Actions (`core/internal/sim/e2e_world.go`, `workload.go`, `regression_seeds_test.go`):** Weighted chaos actions for API memory mutations (Core create/edit/pin/unpin/demote/forget, Daily Log promote/forget, Working promote/dismiss, `SearchMemory`, manual `ConsolidateNow`), rapid chat bursts (4–8 quick turns staging `Excerpts` & soft compaction), virtual-clock idle jumps (`+2m5s` flush wake, `+15m5s` AutoDream wake, `+12h5m` dream interval), delayed `NextConversationWake` re-arming/execution in `LaneConversation` worker handling and lease repair, and post-drain `FlushedThrough == Through` verification across 200+ seeds (`make sim-deep`) with 2-run SHA-256 determinism.

