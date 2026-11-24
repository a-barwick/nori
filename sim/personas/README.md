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

### 3. Run real-LLM simulations after a feature launch

Configure model keys in `.env` (or export `OPENAI_API_KEY`, `OPENROUTER_API_KEY`, and optionally `SIM_RESPONDER_API_KEY` / `SIM_JUDGE_API_KEY`, which default to `OPENAI_API_KEY` or `OPENROUTER_API_KEY`):

```bash
# Fast 1-persona sanity check (~2-4 min)
python3 scripts/persona-sim/runner.py --preset smoke

# 3 memory/compaction-heavy personas (~8-10 min)
python3 scripts/persona-sim/runner.py --preset memory-pack

# Full 10-persona × 30-day suite (~15-20 min with --concurrency 3)
python3 scripts/persona-sim/runner.py --preset full --concurrency 3

# Grade a completed run and compare against a previous baseline
python3 scripts/persona-sim/grader.py .sim-runs/<run-dir> --compare .sim-runs/<baseline-dir>/scorecard.json
```

### 4. Visual QA in the local Next.js Workbench (`http://localhost:3210`)

Replay one persona against a dedicated test user in your local dev stack (never touches production):

```bash
python3 scripts/persona-sim/runner.py --persona 01-maya-lin --workbench-seed --target-url http://127.0.0.1:8080
```

## Safety Guarantees

- `cmd/nori-sim` refuses to start unless `NORI_CLOUD=local` and the Postgres URL points to loopback.
- By default, `runner.py` creates and drops its own ephemeral Postgres database (`nori_sim_<random>`) on `127.0.0.1:15432` (or starts an isolated Compose stack) and never modifies production or the default `nori` database.
- All external tool calls target the local in-memory `mcp_sandbox.py` server — zero external emails, calendar invites, or API calls ever leave localhost.
