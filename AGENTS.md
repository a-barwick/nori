# Nori

Follow Austin's current request. Older memory, plans, and skills do not add workflow gates. Complete authorized work without routine go-aheads or mandatory planning, demo, status, or review stages.

Verify the requested behavior proportionally, including the real integration boundary when relevant. Repeat checks only for changed evidence or unresolved failures. Fix blockers; put nonblocking findings in Linear Backlog with evidence and why they can wait. Use related links, not blocking dependencies; do not implement follow-ups without authorization.

Use a `codex/*` branch and PR; merge completed, verified work without an extra approval round. Use a build or type check appropriate to the changed code. No mandatory test, demo, or review cycle. Deploy only when requested, with `make deploy` (see `core/infrastructure/README.md`). Model/CLI/external reviews require an explicit request; reviewer selection alone does not require a review.

## Threads

- One thread writes one deliverable. Start subagents or message other threads only when Austin asks in this thread.
- A stop covers the action Austin named. Do not widen it into removing a check, changing a pipeline, or cancelling a running deployment without asking.

## Looking things up

Search the code directly. `docs/agent_map.py` is optional navigation, not a prerequisite for reading or editing files.

## Documentation

Nori's documentation lives in `docs/site/`. Update it when requested or when an existing instruction becomes wrong because of the change. Keep writing concise and grounded in the code. The writing skills are optional guidance. No separate review, screenshots, style checker, commit trailers, or review checkpoints are required. Publish only when requested.

## Marketing film

The Nori film and how to edit or reshoot it live in `demo/`. Read `demo/README.md` before any film work.

## Model choices

Only Austin may authorize changing these models, providers, or versions, including defaults, launch overrides, experiments, and fallbacks. Cost, latency, availability, or failure does not authorize substitution.

| Role | Model |
| --- | --- |
| Intent classification | `jev-latest` |
| Outer conversation | GPT-6.1 Sol via OpenAI platform API, `gpt-6.1-sol`, low reasoning (standard tier) |
| Inner task worker | GPT-6.1 Terra via OpenRouter Responses, `openai/gpt-6.1-terra` |
| Implementation subagents (developing Nori, not product workers) | GPT-6.1 Sol, high reasoning |
| CLI code reviewer (only when requested) | Claude Opus 5.5, medium reasoning |

## Secrets, data, and cost

- Keep credentials in the main checkout's gitignored, mode-600 root `.env`; never print values, copy credentials into worktrees or other projects, or use 1Password. Ask for missing credentials. Keep management credentials out of child processes. Server env files stay outside the repo, root-owned and mode 600.
- Preserve retained disks, browser profiles, files, history, pending user tasks, and unrelated work. Do not delete or rebuild user data for cleanup or cost savings.
- Use the project's ordinary model keys. No development allowance wrapper, temporary key issuance, per-chat grants, or shared reservation ledger is required. Keep usage reporting and the configured task spending ceiling. Missing cost data stays unknown and does not invalidate a successful result.
- In GCP project `bot-111549`, keep Cloud Run and Agent Runtime minimum instances at zero and maximum at one. After live work, scale worker pools to zero and stop the computer without interrupting unrelated user work or deleting retained data.
