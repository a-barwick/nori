# Nori refactor decisions

The audit starts from `36aae02`. The scope covers the Go runtime, web client, infrastructure and documentation build. The two optional passes retain comments only where they explain a constraint and remove the test material. The baseline had already dropped its executable suites. Paths and line numbers in the findings refer to the baseline.

## Bind infrastructure at startup

- Pass Temporal connection, queue and deployment settings explicitly. `platform/orchestration.go:71` and `task/client.go:19` keep mutable process globals, and the API, recovery and workers depend on a startup call to populate them. Select the secret lookup alongside the connection and pass the queue through worker registration and polling.
- Give storage constructors the user scope. `platform/platform.go:32` exposes an unused configuration override, and `state/records/database.go:22` relies on a second initialization call before any records can be read. Keep the directory connection separate from the user record connection.
- Parse the configured worker routes once per process. `platform/orchestration.go:27` and `cmd/nori-worker/config.go:60` decode the same configuration independently. Worker wake delivery and startup validation need the same route map.

## Keep durable acceptance and delivery together

- Put command signaling, worker wake and acknowledgement in one delivery path. `dispatch/dispatcher.go:110` repeats the acknowledgment sequence across task status branches. A saved stop for a paused or terminal task still needs to address the existing execution. Ordinary commands for a terminal task can be acknowledged without delivery.
- Share equivalent pending and live lease checks across `state/conversation.go:270`, `conversation/session.go:98` and `dispatch/dispatcher.go:218`. Keep lease-owner fencing and the separately observable processing state for records that predate the current schema.
- Use the same upload validation and create-once byte write in `conversation/uploads.go:16` and `chat/service.go:483`. Keep the assistant and chat storage keys and their existing metadata conflict rules.
- Keep chat turn ownership with its active reply. `chat/service.go:202` and `:239` use the position of the reply in the loaded message tail as a hidden contract. The same attention predicate appears in `state/chat.go:89` and `:131`. Use the actual reply ID and reuse the message predicate. Wake an in-process waiter when its local run completes and keep the bounded database reconciliation for other processes.
- Remove the impossible error from `task/history.go:110` and its callers. Give `task/activities.go:177` a single explicit interruption policy. Reuse the revision lookup and scope list that `task/recovery.go:74`, `:128` and `:190` build independently. Keep activity error names consistent between the producer, workflow and telemetry, with the existing serialized values and activity sequence.
- Resolve user scope at activity registration. Each task activity currently has to remember its own `Resolve` guard before accessing state. Register each existing activity name with a typed scope adapter and preserve its inputs, outputs and workflow call order.
- Keep separate wake and resume selection rules where the recorded build metadata has different precedence. Preserve failed or ambiguous operation records, old chat attention evidence, stable cursor ordering and the storage engines' transaction and retry semantics.

## Define task tool policy once

- Colocate the ordered tool schemas and their execution policy. `agent/steps.go:23`, `agent/catalog.go:66`, `task/tool_durable.go:20`, `task/workflow.go:418` and `task/jobs.go:287` each know which tools read, mutate, need the computer or incur a cost. Task telemetry has another list with a deleted calendar tool and missing current tools at `task/telemetry.go:66`. Keep the default conservative for an unrecognized name and keep per-tool validation messages and paid-read accounting.
- Treat the capability's tool names as data. `agent/catalog.go:29` stores a comma-separated string that `agent/steps.go:398` parses to recover tool ownership. Render the same text for models and saved capability results.
- Keep the workflow's completion, question and job-wait branches. They advance the durable task in different ways. Keep operation-evidence reads in the task activity that owns that evidence.

## Put computer RPC admission beside registration

- Define the guest endpoints once for the host proxy and guest mux. `computerd/server.go:58` registers RPCs, then repeats every permitted method and path at `:193`. The file upload limit and the set of interruptible reads are inferred from paths in `server.go:218` and `receipts.go:154`. Put those policies on the guest route. Keep the activity endpoint host-only, the request cap, receipt digests and saved response format.
- Share the RPC path, header and health vocabulary with `computer/computer.go:74` and the job and operation clients. Keep old guest capability negotiation, the browser-restart response, the pre-dispatch draining signal and the idle shutdown check.

## Share conversation transport and preserve its contracts

- Put SSE framing, bounded stream lifetime and batch delivery in one place. `api/chat.go:235`, `api/conversation_events.go:20` and `api/events.go:25` repeat transport handling. Preserve named legacy events, unnamed conversation events, exact replay limits, cursor precedence, heartbeat payloads and each stream's reset rules. Keep projection and wake repair with the conversation mode.
- Make feature availability part of route dispatch. `api/features.go:9` infers the assistant feature from path prefixes apart from `api/http.go:49` and the unified conversation handlers. Keep the established disabled responses and the internal processing routes. Reuse the user handler's prepared routes while still resolving and releasing the current user scope per request.
- Reuse attachment limits and response handling where the policies match. Keep the upload deadline, metadata checks, content sandbox, legacy route aliases and independent chat limits.

