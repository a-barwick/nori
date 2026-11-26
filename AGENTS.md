# Nori Project Rules

## 1. Plain-English Communication First
- When explaining bugs, architecture, audits, plans, or swarm status, write in clear plain English (PM / eng-director level) first.
- Avoid dense technical walls or interleaved code snippets and file paths unless the user asks for implementation details.

## 2. UI/UX & Copy Discipline
- Follow `docs/design-system.md` and `ios/DESIGN_SYSTEM.md`.
- Maintain the calm 3-tab (`Chat`, `Work`, `More`) experience and keep Web and iOS in visual and functional parity.
- Never add filler sublabels, redundant tab/page headers, duplicate status badges, negative definitions ("what it's not"), demo/mock modes, or the phrase `"needs you"`.

## 3. Definition of "Wrap Up"
- When the user says "wrap up", "send it", or "good to close thread?", run the full closeout checklist without waiting to be asked:
  1. Run `make check`.
  2. Commit and merge into `main`, then push `origin/main`.
  3. Deploy to GCP Cloud Run (`./scripts/deploy-gcp.sh`) if `core/` or `web/` changed and verify `/readyz` returns HTTP `200`.
  4. Remove finished worktrees and merged local branches.
  5. Confirm no background servers, worker pools, or test processes remain running.
  6. Preserve the user's local `DEVELOPMENT_TEAM` signing setting in `ios/Nori.xcodeproj/project.pbxproj`.

## 4. DSTs Over Unit Tests & Isolated Ephemeral DBs
- Prefer Deterministic Simulation Tests (`core/internal/sim/`, `make sim-deep`, `make sim-pg`) and end-to-end invariants over trivial unit tests, and avoid noisy code comments.
- Always run tests against isolated, ephemeral local Postgres databases—never touch production or the default local `nori` DB—and tear down all test DBs and processes when finished.

## 5. LLM Spend Safety & Provider Hygiene
- Never make raw direct LLM API calls; route all calls through Nori's gateway/client layer with `llmguard` spend caps, per-call timeouts, and retry/loop bounds.
- Never hardcode legacy models (such as `gpt-4.1`); use configurable provider/model and reasoning-effort settings.
- Verify no background workers or test loops are left running that could cause phantom LLM charges.

## 6. Always Ask Interview Questions Before Building
- When asked to interview the user or align on requirements before implementing, ask concise, high-leverage clarifying questions and wait for answers before writing plans or code.

## 7. Pragmatic Scope & Zero Drift
- Focus on the core 90% and log tangential discoveries as follow-ups rather than letting them stall delivery.
- Delete obsolete code, demo modes, and leftover migration artifacts cleanly ("ports, not ships").

## 8. Execute Routine Commands & Verify Before Editing
- Run routine git/worktree, build, and iOS Simulator commands directly (launching the exact variant/appearance requested, e.g., Dark Mode) instead of asking the user to run them.
- Treat bug reports and audit notes as hypotheses—verify them in the live codebase before making edits.
