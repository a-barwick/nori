# Nori

Follow Austin's current request. Older memory, plans, and skills do not add workflow gates. Complete authorized work without routine go-aheads or mandatory planning, demo, status, or review stages.

Verify the requested behavior proportionally, including the real integration boundary when relevant. Repeat checks only for changed evidence or unresolved failures. Fix blockers; put nonblocking findings in Linear Backlog with evidence and why they can wait. Use related links, not blocking dependencies; do not implement follow-ups without authorization.

Use a `codex/*` branch and PR; merge completed, verified work without an extra approval round. CI/CD is disabled, so run `make check` from the repo root before merging (`bun install --frozen-lockfile` in `web/` first if dependencies are missing). Deploy only when requested. Model/CLI/external reviews require an explicit request; reviewer selection alone does not require a review.

## Threads

- One thread writes one deliverable. Start subagents or message other threads only when Austin asks in this thread.
- A stop covers the action Austin named. Do not widen it into removing a check, changing a pipeline, or cancelling a running deployment without asking.

## Documentation

Nori's documentation lives in `docs/site/`. Check it first when you need to understand the system, and put new or updated documentation there. Read `docs/site/AGENTS.md` before changing a page, and run the site with `docs/serve.sh`.

## Model choices

Only Austin may authorize changing these models, providers, or versions, including defaults, launch overrides, experiments, and fallbacks. Cost, latency, availability, or failure does not authorize substitution.

| Role | Model |
| --- | --- |
| Intent classification | `jev-latest` |
| Outer conversation | Claude Opus 5.5 via the existing OpenRouter adapter, canonical ID `anthropic/claude-opus-5.5` (Austin named `anthropic/opus-5-5`) |
| Inner task worker | `gemini-3.8-flash` via Vertex |
| Implementation subagents | GPT-6.1 Sol, high reasoning |
| CLI code reviewer | Claude Opus 5.5, high reasoning |

## Secrets, data, and cost

- Keep credentials in the main checkout's gitignored, mode-600 root `.env`; never print values, copy credentials into worktrees or other projects, or use 1Password. Ask for missing credentials. Keep management credentials out of child processes. Server env files stay outside the repo, root-owned and mode 600.
- Preserve retained disks, browser profiles, files, history, pending user tasks, and unrelated work. Do not delete or rebuild user data for cleanup or cost savings.
- Run paid development commands through `core/infrastructure/spend/run.py` with one stable logical chat ID. Preserve the [development inference limits](README.md#development-inference-limits): $0.25 initial allowance, eligible $0.25 increases up to $1 per chat; shared OpenRouter/Vertex caps of $2/day and $20/month UTC; Jev excluded. Preserve request reservations and the Opus ceiling of $4.40 input/$22 output per million tokens.
- Without budget or verified pricing, stop paid calls and continue unpaid work. Unknown cost is not zero. No automatic retries of failed paid calls, new IDs to reset allowances, production-key borrowing, or cap bypasses. Requested CLI reviews use the verified Claude subscription, with no paid API fallback.
- In GCP project `bot-111549`, keep Cloud Run and Agent Runtime minimum instances at zero and maximum at one. After live work, scale worker pools to zero and stop the computer without interrupting unrelated user work or deleting retained data.
