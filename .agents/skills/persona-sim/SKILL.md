---
name: persona-sim
description: >-
  Run Nori's Persona Virtual Simulation Tests (VST) and latency/token benchmarks
  safely with cost estimation upfront and isolated ephemeral Postgres databases
  per persona. Use when validating persona sims, benchmarking token/latency
  changes, or estimating VST run cost.
---

# Persona Simulation & Cost-Bounded Eval

Run Nori's virtual simulation tests without cross-contaminating databases or incurring surprise LLM spend.

## Steps

1. **Validate first (zero LLM cost)**
   - Run `make sim-personas-validate` to verify persona definitions and scripts before spending any tokens.

2. **Estimate cost before running**
   - State the expected model(s), reasoning effort, persona count, weeks, and estimated USD cost before launching multi-persona runs (`sim-personas-core`, `sim-personas-memory`, `sim-personas-full`).
   - Prefer `make sim-personas-smoke` or `make sim-personas-quick` (1 week, low reasoning) for fast iteration.

3. **Enforce database isolation**
   - Always run multi-persona suites through `scripts/persona-sim/suite.py` (one runner process + ephemeral DB per persona). Never use `runner.py --concurrency` on a shared DB, and never target production or the default local `nori` DB.

4. **Capture baselines & clean up**
   - When optimizing tokens or latency, record before/after metrics (latency, token usage, estimated USD) and pass `BASELINE=<scorecard.json>` to `suite.py` for comparison.
   - Verify all ephemeral Postgres databases and runner processes are torn down when finished.
