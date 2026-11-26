---
name: wrap-up
description: >-
  Run Nori's full end-of-thread closeout: verify with make check, commit and
  merge to main, push origin/main, confirm the Release workflow shipped any core/web changes, clean up finished worktrees and branches, and confirm no
  background processes remain. Use when the user says "wrap up", "send it",
  "merge and deploy", or "good to close thread?".
---

# Wrap Up & Deploy

Execute Nori's complete closeout checklist in order.

## Steps

1. **Verify changes**
   - Check `git status` and `git diff --stat`.
   - Ensure the user's local `DEVELOPMENT_TEAM` signing setting in `ios/Nori.xcodeproj/project.pbxproj` is preserved (do not overwrite or revert it unintentionally).
   - Run `make check` and resolve any failures.

2. **Commit, merge to `main`, and push**
   - Commit any remaining changes on the current worktree branch with a clear commit message.
   - Fast-forward or cleanly merge the branch into `main` in the primary repo, then push `origin/main`.

3. **Confirm the release (when `core/`, `web/`, or `contracts/` changed)**
   - The Release workflow ships the newest green `main` commit every three hours. To ship now, run `gh workflow run release.yml` after CI passes, and confirm the run succeeded (staging, checks, bake).
   - If the release reverted, read its log, fix forward, and push again.

4. **Clean up worktrees, branches, and processes**
   - Remove the finished worktree and delete its merged local branch (`git worktree prune`).
   - Confirm no local dev servers, test DBs, or background worker processes remain running.

5. **Report concisely**
   - Summarize: `main` & `origin/main` commit SHA, the Release workflow result (if core/web changed), worktree/branch cleanup status, and confirmation that nothing is left running.
