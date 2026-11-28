#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT="${GCP_PROJECT:-bot-111549}"
REGION="${GCP_REGION:-us-central1}"
REGISTRY="${REGION}-docker.pkg.dev/${PROJECT}/${GCP_AR_REPO:-nori-poc}"
REPO_SLUG="${NORI_GITHUB_REPO:-a-barwick/nori}"
MIGRATION_JOB="${NORI_GCP_MIGRATION_JOB:-nori-migrate}"
BAKE_SECONDS_SET="${NORI_RELEASE_BAKE_SECONDS+set}"
BAKE_INTERVAL_SET="${NORI_RELEASE_BAKE_INTERVAL+set}"
BAKE_SECONDS="${NORI_RELEASE_BAKE_SECONDS:-600}"
BAKE_INTERVAL="${NORI_RELEASE_BAKE_INTERVAL:-30}"

usage() {
  cat <<'USAGE'
Release Nori Core, Web and the public site to Cloud Run. Scheduled Release runs use
--latest-green every three hours; manual workflow_dispatch runs release main HEAD by
default and fail if HEAD is not green and built.

Usage:
  scripts/release.sh [--latest-green] [--quick] [--dry-run] [--plan-groups] [--only] [--core] [--web] [--site] [--force-unverified]
  scripts/release.sh rollback core|web|site|all

Releases HEAD (or NORI_RELEASE_SHA) using the images CI built for that commit.
--latest-green instead picks the newest commit on main whose CI checks passed and whose
core and web images exist.
--quick skips the Deep Deterministic Simulation CI gate and uses a 60s bake by default
(instead of 600s).
--plan-groups prints the groups that would ship for HEAD (one per line) and exits without
checking CI or Artifact Registry images.
--only restricts the release to the explicitly requested --core / --web / --site groups
instead of also shipping other changed groups.
Only groups whose code changed since the live release ship; --core / --web / --site force a group.
  core: nori-migrate job, then nori-core (API + worker)
  web:  nori-web and nori-web-mac (same image)
  site: nori-site (public marketing site). Skipped when the commit has no nori-site image
        or the service doesn't exist yet.
Each service in every releasing group is staged with no traffic and must pass its HTTP
startup probe and checks before any group is promoted. Promoted revisions are then
watched for NORI_RELEASE_BAKE_SECONDS (default 600, or 60 with --quick); any 5xx, crash,
or application error log reverts that group to its previous revision.
USAGE
}

log() { printf '==> %s\n' "$*"; }
die() { printf 'release: %s\n' "$*" >&2; exit 1; }
DRY_RUN=0
run() {
  printf '+ %s\n' "$*"
  if [[ "$DRY_RUN" -eq 0 ]]; then "$@"; fi
}
cloud_run() { gcloud run "$@" --project="$PROJECT" --region="$REGION"; }

group_services() {
  case "$1" in
    core) echo "nori-core" ;;
    web) echo "nori-web nori-web-mac" ;;
    site) echo "nori-site" ;;
  esac
}
group_paths() {
  case "$1" in
    core) echo "core api contracts" ;;
    web) echo "web api contracts" ;;
    site) echo "site" ;;
  esac
}
group_image() {
  case "$1" in
    core) echo "$REGISTRY/nori-core:$SHA" ;;
    web) echo "$REGISTRY/nori-web:$SHA" ;;
    site) echo "$REGISTRY/nori-site:$SHA" ;;
  esac
}
probe_path() {
  case "$1" in
    nori-core) echo /readyz ;;
    nori-site) echo /healthz ;;
    *) echo /api/health ;;
  esac
}
group_of() {
  case "$1" in
    nori-core) echo core ;;
    nori-site) echo site ;;
    *) echo web ;;
  esac
}

service_json() { cloud_run services describe "$1" --format=json; }
revision_json() { cloud_run revisions describe "$1" --format=json; }

serving_revision() {
  service_json "$1" | jq -r '[.status.traffic[]? | select((.percent // 0) > 0)] | if length == 1 then .[0].revisionName else empty end'
}
revision_label() {
  revision_json "$1" | jq -r --arg key "$2" '.metadata.labels[$key] // empty'
}
revision_ready() {
  revision_json "$1" | jq -e 'any(.status.conditions[]?; .type == "Ready" and .status == "True")' >/dev/null
}
# Exit status: 0 if the service exists, 1 if it doesn't, 2 if Cloud Run couldn't be listed.
service_exists() {
  local names
  names="$(cloud_run services list --filter="metadata.name=$1" --format='value(metadata.name)')" || return 2
  grep -qx -- "$1" <<< "$names"
}
image_exists() {
  gcloud artifacts docker images describe "$1" --project="$PROJECT" --format='value(image_summary.digest)' >/dev/null 2>&1
}
tagged() {
  service_json "$1" | jq -r --arg tag "$2" '.status.traffic[]? | select(.tag == $tag) | "\(.revisionName)\t\(.url)"'
}
http_status() {
  curl --silent --show-error --output /dev/null --write-out '%{http_code}' --max-time 15 --retry 5 --retry-all-errors "$@" || true
}