## Give web requests one owner

- Resolve the authenticated runtime target once, and share its relay. `web/src/lib/runtime/{auth,server,forward}.ts` and `web/src/app/api/agent/[operation]/route.ts` assemble the same URL, credentials, trace headers and scope independently. Preserve the local loopback and origin restrictions, verified sign-in, explicit route and media allowlists, response headers and hosted throttling rules.
- Let one chat selection own its requests and merge message pages in one place. In `web/src/lib/chat/useChat.ts:31`, selection and history check for stale work, but late send, create, cancel and delete responses can still update another selection. Preserve client UUIDs, server-side mutations, loaded history and bounded reconnects.
- A stream reset can announce a cursor from before a store restore. Replace the old projection when the cursor moves backward and fence responses from before the replacement. Preserve ambiguous submission IDs for deliberate retry. A user can dismiss a failed local send if the restored store no longer has its attachment.
- Share draft attachment transitions, submission limits and UTF-8 checks. `web/src/lib/chat/useChat.ts:142` rejects file-only input even though the composer enables it and the Go API accepts it. Keep the distinct file type policies. Use the same object URL lifecycle for previews and screenshots so a late response cannot leak a URL.
- Reuse assistant snapshot and pending request reconciliation in `web/src/lib/runtime/useRuntime.ts:301` and `:451`. Centralize client path selection. Remove unused view fields and unreachable presentation branches. Keep delivery acknowledgements tied to viewport exposure and keep read position separate.
- Share the Sentry server and edge policy, preserving the runtime-specific settings and privacy filters.

## Keep release operations and documentation reproducible

- Give each deployment or rehearsal its own Terraform plan directory. `core/infrastructure/release/deploy.py:272` uses shared plan paths across commands, so an unlocked rehearsal can replace the plan a deploy is about to apply. Use the same Terraform output decoder, pass the worker inventory explicitly, and call the existing operator functions directly where the release tool currently shells into its own imported module.
- Keep one validated worker configuration per operator action in `core/infrastructure/app/ops.py:428`. Preserve the ordering of pre-close fences, post-close drain checks, resume reconciliation and rollback fallback.
- Put new-build Brave search bindings on their own configuration. `deploy.py:226` still requires calendar credentials to enable search. Preserve the secret bindings of retained immutable builds.
- Hash the full worker build input. `core/infrastructure/temporal/worker-release.py:22` omits embedded prompts and SQL migrations from its revision fingerprint. Remove the unused execution mode setting and the one-time repair script tied to retired instance metadata.
- Retain independent recovery and bootstrap scripts while they can run against different generations of host images. Keep Terraform state addresses, ingress and credential ownership stable. Verify the resulting build configuration without cloud writes.
- Build the public docs from one parsed page inventory in `docs/publish.py:101`. Put legal pages in the same bundle and keep the private-source boundary explicit. Keep the raw local preview for maintainers. Remove retired calendar, email and schedule contracts from current reference pages while retaining existing docs URLs and historical material where it explains decisions.
- Remove the written acceptance suite and its site-only navigation and styles. `docs/site/scenarios.html` supplies 116 fixtures with setup and expected behavior. Keep the request and task diagrams' illustrative data.
- Bind the local documentation listener before opening it. `docs/serve.sh:6` guesses startup time with a one-second sleep. Remove the roadmap widget that no page loads.
- Keep directives, credentials and deployment examples where the syntax or external contract depends on them. Retain comments about safety, provider constraints, compatibility, timing or otherwise hidden ordering. Remove comments whose information is carried by the code.

## Verification

- Local Postgres and Temporal exercised user isolation, uploads, file-only acceptance, cancellation, SSE replay and reset, and a pinned worker's scoped activity sequence. All 19 registered activity names match the baseline. Firestore compatibility was reviewed against the saved paths and formats.
- A baseline comparison matched tool requests, SSE framing and computer receipt replay. It also exercised the host-only activity route and file transfer through the guest proxy.
- Chromium against a loopback runtime fixture exercised delayed cancel and delete responses, file-only submission, SSE reconnect, backward cursors in both modes, and rejection of cross-origin calls. It also verified that reconnect after a restore did not resend the interrupted task command.
- Offline Terraform validation and operator exercises covered private plan files, retained build settings and pause and resume ordering. The public docs bundle contains the legal pages and excludes private source references and the acceptance suite.
