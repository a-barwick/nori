#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "Usage: scripts/ci-status.sh <commit-sha> [--wait] [--exclude-job <name>]"
  echo "Prints success, failure, or pending for the CI checks on a pushed commit (image and release jobs excluded)."
  echo "Exits 0 only on success. --wait polls for up to NORI_CI_WAIT_SECONDS (default 2400)."
}
[[ "$#" -ge 1 && "$1" != -h && "$1" != --help ]] || { usage; exit 2; }
sha="$1"
shift
wait=0
exclude_jobs=("Build images" "Release")
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --wait) wait=1; shift ;;
    --exclude-job)
      [[ "$#" -ge 2 ]] || { usage >&2; exit 2; }
      exclude_jobs+=("$2")
      shift 2
      ;;
    *) usage >&2; exit 2 ;;
  esac
done
exclude_json="$(printf '%s\n' "${exclude_jobs[@]}" | jq -R . | jq -s .)"
repo="${NORI_GITHUB_REPO:-${GITHUB_REPOSITORY:-a-barwick/nori}}"
deadline=$((SECONDS + ${NORI_CI_WAIT_SECONDS:-2400}))

status() {
  local run_id
  run_id="$(gh run list --repo "$repo" --workflow CI --commit "$sha" --event push --limit 1 --json databaseId --jq '.[0].databaseId // empty')"
  if [[ -z "$run_id" ]]; then
    echo pending
    return
  fi
  gh run view "$run_id" --repo "$repo" --json jobs | jq -r --argjson exclude "$exclude_json" '
    [.jobs[] | select(.name as $n | ($exclude | index($n)) | not)] as $checks
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