rollback() {
  local group="$1" services svc current previous
  case "$group" in
    core|web|site) services="$(group_services "$group")" ;;
    all) services="$(group_services core) $(group_services web) $(group_services site)" ;;
    *) usage >&2; exit 2 ;;
  esac
  for svc in $services; do
    if [[ "$group" == all && "$svc" == nori-site ]] && ! service_exists "$svc"; then
      log "Skipping $svc: it doesn't exist."
      continue
    fi
    current="$(serving_revision "$svc")"
    [[ -n "$current" ]] || die "$svc has split traffic; set it to one revision first."
    previous="$(revision_label "$current" nori-prev)"
    [[ -n "$previous" ]] || die "$svc revision $current has no recorded previous revision."
    log "Rolling $svc back from $current to $previous"
    run cloud_run services update-traffic "$svc" --to-revisions="$previous=100" --quiet
    revision_ready "$previous" || die "$previous is not Ready after rollback."
  done
}

FORCE_CORE=0
FORCE_WEB=0
FORCE_SITE=0
ONLY_FORCED=0
PLAN_GROUPS=0
FORCE_UNVERIFIED=0
LATEST_GREEN=0
QUICK=0
if [[ "${1:-}" == rollback ]]; then
  [[ "$#" -eq 2 ]] || { usage >&2; exit 2; }
  rollback "$2"
  exit 0
fi
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --core) FORCE_CORE=1 ;;
    --web) FORCE_WEB=1 ;;
    --site) FORCE_SITE=1 ;;
    --only) ONLY_FORCED=1 ;;
    --plan-groups) PLAN_GROUPS=1 ;;
    --force-unverified) FORCE_UNVERIFIED=1 ;;
    --latest-green) LATEST_GREEN=1 ;;
    --quick) QUICK=1 ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done

if [[ "$ONLY_FORCED" -eq 1 && "$FORCE_CORE" -eq 0 && "$FORCE_WEB" -eq 0 && "$FORCE_SITE" -eq 0 ]]; then
  die "--only requires at least one of --core, --web, or --site."
fi

if [[ "$QUICK" -eq 1 ]]; then
  [[ -n "$BAKE_SECONDS_SET" ]] || BAKE_SECONDS=60
  [[ -n "$BAKE_INTERVAL_SET" ]] || BAKE_INTERVAL=15
fi

cd "${NORI_RELEASE_REPO:-$SCRIPT_DIR/..}"
CI_STATUS="${NORI_CI_STATUS_CMD:-$SCRIPT_DIR/ci-status.sh}"
MAIN_REF=HEAD
if [[ "${GITHUB_ACTIONS:-}" != true ]]; then
  git fetch --quiet origin main
  MAIN_REF=origin/main
fi

ci_green() {
  [[ "${NORI_RELEASE_CI_GATED:-}" == 1 ]] && return 0
  local exclude=(--exclude-job "Swift package and iOS app")
  if [[ "$QUICK" -eq 1 ]]; then
    exclude+=(--exclude-job "Deep Deterministic Simulation")
  fi
  NORI_GITHUB_REPO="$REPO_SLUG" "$CI_STATUS" "$1" "${exclude[@]}" >/dev/null
}
# The site image is optional: commits from before site/ existed, or whose site build
# failed, still release core and web.
images_exist() {
  local name
  for name in nori-core nori-web; do
    image_exists "$REGISTRY/$name:$1" || return 1
  done
}

FALLBACK_FROM=""
if [[ "$LATEST_GREEN" -eq 1 ]]; then
  HEAD_SHA="$(git rev-parse "$MAIN_REF")"
  SHA=""
  for candidate in $(git rev-list --topo-order -n "${NORI_RELEASE_SEARCH_DEPTH:-30}" "$MAIN_REF"); do
    if ci_green "$candidate" && images_exist "$candidate"; then
      SHA="$candidate"
      break
    fi
  done
  if [[ -z "$SHA" ]]; then
    log "No recent commit on main has passing CI and built images. Nothing to release."
    exit 0
  fi
  if [[ "$SHA" != "$HEAD_SHA" ]]; then
    FALLBACK_FROM="$HEAD_SHA"
    printf 'WARNING: main HEAD %s is not green/built; falling back to %s.\n' "${HEAD_SHA:0:12}" "${SHA:0:12}" >&2
  fi
