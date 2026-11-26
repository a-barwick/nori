#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "Usage: scripts/ci-status.sh <commit-sha> [--wait]"
  echo "Prints success, failure, or pending for the CI checks on a pushed commit (image and release jobs excluded)."
  echo "Exits 0 only on success. --wait polls for up to NORI_CI_WAIT_SECONDS (default 2400)."
}
[[ "$#" -ge 1 && "$1" != -h && "$1" != --help ]] || { usage; exit 2; }
sha="$1"
wait=0
[[ "${2:-}" == --wait ]] && wait=1
repo="${NORI_GITHUB_REPO:-${GITHUB_REPOSITORY:-a-barwick/nori}}"
deadline=$((SECONDS + ${NORI_CI_WAIT_SECONDS:-2400}))

status() {
  local run_id
  run_id="$(gh run list --repo "$repo" --workflow CI --commit "$sha" --event push --limit 1 --json databaseId --jq '.[0].databaseId // empty')"
  if [[ -z "$run_id" ]]; then
    echo pending
    return
  fi
  gh run view "$run_id" --repo "$repo" --json jobs --jq '
    [.jobs[] | select(.name != "Build images" and .name != "Release")] as $checks
    | if ($checks | length) == 0 then "pending"
      elif any($checks[]; (.conclusion // "") != "" and .conclusion != "success") then "failure"
      elif all($checks[]; .conclusion == "success") then "success"
      else "pending" end'
}

while :; do
  result="$(status)"
  if [[ "$result" != pending || "$wait" -eq 0 || "$SECONDS" -ge "$deadline" ]]; then break; fi
  sleep 30
done
echo "$result"
[[ "$result" == success ]]
