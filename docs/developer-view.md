# Nori developer view

| | |
| --- | --- |
| Status | Draft: the full list of what the view shows |
| Author | Austin Barwick |
| Last updated | 2026-10-25 |
| Scope | Everything the web app's developer view shows. Layout comes later. |
| Related | [Client contract](client-contract.md), [separation of concerns](separation-of-concerns.md) |

The owner's interface shows a short reply, task status and Stop. The developer view shows everything behind that: what each loop saw, what it chose, what it did, and what the runtime was doing at the time. It is for Austin as Nori's builder, not as its owner.

## Why

Every question about Nori's behavior on 2026-10-25 was answered by reading source code, never by looking at a run.

- **Claude, inner loop design.** The tool list, the 6 model-call and 12 tool-call budgets, the 1024-token output cap and what the Context activity loads were all found by grepping `core/`. A drawing of the task loop was read as the outer loop, because nothing shows which loop handled a message. It took three corrections to sort out.
- **Codex, local outer loop runner.** Proving that the model returns text and a tool call in one response took a separate script. A Jev outage showed up only as a stdout line. Latency was timed by hand (5.79 s for a chat turn, 10.15 s for dispatch acceptance).
- **The runtime today.** Task events carry only `{kind: "tool", text: "shell"}` and `{kind: "model", text: "Thinking"}`. The full prompt and every tool argument and result exist only inside Temporal history, and no one read it all day.
- **The tools used to build Nori.** Claude Code, Codex and ChatGPT record nearly all of this for themselves (see [reference](#reference-what-other-harnesses-record)).

## Boundary

- **Owner only, off by default.** Each device turns the view on or off. It is never part of the owner's normal interface.
- **Its own read API.** Developer data is served under `/v1/dev/…` and never mixed into `/v1/snapshot` or `/v1/events`. Contract §9 says clients never receive prompts, system context, tool schemas or worker transcripts. That rule needs a carve-out that names this API. **Open decision.**
- **Read-only.** Stop and approvals stay where they are. Editing or replaying internals remains a non-goal.
- **Secrets never leave the service.** Credentials, tokens and `.env` values appear only as name, present or missing, and length.
- **One set of IDs.** Every item is keyed by the same correlation IDs: `command_id`, `turn_id`, task ID (the run ID today), workflow ID and Temporal run ID, `operation_id`, computer scope ID, `artifact_id` and `seq`. Clicking any ID filters the view to it.
- **Thinking, honestly.** Nori shows whatever reasoning the provider returns. Gemini returns thought summaries, thought token counts and opaque thought signatures, not raw thoughts. The view labels which of these it is showing.

## Today column

| Mark | Meaning |
| --- | --- |
| **Kept** | Recorded today and readable. |
| **History** | Exists only inside Temporal workflow history. A history read exposes it with no new storage. |
| **Dropped** | Computed today, then thrown away. |
| **Missing** | Not recorded anywhere. |
| **Not built** | The concept doesn't exist on main yet: the outer loop, Jev, approvals, contract events. |

## 1. Inputs and routing

One row per owner message or trigger.

| Item | Shows | Today |
| --- | --- | --- |
| Input | ID, thread, text and parts as received, `client_command_id`, device, arrival time | Kept: `owners/{owner}/messages` (`id`, `run_id`, `role`, `text`, `created_at`). No device or command ID. |
| Origin | Typed, chip, control button, approval control, scheduled check, task result, needs-input event, task failure, alert | Missing |
| Route | Outer turn, or delivered straight into a task as input (which task, which revision) | Missing. While a task runs, every message becomes that task's input (`useRuntime.ts`, `runId = running.id`). |
| Jev answers | Each question, its options, the chosen option, probabilities and confidence. Intent (`converse`, `ask`, `request_work`, `steer`, `provide_input`, `decide`, `inspect`, `control`, `update_memory`), target task and target confidence | Not built. Codex's runner work adds `classification_hint{intent, confidence, available}`. |
| Jev availability | Whether classification ran, its latency and error, and the default applied (`converse`) | Not built. The runner logs only "Jev hint unavailable". |
| Mixed messages | The parts of a split, which part waits on which task's result, and which part interrupts | Not built |
| Steering | Mode (steer, collect, follow-up, interrupt, rollback), queue position, which model call absorbed it, and the stale decision it discarded | Partly kept: `task_commands{kind, text, position, revision, delivered}`. Which model call absorbed it is missing. |

## 2. Outer-loop turns

| Item | Shows | Today |
| --- | --- | --- |
| Turn | `turn_id`, trigger, cause message IDs, thread | Not built |
| Phases | `thinking`, `writing`, `done`, `superseded` or `failed`, each with a timestamp | Not built (contract `turn.updated`) |
| Model calls | Every call the turn made (§4) | Not built |
| Actions | `respond`, `dispatch`, `update_dispatch`, `clarify`, each with its full arguments | Not built. The runner on `codex/local-agent` offers only `dispatch{request}`. |
| Validation | Per action: does the target exist, permission scope, duplicate command, current state. Rejected actions and the reason | Not built |
| Commands | Command IDs, `seq`, delivery intent, delivery state (delivered, pending, retrying) | Not built. "Temporal delivery remains pending" appears only on stdout. |
| Publish check | Inputs that arrived during the turn, and whether and why the turn was superseded | Not built |
| Failure | The full reason, beside the owner-safe text | Missing. A 500 returns a generic message, and the real error goes to `log.Printf`. |
| Notification | `alert`, `badge` or `silent`, the reason, the proactive flag, quiet hours, the daily-cap count | Not built |
| Latency | Acceptance, first `thinking`, first token, publish | Missing |
| Instructions | Hash and version of the outer-loop system prompt used | Not built |

## 3. Context

Per model call, in both loops: the context exactly as the model received it, as a stack of blocks.

| Item | Shows | Today |
| --- | --- | --- |
| Blocks | System instruction, standing instructions, soul, memory summary, date and time zone, recent conversation, historical task records, task commands, active-task summaries, Jev outputs, advisory context, tool results, attachments | History: the `Model` activity input (`ModelInput.Contents`). The system instruction is a constant in `steps.go`. Inline mode keeps nothing. |
| Sources | For each block, the records it came from: which messages, which runs, which document revision | Missing |
| Size | Tokens and bytes per block, and total fill against the model's window | Missing |
| Budgets | Each block's limit against its use: 20 messages and 64 KiB of history, 30 runs scanned, 1024 output tokens | Missing |
| Cuts | What was left out and why: over budget, run not completed, outside the window, tool output truncated | Missing |
| Diff | What changed since the previous call in the same turn or task | Missing |
| Gaps | What the design says belongs in context but isn't there. Today the task prompt has no soul, memory, date or time zone. | Missing |
| Raw request | The exact JSON sent to the provider, copyable | History |

## 4. Model calls

| Item | Shows | Today |
| --- | --- | --- |
| Identity | Call number in the turn or task (`m<n>`), activity ID, attempt | History |
| Model | Provider, location, requested model, resolved model version, response ID | Partly kept: `MODEL_NAME` env (`gemini-3.1-flash-lite`). `ModelVersion` and `ResponseID` are dropped. |
| Config | Max output tokens, thinking level or budget, temperature, tool config, safety settings | Code only. Nothing is set except `MaxOutputTokens: 1024`. |
| Tools offered | The catalog given on this call (§5) | Missing |
| Response | Every part in order: text, function calls with arguments, thought summaries, thought signatures | History. Thoughts are never requested. |
| Thinking | The reasoning returned, with thought token count | Missing: no `ThinkingConfig`, and `Text()` skips thought parts |
| Text beside tool calls | What the model wrote alongside its calls | History. Never shown. |
| Finish | Finish reason, safety ratings, citations | Dropped. A finish reason other than `STOP` becomes "model response incomplete". |
| Usage | Prompt, cached, output, thought, tool-use prompt and total tokens | Dropped (`UsageMetadata`) |
| Cost | Computed from usage and the price table | Missing |
| Timing | Start, first token (once streaming), end, the 45 s timeout | Missing. History has activity times. |
| Outcome | Used, or discarded because the revision moved (still counted against the budget) | Missing. Discarded silently. |
| Errors | Provider status (such as 429), retryable or not, attempts (always 1 today) | Partly kept: `run.error`, only when fatal |

## 5. Tool catalog

What a model could use, as opposed to what it used.

| Item | Shows | Today |
| --- | --- | --- |
| Offered | The tools on each model call, with full schemas as sent | Missing per call. Declared in `steps.go`. |
| Why in or out | `wait_for_input` exists only in durable mode. Computer tools are offered even when `COMPUTER_ZONE` is empty. Durable mode requires guest receipts. | Missing |
| Schema version | A hash per tool, and changes between calls | Missing |
| Readiness | Each tool's backend: computer reachable, guest `/healthz` (`ready`, `operation_receipts_v1`), CDP up, credentials present (names only) | Missing |
| Connectors | When connectors arrive: connected, failed or needs auth, and the tools each exposes against what it promised | Not built |
| Limits | Shell 45 s; arguments 8192 bytes; stdout and stderr 64 KiB each; results 32 KiB; page text 64,000 characters; screenshots 2 MiB | Code only |

## 6. Tool calls and operations

| Item | Shows | Today |
| --- | --- | --- |
| Identity | Call ID, operation ID (`<run>-m<n>-t<n>`), revision, `admitted_at` | Partly kept: `runs/{id}/operations/{opID}`, durable mode only |
| Arguments | In full: the command, the URL, the question | History. `run.events` keeps only the tool name. |
| Result | In full: stdout, stderr and exit code; URL, title and page text; the screenshot | History. Screenshots are kept in GCS (`artifacts/{run}/{id}.png`). |
| Truncation | Which cap applied, and the original size | Missing. The result becomes `{output, truncated: true}`. |
| Timing | Start, end, duration; timeout (exit code 124) | Missing. Events have only `created_at`. |
| Error | Message, `ApplicationError` type (`superseded`, `cancelled`), attempts | Partly kept: a fatal error ends the task with `run.error` |
| Skipped | Calls answered with `{"skipped": true, reason}` after a correction | History |
| Receipt | Request digest, `started` or `completed`, HTTP status, content type, body. `uncertain` when it started but never finished | Kept on the guest disk (`/workspace/tasks/.nori-receipts/{opID}.json`). Core never calls `/operation`. |
| Operation state | `prepared`, `attempted`, `submitted`, `confirmed`, `failed`, `uncertain`, with the receipt | Not built |
| Approval | `operation_rev`, what, why, impact, the decision and the deciding device, superseded or expired | Not built |
| Process | Computer scope ID (`<run>-r<rev>`), its cgroup, whether Stop killed it | Missing |
| Workspace | Files in `/workspace/tasks/<task_id>` after the call | Missing |

## 7. Tasks

The inner loop, one task at a time.

| Item | Shows | Today |
| --- | --- | --- |
| Identity | Task ID (run ID today), workflow ID `nori-task-<runID>`, Temporal run ID, path (inline or Temporal), the worker build that ran it | Partly kept: run ID and `temporal`. Build missing. |
| Origin | The dispatch or input that started it, turn ID, source message IDs, `follows_task_id` | Partly kept: the `<run>-user` message. Turns aren't built. |
| Brief | The request as accepted, interpretation, completion condition, category, scoped authority | Not built. Today the transcript is the brief. |
| Revisions | Every `start`, `input` and `cancel` command with position, revision and delivered flag; `revision` against `incorporated` | Kept (`task_commands`, `runs`), never shown |
| Status | `running`, `waiting` (wait reason, question, resume condition), `needs_input`, `blocked`, `completed`, `failed`, `cancelled`, each with a timestamp | Partly kept: `status`, `waiting`, `started_at`, `finished_at`. Transitions aren't timestamped. |
| Stop | `requested`, `stopping`, `stopped`, `not_applied` with times; `cancel_requested`; `StopConfirmed` | Partly kept |
| Budgets | Model calls out of 6, tool calls out of 12, revisions out of 32, commands out of 32, including superseded calls | Missing. The counters live only in the workflow. |
| Deadline | Run deadline (session expiry in durable mode, two minutes inline) and workflow execution timeout (plus 20 s grace) | Partly kept: `deadline` |
| End cause | Which guard ended it: budget, finish reason, tool error, deadline, pause drain, Stop or crash, beside the status recorded | Missing. Session expiry is recorded as `failed`. |
| Discarded work | Model decisions dropped and tool calls skipped because the revision moved | Missing |
| Completion | `complete(answer, evidence, unmet)` and the runtime's checks: revision current, constraints met, a receipt for every claimed effect | Not built |
| Result | Final text and artifacts, verified against `instruction_rev` | Partly kept: the final message and `artifacts[]` |
| Now | What it is doing this second: a model call in flight, a tool running, a wait, and for how long | Partly kept: projections `model` ("Thinking") and `tool` (the name) |
| Crash | A failure event listing completed operations, and a link to the re-issued task | Not built |

## 8. Temporal

| Item | Shows | Today |
| --- | --- | --- |
| Execution | Status, start and close, execution timeout, task queue `nori-tasks`, namespace, history length and size | Missing in the app |
| History | Each activity's scheduled, started, completed or failed times and attempts; `task-command` signals; timers | Missing in the app |
| Activity data | Inputs and outputs of `Context`, `Reconcile`, `Model`, `Tool`, `Stop`, `Publish`, `Project` | Missing in the app |
| Payload size | Per activity. Today the whole transcript is copied into every `Model` input, and tool output appears twice. | Missing |
| Pending | Activities in flight and their last heartbeat | Missing |
| Live state | Through a workflow query: `contents` size, `revision`, `incorporated`, `modelCalls`, `toolCalls`, `usedComputer`, `computerRuns`, `finalText` | Missing. There is no query handler. |
| Workers | Deployment `nori-task`, the build pinned to each workflow, pollers and backlog on the queue | Missing |
| Recovery | `RepairClosed` and `Drain` runs, and what each cancelled | Missing |
| Link out | The workflow in the Temporal Cloud UI | Missing |

## 9. Computer

| Item | Shows | Today |
| --- | --- | --- |
| VM | `nori-computer` status (`RUNNING`, `TERMINATED` and so on), zone, machine type, last start and stop, cold-start time | Missing. Core sees the status and records none of it. |
| Lease | Which task holds the computer, since when, `computerRuns` | Missing |
| Tunnel | IAP tunnel up or down, and its errors | Dropped: tunnel stdout and stderr are discarded |
| Guest health | `/healthz` `ready`, `operation_receipts_v1`, CDP reachable | Missing |
| Browser | Current URL, title, live screenshot, tabs | Missing. There is one CDP page and no endpoint for any of this. |
| Screenshots | Every screenshot a task took, in order | Kept (GCS) |
| Workspace | Files under `/workspace/tasks/<task_id>`, receipts, `.cancelled` markers | Missing |
| Processes | Running processes per operation cgroup; CPU, memory, disk | Missing. Cgroups are used only to kill. |
| Guest logs | `/var/log/nori-computer-setup.log`, `/var/log/nori-vm-console.log`, the `bot-computer-agent.service` journal | Missing in the app |
| Disk | Retained disk `bot-computer-data`: size and use | Missing |

## 10. Runtime and session

| Item | Shows | Today |
| --- | --- | --- |
| Build | Git commit and image for each component (core service, worker, deliver, computer agent, web), Cloud Run revision, `TEMPORAL_BUILD_ID`, contract version | Missing |
| Routes | The operations core serves right now | Missing |
| Config | `MODEL_NAME`, `MODEL_LOCATION`, `TEMPORAL_ADDRESS`, `TEMPORAL_NAMESPACE`, `TEMPORAL_TASK_QUEUE`, `TEMPORAL_LOCAL`, `ARTIFACT_BUCKET`, `COMPUTER_ZONE`, `COMPUTER_INSTANCE`, `FIRESTORE_DATABASE_ID` with values. Secrets as present and length. | Missing |
| Session | `system/control{paused, expires_at}`, session start, access expiry, shutdown time, hard deadline, whether shutdown was verified, whether WCI access is on, scheduled shutdown jobs | Partly kept: `paused` and `expires_at` reach the web app. The rest exists only in `session.tfvars.json` and Terraform output. |
| Capacity | Worker pool instances (current, min, max), Cloud Run instances, worker concurrency (1 activity, 2 workflow tasks) | Missing |
| Owner lock | `active_run`, and why a run was refused (`ErrBusy`, `ErrPaused`) | Kept (`active_run`), never shown |
| Health | Each service's `/healthz`, plus Temporal, Vertex and Jev reachability | Missing. The web app doesn't proxy `healthz`. |
| Pause and resume | Steps done, steps pending, verification results | Missing |
| Storage after pause | Retained disk, GCS objects, Firestore size, images, with estimated monthly cost | Missing |

## 11. Memory and identity

| Item | Shows | Today |
| --- | --- | --- |
| Documents | Soul and memory: current revision, author (owner or Nori), revision history with notes | Not built in the service |
| Reads | Which revision each turn and task read | Not built |
| Writes | Which turn or task changed what, the diff, the `update_memory` intent | Not built |
| Instructions | Standing instructions in effect and where each came from | Not built. The system prompt is a constant. |
| Recall | Which earlier messages or task records were pulled into context, and why | Missing. The Context activity picks the last 20 completed-task messages and doesn't record which. |

## 12. Event log and client sync

| Item | Shows | Today |
| --- | --- | --- |
| Durable events | `seq`, `type`, `at`, `cause` (`command_id`, `turn_id`, `task_id`), `data` | Not built |
| Live stream | `turn.delta`, `task.progress`, `presence.snapshot`, `heartbeat` | Not built. The web app polls `/api/session` every 2 s. |
| Commands | `command_id`, type, payload, accepted `seq`, idempotency hits, rejection codes | Not built |
| This client | Cursor `seq`, stream state, reconnects, `resync_required`, outbox (pending, retrying, failed), optimistic entries awaiting reconciliation | Not built |
| Device | `device_id`, platform, app version, `min_client_version` | Not built |
| Rendering | Raw message text before hints are stripped (`extractHints`), the bubble-or-page decision with its reasons (`classify().reasons`), parts shown as fallback text | Computed in the browser, never shown |
| Hidden fields | What `/api/session` already returns but the UI doesn't show: run `id`, `revision`, `incorporated`, `started_at`, `deadline`, `finished_at`, `artifacts`, and events of runs that aren't selected | Kept. Can be shown today. |

## 13. Usage and cost

| Item | Shows | Today |
| --- | --- | --- |
| Tokens | Input, cached, output and thought tokens, per model call, turn, task and day | Dropped |
| Context fill | Per call, against the model's window | Missing |
| Budget burn | Per task (§7) | Missing |
| Money | Per call, turn, task and day, by provider and model | Missing |
| Session spend | This session's worker pool time, computer VM time, Cloud Run, Temporal actions, Vertex and storage | Missing |
| Jev | Calls and cost | Not built |
| Limits | Provider rate limits and quota, when the provider reports them | Missing |

## 14. Logs, traces and errors

| Item | Shows | Today |
| --- | --- | --- |
| Logs | A turn's or task's logs across core, worker, deliver and the computer agent, filtered by ID | Missing. Logs are plain `log.Printf` with no IDs. |
| Trace | A waterfall from command to turn, dispatch, workflow, activity, tool and guest, through `traceparent` | Not built (contract §11) |
| Errors | Recent errors grouped by code: `ErrBusy`, `ErrPaused`, `ErrSuperseded`, `ErrDuplicate`, delivery pending, Stop recovery pending, provider errors | Missing |
| Metrics | Contract §11: acceptance and fan-out latency, open streams, reconnects, `resync_required`, duplicate commands, superseded turns, rejected decisions, time in each Stop state | Not built |

## Reference: what other harnesses record

From the Claude Code, Codex and ChatGPT threads of 2026-10-25.

| Harness | What it records or shows | Section |
| --- | --- | --- |
| Claude Code | A context breakdown by category (system prompt, tools, connector tools, memory files, messages, free space). Full thinking and every tool call in the transcript view. Usage per request, including cache reads and writes and thinking tokens. Every injected instruction labeled by source (project and user instructions, memory, output style, hooks). Tool-list changes, including connectors that failed or need auth. Permission decisions with their source. The steer queue: enqueued, then absorbed mid-turn. | 1, 3, 4, 5 |
| Codex | A snapshot of everything that shapes the context, stored as hashes so changes can be diffed. Every injected block labeled by kind. The exact tool schemas given to the model. Per-turn settings (model, effort, approval and sandbox policy). Token counts with context window and rate limits. Truncation markers with the original size. Time to first token and duration. Fork lineage. | 3, 4, 5, 13 |
| ChatGPT | Requested and resolved model. Thinking effort. Reasoning titles and duration ("Worked for 1m 13s"). Tool traffic kept as messages hidden from the conversation. Tool status labels during and after a call. Citations, and which memories or past chats were used. In agent mode, a live view of its browser. | 4, 9, 11 |

None of them shows everything. Codex stores its reasoning encrypted, and its summaries were empty in 210 of 219 reasoning items today. ChatGPT's agent-mode browser actions aren't in the conversation record at all, so the live view is the only window. Nori owns its runtime, so it can keep everything.

## What the service must start recording

In order of value for effort:

1. **A history read.** A developer endpoint that reads a task's Temporal history gives per-call prompts, raw model output, full tool arguments and results, and activity times and attempts. It needs no new storage, but it covers only durable mode.
2. **Model metadata.** Keep `UsageMetadata`, `ModelVersion`, `ResponseID`, `FinishReason` and latency in the `Model` activity result.
3. **Thinking.** Set `ThinkingConfig` with `IncludeThoughts` and a level, then keep the thought summaries and signatures.
4. **Full task events.** Each tool event carries an argument summary, a result summary, start, end, error, truncation and operation ID, not just the tool name.
5. **Per-call catalog and context map.** Record the tools offered and the system instruction hash on every model call, and the source and size of each context block.
6. **A live-state query** on `nori-task-v1`: counters, revision, computer lease.
7. **Structured logs** carrying run, command, workflow and operation IDs, plus `traceparent`.
8. **A computer probe.** VM status, guest health, and browser URL, title and screenshot on demand.
9. **A runtime panel.** Build per component, config, session control and capacity, read by the service from GCP and Firestore.
10. **Outer-loop records,** once the outer loop lands from `codex/local-agent`: a routing record per input, Jev answers, actions, validation and turn phases.