else
  SHA="${NORI_RELEASE_SHA:-$(git rev-parse HEAD)}"
fi
[[ "$SHA" =~ ^[0-9a-f]{40}$ ]] || die "release commit must be a full SHA (got '$SHA')."
TAG="release-${SHA:0:12}"

if [[ "${GITHUB_ACTIONS:-}" != true ]]; then
  git merge-base --is-ancestor "$SHA" origin/main || die "$SHA is not on origin/main; push it first."
  [[ -z "$(git status --porcelain -- core web api contracts site)" ]] || die "uncommitted changes under core/, web/, api/, contracts/, or site/."
fi

if [[ "$LATEST_GREEN" -eq 1 || "$PLAN_GROUPS" -eq 1 ]]; then
  :
elif [[ "$FORCE_UNVERIFIED" -eq 1 ]]; then
  printf 'WARNING: releasing %s without verifying CI.\n' "$SHA" >&2
elif ! ci_green "$SHA"; then
  die "CI has not passed on $SHA. Fix it, or pass --force-unverified in an emergency."
fi

group_change() {
  local group="$1" svc revision live paths exists
  for svc in $(group_services "$group"); do
    revision="$(serving_revision "$svc")"
    if [[ -z "$revision" ]]; then
      exists=0
      service_exists "$svc" || exists=$?
      case "$exists" in
        0) echo "changed: $svc has split traffic" ;;
        1) echo "missing: $svc does not exist yet (see core/infrastructure/README.md)" ;;
        *) echo "unknown: could not list Cloud Run services" ;;
      esac
      return
    fi
    live="$(revision_label "$revision" nori-commit)"
    if [[ -z "$live" ]]; then echo "changed: $svc has no release label"; return; fi
    [[ "$live" == "$SHA" ]] && continue
    if ! git cat-file -e "$live^{commit}" 2>/dev/null; then echo "changed: $svc runs unknown commit ${live:0:12}"; return; fi
    if git merge-base --is-ancestor "$SHA" "$live"; then echo "newer: $svc already runs ${live:0:12}"; return; fi
    read -r -a paths <<< "$(group_paths "$group")"
    if [[ -n "$(git diff --name-only "$live" "$SHA" -- "${paths[@]}")" ]]; then
      echo "changed: ${live:0:12} -> ${SHA:0:12}"
      return
    fi
  done
  echo "unchanged"
}

RELEASE_GROUPS=()
SUMMARY=()
for group in core web site; do
  forced=0
  [[ "$group" == core && "$FORCE_CORE" -eq 1 ]] && forced=1
  [[ "$group" == web && "$FORCE_WEB" -eq 1 ]] && forced=1
  [[ "$group" == site && "$FORCE_SITE" -eq 1 ]] && forced=1
  if [[ "$ONLY_FORCED" -eq 1 && "$forced" -eq 0 ]]; then
    SUMMARY+=("$group: skip (not selected with --only)")
    continue
  fi
  change="$(group_change "$group")"
  case "$change" in
    unknown:*) die "${change#*: }." ;;
    missing:*)
      [[ "$group" == site && "$forced" -eq 0 ]] || die "cannot release $group: ${change#*: }."
      SUMMARY+=("$group: skip (${change#*: })")
      continue
      ;;
  esac
  if [[ "$forced" -eq 1 || "$change" == changed:* ]]; then
    if [[ "$group" == site ]]; then
      if [[ "$PLAN_GROUPS" -eq 1 ]]; then
        if [[ ! -f site/Dockerfile ]]; then
          [[ "$forced" -eq 0 ]] || die "site/Dockerfile does not exist."
          SUMMARY+=("site: skip (no site/Dockerfile)")
          continue
        fi
      elif ! image_exists "$(group_image site)"; then
        [[ "$forced" -eq 0 ]] || die "image $(group_image site) does not exist; CI builds it after the checks pass."
        SUMMARY+=("site: skip (no nori-site image for ${SHA:0:12})")
        continue
      fi
    fi
    RELEASE_GROUPS+=("$group")
    suffix=""
    if [[ "$forced" -eq 1 ]]; then suffix=", forced"; fi
    SUMMARY+=("$group: release (${change}${suffix})")
  else
    SUMMARY+=("$group: skip ($change)")
  fi
