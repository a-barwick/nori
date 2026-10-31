---
name: verification-monitor
description: Independently bound stabilization and release verification when Austin explicitly requests a monitor chat. Not for ordinary edits.
---

# Verification monitor

Use only when Austin requests creating a monitor chat and exchanging verification checkpoints with it. Follow AGENTS.md for model selection and permissions. Discover the available thread tools; do not substitute a subagent or claim monitoring is active if the required tools are unavailable.

This is cooperative enforcement, not a tool-level kill switch. The worker must wait for each decision. The monitor cannot grant permissions or override Austin.

## Establish the boundary

Before post-implementation verification, create one monitor chat with these instructions and:

- The requested outcome and concrete acceptance criteria.
- Implemented changes and existing verification evidence.
- Remaining checks, authorized deployment scope, and spend limits.
- Known limitations that can be deferred.

The monitor maintains its own cycle count and decision history in that chat. It must not expand acceptance criteria. Its work is read-only: inspect relevant diffs, results, or state; do not edit code, run tests, deploy, or delegate.

## Require a checkpoint

Before each new fix/check/deploy cycle, the worker sends:

1. Last action and observed result, with evidence references.
2. The specific acceptance criterion still unmet.
3. Exact proposed next action, expected information gained, and time/cost bound.
4. Why existing evidence is insufficient.

Use one numbered checkpoint per bounded cycle. The monitor counts attempts by the underlying issue, not the worker's labels. Send the initial checkpoint before the first cycle. Read the monitor's completed response before proceeding; silence, timeout, an unavailable monitor, or a stale decision is not approval. Report an unavailable monitor to Austin without starting another cycle.

## Monitor decisions

Return one decision for the current checkpoint, with a short reason and the running count for that issue:

- **CONTINUE:** Authorize only the proposed bounded cycle addressing an unmet criterion.
- **DEFER:** Record the finding and proceed without investigating it further. Any other cycle needs its own checkpoint.
- **STOP:** End autonomous verification and wrap up.

Inspect relevant evidence when needed; do not rely solely on the worker's narrative. Judge whether another cycle is justified, rather than conducting another broad code review.

Stop or defer when:

- A check would repeat without changed evidence.
- A new finding does not block the agreed outcome or present a concrete safety or data-loss risk.
- Two cycles address the same issue without meaningful progress.
- The agreed outcome is demonstrated, or an authorized limit is reached.

A newly discovered genuine blocker may justify continuation, but not unlimited investigation. Surface consequential decisions to Austin.

## Honor the decision

After STOP, perform only necessary safe cleanup, preserve work and evidence, update tickets, and report results and limitations. Do not blindly cancel an in-flight deployment or interrupt unrelated work.

The worker cannot override the monitor, replace it to obtain approval, or reframe the same investigation as a new issue. Only Austin can reopen it. Do not restart verification during wrap-up.
