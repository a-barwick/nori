---
name: wrap-up
description: >-
  Run Nori's full end-of-thread closeout: verify with make check, commit and
  merge to main, push origin/main, deploy core/web to GCP Cloud Run if changed,
  verify /readyz, clean up finished worktrees and branches, and confirm no
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

3. **Deploy to GCP Cloud Run (when `core/`, `web/`, or `contracts/` changed)**
   - Run `./scripts/deploy-gcp.sh` from `main` (use `--core-only` or `--web-only` when only one service changed).
   - Verify the active Cloud Run revisions serve 100% traffic and `nori-core` `/readyz` returns HTTP `200`.

4. **Clean up worktrees, branches, and processes**
   - Remove the finished worktree and delete its merged local branch (`git worktree prune`).
   - Confirm no local dev servers, test DBs, or background worker processes remain running.

5. **Report concisely**
   - Summarize: `main` & `origin/main` commit SHA, deployed Cloud Run revision(s) and `/readyz` status (if deployed), worktree/branch cleanup status, and confirmation that nothing is left running.