done
if [[ "$PLAN_GROUPS" -eq 1 ]]; then
  if [[ "${#RELEASE_GROUPS[@]}" -gt 0 ]]; then
    printf '%s\n' "${RELEASE_GROUPS[@]}"
  fi
  exit 0
fi
log "Release ${SHA:0:12}"
printf '    %s\n' "${SUMMARY[@]}"
if [[ "${#RELEASE_GROUPS[@]}" -eq 0 ]]; then
  log "Nothing to release."
  exit 0
fi
for group in "${RELEASE_GROUPS[@]}"; do
  image="$(group_image "$group")"
  image_exists "$image" || die "image $image does not exist; CI builds it after the checks pass."
done

var_name() { printf '%s_%s' "$1" "${2//-/_}"; }
set_for() { printf -v "$(var_name "$1" "$2")" '%s' "$3"; }
get_for() { local name; name="$(var_name "$1" "$2")"; printf '%s' "${!name:-}"; }
PROMOTED=()

untag_all() {
  local svc
  for svc in "$@"; do
    run cloud_run services update-traffic "$svc" --remove-tags="$TAG" --quiet || true
  done
}

revert_group() {
  local group="$1" svc
  for svc in $(group_services "$group"); do
    if [[ -n "$(get_for STAGED "$svc")" && -n "$(get_for PREVIOUS "$svc")" ]]; then
      log "Reverting $svc to $(get_for PREVIOUS "$svc")"
      run cloud_run services update-traffic "$svc" --to-revisions="$(get_for PREVIOUS "$svc")=100" --quiet || true
    fi
  done
}

check_staged() {
  local svc="$1" url="$2" health_status root_status
  case "$svc" in
    nori-core) [[ "$(http_status "$url/readyz")" == 200 ]] ;;
    nori-web)
      # nori-web sits behind Cloud Run IAP, so unauthenticated probes receive a 302
      # redirect to Google sign-in while Cloud Run's startup probe verifies /api/health
      # inside the container (and nori-web-mac verifies /api/health == 200 on the same image).
      health_status="$(http_status "$url/api/health")"
      root_status="$(http_status "$url/")"
      [[ "$health_status" == 200 || "$health_status" == 302 ]] &&
        [[ "$root_status" =~ ^[234][0-9][0-9]$ ]]
      ;;
    nori-web-mac)
      [[ "$(http_status "$url/api/health")" == 200 ]] &&
        [[ "$(http_status "$url/")" == 403 ]] &&
        [[ "$(http_status -H 'Authorization: Bearer invalid' "$url/")" == 403 ]]
      ;;
    nori-site)
      # Cloud Run reserves /healthz on its public URLs, so check real pages from outside.
      [[ "$(http_status "$url/")" == 200 ]] &&
        [[ "$(http_status "$url/privacy")" == 200 ]]
      ;;
    *) return 0 ;;
  esac
}

stage() {
  local svc="$1" image="$2" previous revision url
  previous="$(serving_revision "$svc")"
  [[ -n "$previous" ]] || { echo "$svc has split traffic; refusing to stage." >&2; return 1; }
  set_for PREVIOUS "$svc" "$previous"
  log "Staging $svc (previous $previous)"
  run cloud_run deploy "$svc" --image="$image" --no-traffic --tag="$TAG" --quiet \
    --update-labels="nori-commit=$SHA,nori-prev=$previous" \
    --startup-probe="httpGet.path=$(probe_path "$svc"),periodSeconds=3,timeoutSeconds=3,failureThreshold=40" || return 1
  [[ "$DRY_RUN" -eq 1 ]] && { set_for STAGED "$svc" "$svc-staged"; return 0; }
  IFS=$'\t' read -r revision url <<< "$(tagged "$svc" "$TAG")"
  [[ -n "$revision" ]] && revision_ready "$revision" || { echo "$svc staged revision is not Ready." >&2; return 1; }
  check_staged "$svc" "$url" || { echo "$svc staged revision failed its checks." >&2; return 1; }
  set_for STAGED "$svc" "$revision"
}

STAGED_CANDIDATES=()
stage_group() {
  local group="$1" image svc services
  image="$(group_image "$group")"
  read -r -a services <<< "$(group_services "$group")"
  if [[ "$group" == core ]]; then
    log "Running migrations ($MIGRATION_JOB)"
    run cloud_run jobs update "$MIGRATION_JOB" --image="$image" --quiet || return 1
    run cloud_run jobs execute "$MIGRATION_JOB" --wait --quiet || return 1
  fi
  STAGED_CANDIDATES+=("${services[@]}")
  for svc in "${services[@]}"; do
    if ! stage "$svc" "$image"; then
      untag_all "${STAGED_CANDIDATES[@]}"
      return 1
    fi
  done
}

