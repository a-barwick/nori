> AGENTS.md and CLAUDE.md are guidance, not law. Use judgment: follow the intent (keep costs low, keep things simple), and do not let a literal reading block reasonable work or add process. When a rule and good sense conflict, do the sensible thing and say so in one line.

# Model choices are immutable to agents

Austin's model choices are explicit requirements. This restriction overrides the discretion described above and applies to every agent, including subagents and CLI reviewers.

- Never change a selected model, provider, or version without Austin's explicit instruction for that change. This includes defaults, environment settings, launch overrides, infrastructure configuration, and fallback models. Do not switch between Austin's named choices on your own.
- Cost, latency, availability, quality, or a failed tool call does not authorize a model substitution or comparison using other models. An agent's experimental configuration is not an approved model choice.
- Continue solving problems autonomously within the selected models: investigate prompts, integration, concurrency, and implementation. If the selected model itself is a blocker, report the evidence and let Austin decide the model change.

The selected models are:

| Role | Model |
| --- | --- |
| Intent classification | `jev-latest` |
| Outer conversation | Claude Opus 5.5 via the existing OpenRouter adapter, canonical ID `anthropic/claude-opus-5.5` (Austin named `anthropic/opus-5-5`) |
| Inner task worker | `gemini-3.8-flash` via Vertex |
| Implementation subagents | GPT-6.1 Sol, high reasoning |
| CLI code reviewer | Claude Opus 5.5, high reasoning |

# Agent delivery

Use a `codex/*` branch and a pull request for changes. Keep the PR draft while
implementation or independent review is incomplete. Once required verification
and review pass, mark it ready: GitHub automatically merges eligible owner/agent
PRs after `Checks` succeeds for the exact head commit, then deploys from `main`.
Do not add manual merge gates or branch protections for this personal repository.
Do not run a competing local deployment while the `Release` workflow is active.

# GCP cost rules

These apply to this repository's GCP project `bot-111549`. They are cost controls, not design constraints.

- Keep Cloud Run and Agent Runtime minimum instances at zero and maximum instances at one.
- When you finish live work, scale worker pools to zero and stop the computer. Don't leave paid compute running.
- Never delete, replace, or rebuild retained user data (disks, browser profile, files) to save money.
