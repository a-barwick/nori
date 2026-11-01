# Nori

Follow Austin's current request. Older memory, plans, and skills do not add workflow gates. Complete authorized work without routine go-aheads or mandatory planning, demo, status, or review stages.

Verify the requested behavior proportionally, including the real integration boundary when relevant. Repeat checks only for changed evidence or unresolved failures. Fix blockers; put nonblocking findings in Linear Backlog with evidence and why they can wait. Use related links, not blocking dependencies; do not implement follow-ups without authorization.

Use a `codex/*` branch and PR; merge completed, verified work without an extra approval round. CI/CD is disabled, so run `make check` from the repo root before merging (`bun install --frozen-lockfile` in `web/` first if dependencies are missing). Deploy only when requested, with `make deploy` (see `core/infrastructure/README.md`). Model/CLI/external reviews require an explicit request; reviewer selection alone does not require a review.

## Threads

- One thread writes one deliverable. Start subagents or message other threads only when Austin asks in this thread.
- A stop covers the action Austin named. Do not widen it into removing a check, changing a pipeline, or cancelling a running deployment without asking.

## Monitored verification

When Austin requests monitored verification, use the [verification-monitor](.agents/skills/verification-monitor/SKILL.md) skill. Establish the acceptance scope before verification begins. The monitor may stop further autonomous verification; only Austin may override that stop.

Ordinary edits do not require a monitor. Monitoring does not authorize additional scope, spending, deployments, or model changes.

## Looking things up

To look something up, run `python3 docs/agent_map.py find "words"` first. It prints matching doc sections as plain text with their code paths. `docs/agent-map.json` lists every page and section. Read the code it names when you need detail, and avoid reading the whole site. Before changing a file, or to find its docs, run `python3 docs/agent_map.py code PATH`. It lists the pages that describe that file. If its output says STALE, the code changed after the page was reviewed. Check the claim against the code it names, because the code decides what's true.

## Documentation

Nori's documentation lives in `docs/site/`. Check it first when you need to understand the system, and put new or updated documentation there.

Write and review a feature's docs after its code is merged, as one change describing what's on `main`. Don't write `designed` pages first and mark them built later.

**Every change to a page in `docs/site/` must follow these steps. A small edit, a status update, a fact fix and a page Austin asked for quickly all count.**

1. Before you edit a page, load and follow the `write-docs` skill in `.agents/skills/`, which also requires `plain-writing`. Do this first, not after the edit.
2. Before you commit, a new thread or subagent that didn't write the change runs the `review-docs` skill on each changed page and returns PASS. Austin requires this review, so the rules above and below about requesting reviews and starting subagents don't apply to it.
3. Add these lines to the commit message, and only if both are true:

   ```text
   Docs-Skills: write-docs, plain-writing
   Docs-Review: PASS, review-docs in a separate thread
   ```

4. Run `docs/deploy.sh` to publish only after those steps.

Each changed page must pass `check.py` completely, including findings that were there before your edit. The commit hook, `make check` and `docs/deploy.sh` enforce this. Don't bypass them with `--no-verify`, and don't change their rules or limits to get a page through. If you find you skipped a step, say so, then do it. Only Austin can waive a rule.

A change gets one full review and at most two rechecks, and the author gives the reviewer only the page paths, plus the earlier report and diff for a recheck. If the last recheck rejects, stop and give Austin the reports. A waiver from Austin goes in the commit as `Docs-Waived: Austin said "..."` in place of the `Docs-Review` line.

Read `docs/site/AGENTS.md` for the rules, and run the site with `docs/serve.sh`.

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
- Run paid development commands through `core/infrastructure/spend/run.py` with one stable logical chat ID. Preserve the [development inference limits](README.md#development-inference-limits): $0.25 initial allowance, eligible $0.25 increases up to $1 per chat; shared model caps of $2/day and $20/month UTC; Jev excluded. Preserve request reservations and per-million-token price ceilings: Sol $2 input/$10 output ($0.10 cached input, explicit cache mode); Terra $1 input/$5 output.
- Without budget or verified pricing, stop paid calls and continue unpaid work. Unknown cost is not zero. No automatic retries of failed paid calls, new IDs to reset allowances, production-key borrowing, or cap bypasses. Requested CLI reviews use the verified Claude subscription, with no paid API fallback.
- In GCP project `bot-111549`, keep Cloud Run and Agent Runtime minimum instances at zero and maximum at one. After live work, scale worker pools to zero and stop the computer without interrupting unrelated user work or deleting retained data.
