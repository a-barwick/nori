# Nori Real-Life Persona Simulation Suite

Real-life, nondeterministic month-long user simulations that exercise Nori with **real LLMs**, **real Postgres 18** (`nori_records`, `nori_memory_chunks` GIN FTS, `nori_task_leases`, `nori_user_wakes`), **real file uploads**, **out-of-band UI actions**, and a **stateful sandbox MCP world**.

Whereas Nori's DSTs (`core/internal/sim`) prove deterministic state-machine and fault-injection invariants, this suite validates **semantic and behavioral quality** after major feature launches (such as Coupled 3-Tier Memory & Compaction, AutoDream, FTS recall, prompt-injection defense, automations, and goals).

## Directory Layout

- `sim/personas/schema/transcript.schema.json` — JSON Schema for `profile.json` and `month.json`.
- `sim/personas/01-maya-lin/` through `10-jordan-reyes/` — 10 complete persona bundles:
  - `persona.md` — Narrative bible, writing style guide, relationships, and 30-day arc.
  - `profile.json` — Initial user config, preferences, permissions, MCP world seed, and Adaptive Responder brief.
  - `month.json` — Chronological Day 1–30 simulation script (45–65 events + 10–15 embedded probes + end-of-month audit).
  - `assets/` — Real synthetic uploads (`.pdf`, `.csv`, `.ics`, `.png`, `.txt`, `.md`).
- `core/cmd/nori-sim/` — Local-only virtual-clock Nori server wrapping real `app.Service`, `worker.Pool`, and Postgres with `/_sim/*` control, drain, state snapshot, and deterministic `INV-MEM-01..08` invariant checks.
- `scripts/persona-sim/` — Validator, stateful MCP sandbox server, runner with adaptive in-character responder, and hybrid deterministic + LLM-judge grader.

## Quick Start

### 1. Validate all 10 personas and assets (offline, no API keys needed)

```bash
python3 scripts/persona-sim/validate.py
```

### 2. Dry-run the full simulation harness with the local model stub (free, no API keys needed)

```bash
python3 scripts/persona-sim/runner.py --preset smoke --stub-models --max-events 12
```

### 3. Run model-backed simulations

Per project policy (`AGENTS.md` Rule 2), use local **Inkling Mini 2** (`NORI_LOCAL_MODEL_URL=http://127.0.0.1:11434`, `NORI_LOCAL_MODEL_TAG=inkling-mini-2`) for routine model-backed local development and tests. Use cloud LLMs (`OPENAI_API_KEY`, `OPENROUTER_API_KEY`, and optionally `SIM_RESPONDER_API_KEY` / `SIM_JUDGE_API_KEY`) only when explicitly running a cloud-model evaluation or baseline comparison:

```bash
# Fast 1-persona sanity check (~2-4 min)
python3 scripts/persona-sim/runner.py --preset smoke

# Quick 1-week check across the 5 highest-signal core personas (low reasoning, ~$1-2 on cloud)
make sim-personas-quick

# Full-month 5-persona high-signal core suite (~$5-8 on cloud)
make sim-personas-core

# 3 memory/compaction-heavy personas (~8-10 min)
python3 scripts/persona-sim/runner.py --preset memory-pack

# Full 10-persona × 30-day suite: one isolated runner + DB per persona, graded together
# (~45-60 min at the default --parallel 4); add --weeks 1|2|3 or --max-days N to shorten
python3 scripts/persona-sim/suite.py --preset full --tag full
make sim-personas-full BASELINE=.sim-runs/<baseline-dir>/scorecard.json   # same, plus a diff

# Re-run a few personas, or merge existing bundles into one graded run
python3 scripts/persona-sim/suite.py --persona 03-priya-subramanian --tag retry
python3 scripts/persona-sim/suite.py --collect-only .sim-runs/<a>/01-maya-lin .sim-runs/<b>/02-devon-brooks --tag merged

# Grade a completed run and compare against a previous baseline
python3 scripts/persona-sim/grader.py .sim-runs/<run-dir> --compare .sim-runs/<baseline-dir>/scorecard.json
```

Each persona in `runner.py` and `suite.py` receives its own isolated ephemeral Postgres database (`nori_sim_<random>`). Prefer `suite.py` for multi-persona runs: it staggers starts (`--stagger`, default 45 s) so concurrent schema migrations don't exhaust Postgres's lock table (`out of shared memory`), enforces spend/call caps (`--max-usd`, `--max-calls`), and re-runs the judge for retryable errors.

**Before you run:**

- Local Postgres must be up on `127.0.0.1:15432` (`make deps`).
- Strict BYOK applies: local simulation runs use either `--stub-models` (deterministic offline stubs) or a loopback local OpenAI-compatible server (e.g., Inkling Mini 2 via `NORI_LOCAL_MODEL_URL`). Operator cloud-LLM env vars are stripped automatically.
- Keep the laptop awake with the lid open. Sleep mid-run corrupts results.

**Where results go:** everything is written under `.sim-runs/` at the repo root. It's git-ignored, so results stay on your machine.

```
.sim-runs/<YYYYMMDD-HHMMSS>-<tag>/          # the graded run: this is what you read and diff against
  scorecard.md                              # human-readable report: per-persona table + every FAIL/PARTIAL with the judge's reason
  scorecard.json                            # same data, machine-readable; pass it to --compare / BASELINE=
  suite_manifest.json                       # which personas ran, which failed/were excluded, and where each came from
  _logs/<slug>.log                          # runner console output per persona (progress, crashes)
  <slug>/                                   # one bundle per persona:
    summary.json                            #   events run, HTTP 5xx count, runtime
    probes.json                             #   each probe: user message, Nori's reply, memory/FTS evidence
    trace.jsonl                             #   every event in order, with Nori's reply and any UI-action result
    nori-sim.log                            #   Nori's own server log (ERROR/WARN lines, model calls, 402/403s)
    mcp_audit.json                          #   every tool call into the mock email/calendar/docs world, incl. writes
    snapshots/*.json                        #   memory state + invariant checks at each weekly checkpoint
.sim-runs/<YYYYMMDD-HHMMSS>-<tag>-<slug>/   # raw per-persona run dirs suite.py collected from (safe to delete)
```

**Reading a run:**

1. Start with `scorecard.md`. Tier A is hard invariants and must all pass; Tier B is the judge's score per probe.
2. For anything that failed, open that persona's `probes.json` (what Nori said) and `nori-sim.log` (errors), then `trace.jsonl` for the surrounding events.
3. Record confirmed issues in `docs/vst-findings.md`. That's the hand-maintained backlog; `scorecard.md` is regenerated on every run.

The current baseline to diff against is listed at the top of `docs/vst-findings.md`. After a clean full run on current `main`, point `BASELINE=` at that run's `scorecard.json` instead.

### 4. Visual QA in the local Next.js Workbench (`http://localhost:3210`)

Replay one persona against a dedicated test user in your local dev stack (never touches production):

```bash
python3 scripts/persona-sim/runner.py --persona 01-maya-lin --workbench-seed --target-url http://127.0.0.1:8080
```

## Safety Guarantees

- `cmd/nori-sim` refuses to start unless `NORI_CLOUD=local` and the Postgres URL points to loopback.
- By default, `runner.py` creates and drops its own ephemeral Postgres database (`nori_sim_<random>`) on `127.0.0.1:15432` (or starts an isolated Compose stack) and never modifies production or the default `nori` database.
- All external tool calls target the local in-memory `mcp_sandbox.py` server — zero external emails, calendar invites, or API calls ever leave localhost.