promote_group() {
  local group="$1" svc services
  read -r -a services <<< "$(group_services "$group")"
  for svc in "${services[@]}"; do
    log "Promoting $svc to $(get_for STAGED "$svc")"
    if ! run cloud_run services update-traffic "$svc" --to-revisions="$(get_for STAGED "$svc")=100" --remove-tags="$TAG" --quiet; then
      revert_group "$group"
      return 1
    fi
    PROMOTED+=("$svc")
  done
}

bake_failures() {
  local since="$1" revisions filter attempt output
  revisions="$(for svc in "${PROMOTED[@]}"; do printf '"%s" OR ' "$(get_for STAGED "$svc")"; done)"
  revisions="${revisions% OR }"
  filter="resource.type=\"cloud_run_revision\" AND resource.labels.revision_name=($revisions) AND timestamp>=\"$since\" AND ((logName:\"run.googleapis.com%2Frequests\" AND httpRequest.status>=500) OR (logName:\"run.googleapis.com%2Fvarlog%2Fsystem\" AND textPayload=~\"probe failed|Container called exit|Container terminated|Memory limit\") OR (logName:(\"run.googleapis.com%2Fstdout\" OR \"run.googleapis.com%2Fstderr\") AND (severity>=ERROR OR jsonPayload.level=\"ERROR\")))"
  for attempt in 1 2 3; do
    if output="$(gcloud logging read "$filter" --project="$PROJECT" --limit=20 --format='value(resource.labels.revision_name)')"; then
      printf '%s\n' "$output" | awk 'NF' | sort -u
      return 0
    fi
    [[ "$attempt" -lt 3 ]] && sleep "${NORI_RELEASE_LOG_RETRY_SLEEP:-5}"
  done
  return 1
}

FAILED=0
for group in "${RELEASE_GROUPS[@]}"; do
  if ! stage_group "$group"; then
    printf 'release: %s group failed to stage; no groups were promoted.\n' "$group" >&2
    FAILED=1
    break
  fi
done

STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
if [[ "$FAILED" -eq 0 ]]; then
  PROMOTED_GROUPS=()
  for group in "${RELEASE_GROUPS[@]}"; do
    if ! promote_group "$group"; then
      printf 'release: %s group failed to promote; reverting promoted groups.\n' "$group" >&2
      for prev_group in "${PROMOTED_GROUPS[@]}"; do
        revert_group "$prev_group"
      done
      untag_all "${STAGED_CANDIDATES[@]}"
      FAILED=1
      break
    fi
    PROMOTED_GROUPS+=("$group")
  done
fi

if [[ "$FAILED" -eq 0 && "${#PROMOTED[@]}" -gt 0 && "$DRY_RUN" -eq 0 ]]; then
  log "Baking ${PROMOTED[*]} for ${BAKE_SECONDS}s"
  elapsed=0
  bad=""
  while :; do
    bad="$(bake_failures "$STARTED_AT")"
    [[ -n "$bad" || "$elapsed" -ge "$BAKE_SECONDS" ]] && break
    sleep "$BAKE_INTERVAL"
    elapsed=$((elapsed + BAKE_INTERVAL))
  done
  if [[ -n "$bad" ]]; then
    printf 'release: errors from %s during bake.\n' "$(tr '\n' ' ' <<< "$bad")" >&2
    reverted=" "
    for svc in "${PROMOTED[@]}"; do
      if grep -qx "$(get_for STAGED "$svc")" <<< "$bad"; then
        group="$(group_of "$svc")"
        [[ "$reverted" == *" $group "* ]] || { revert_group "$group"; reverted+="$group "; }
      fi
    done
    FAILED=1
  fi
fi

if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  {
    echo "### Release ${SHA:0:12} $([[ "$FAILED" -eq 0 ]] && echo succeeded || echo failed)"
    if [[ -n "$FALLBACK_FROM" ]]; then
      echo "> [!WARNING]"
      echo "> main HEAD \`${FALLBACK_FROM:0:12}\` was not green/built; fell back to \`${SHA:0:12}\`."
    fi
    printf -- '- %s\n' "${SUMMARY[@]}"
  } >> "$GITHUB_STEP_SUMMARY"
fi
[[ "$FAILED" -eq 0 ]] || exit 1
log "Released ${SHA:0:12} (${RELEASE_GROUPS[*]})."
