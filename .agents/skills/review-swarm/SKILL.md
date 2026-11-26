---
name: review-swarm
description: >-
  Coordinate staged parallel implementation with strict disjoint file ownership,
  Deterministic Simulation Test (DST) verification, and an independent high-rigor
  code review pass (e.g., Opus 5.5) before merging. Use for medium-to-large
  features, refactors, or multi-bug remediation passes.
---

# Staged Implementation & Independent Code Review

Execute multi-part engineering work cleanly without merge conflicts, scope drift, or unverified fixes.

## Steps

1. **Verify scope & partition cleanly**
   - Treat reported bugs or audit items as hypotheses—verify each in the codebase first.
   - Split work into stages with **strict disjoint file ownership** per worker so parallel edits merge without conflicts.
   - Give workers clear goals and verification criteria without micromanaging implementation details.

2. **Require DST & check verification**
   - Verify behavioral changes with Deterministic Simulation Tests (`core/internal/sim/`, `make sim-deep`, `make sim-pg`) and `make check` rather than trivial unit tests.
   - Keep scope focused on the core 90%; log tangential findings as follow-up notes instead of stalling the stage.

3. **Run an independent code review pass**
   - Before merging to `main`, run an independent code review (e.g., via Opus 5.5 in `/swarm` or CLI review if requested) checking correctness, edge cases, spend safety, and DST coverage.
   - Address any blockers and re-run `make check`.

4. **Give human-readable status updates**
   - Keep progress updates brief and in plain English: what's done, what's in flight, rough completion percentage, and any real blockers.
