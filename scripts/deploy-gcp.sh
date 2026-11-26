#!/usr/bin/env bash
set -euo pipefail

# Cloud Run production deployment script supporting both topologies:
#   - combined (default, matches live prod today): nori-core (--role=all) + nori-web
#   - split (NORI_DEPLOY_TOPOLOGY=split): mandatory migration job (nori-migrate),
#     nori-api (NORI_ROLE=api), nori-worker (NORI_ROLE=worker, internal ingress,
#     --no-cpu-throttling, min-instances >= 1, --metrics-addr=0.0.0.0:8080), and nori-web.
#
# Postgres connection budget (split topology):
#   total_conns = (NORI_API_MAX_INSTANCES * NORI_API_POSTGRES_MAX_CONNS)
#               + (NORI_WORKER_MAX_INSTANCES * (NORI_WORKER_POSTGRES_MAX_CONNS + 1_listen))
#               + migration_job (4)
#   Defaults: (10 * 8) + (4 * 11) + 4 = 128 connections (fits Cloud SQL max_connections=200).
#   When using PgBouncer in transaction pooling mode for NORI_POSTGRES_URL, set
#   NORI_POSTGRES_LISTEN_URL_SECRET (e.g. "nori-postgres-listen-url:latest") so the
#   direct session-mode Postgres URL is mounted as a Secret Manager secret (never plain env).

PROJECT="${GCP_PROJECT:-bot-111549}"
REGION="${GCP_REGION:-us-central1}"
REPO="${GCP_AR_REPO:-nori-poc}"
REGISTRY="${REGION}-docker.pkg.dev/${PROJECT}/${REPO}"
CLOUD="${NORI_CLOUD:-gcp}"
TOPOLOGY="${NORI_DEPLOY_TOPOLOGY:-split}"

CORE_SERVICE="${NORI_GCP_CORE_SERVICE:-nori-core}"
API_SERVICE="${NORI_GCP_API_SERVICE:-nori-core}"
WORKER_SERVICE="${NORI_GCP_WORKER_SERVICE:-nori-worker}"
WEB_SERVICE="${NORI_GCP_WEB_SERVICE:-nori-web}"
if [[ "$TOPOLOGY" == "split" ]]; then
  MIGRATION_JOB="${NORI_GCP_MIGRATION_JOB-nori-migrate}"
else
  MIGRATION_JOB="${NORI_GCP_MIGRATION_JOB:-}"
fi

API_MIN_INSTANCES="${NORI_API_MIN_INSTANCES:-1}"
API_MAX_INSTANCES="${NORI_API_MAX_INSTANCES:-10}"
API_CONCURRENCY="${NORI_API_CONCURRENCY:-80}"
API_POSTGRES_MAX_CONNS="${NORI_API_POSTGRES_MAX_CONNS:-${NORI_POSTGRES_MAX_CONNS:-8}}"

WORKER_MIN_INSTANCES="${NORI_WORKER_MIN_INSTANCES:-1}"
WORKER_MAX_INSTANCES="${NORI_WORKER_MAX_INSTANCES:-4}"
WORKER_POSTGRES_MAX_CONNS="${NORI_WORKER_POSTGRES_MAX_CONNS:-${NORI_POSTGRES_MAX_CONNS:-10}}"

SKIP_TESTS=0
DEPLOY_CORE=1
DEPLOY_WEB=1
ALLOW_BRANCH=0
DRY_RUN=0

for arg in "$@"; do
  case "$arg" in
    --skip-tests) SKIP_TESTS=1 ;;
    --core-only) DEPLOY_WEB=0 ;;
    --web-only) DEPLOY_CORE=0 ;;
    --allow-branch) ALLOW_BRANCH=1 ;;
    --dry-run) DRY_RUN=1 ;;
    -h|--help)
      cat <<'USAGE'
Test and deploy Nori Core and Nori Web to GCP Cloud Run.

Usage: ./scripts/deploy-gcp.sh [--skip-tests] [--core-only | --web-only] [--allow-branch] [--dry-run]

Environment variables:
  NORI_DEPLOY_TOPOLOGY        combined (default: nori-core + nori-web) or split (nori-api + nori-worker + nori-web)
  NORI_CLOUD                  Cloud adapter: gcp (default) or aws
  NORI_GCP_MIGRATION_JOB      Cloud Run Job name for --migrate-only (mandatory in split mode, optional in combined mode)
  NORI_GCP_CORE_SERVICE       Cloud Run combined core service name (default: nori-core)
  NORI_GCP_API_SERVICE        Cloud Run API service name for split mode (default: nori-api)
  NORI_GCP_WORKER_SERVICE     Cloud Run Worker service name for split mode (default: nori-worker)
  NORI_GCP_WEB_SERVICE        Cloud Run Web service name (default: nori-web)
  NORI_API_MIN_INSTANCES      nori-api min instances (default: 1)
  NORI_API_MAX_INSTANCES      nori-api max instances (default: 10)
  NORI_API_CONCURRENCY        nori-api max concurrent requests per instance (default: 80)
  NORI_API_POSTGRES_MAX_CONNS Postgres pool cap per nori-api instance (default: NORI_POSTGRES_MAX_CONNS or 8)
  NORI_WORKER_MIN_INSTANCES   nori-worker min instances; must be >= 1 (default: 1)
  NORI_WORKER_MAX_INSTANCES   nori-worker max instances (default: 4)
  NORI_WORKER_POSTGRES_MAX_CONNS
                              Postgres pool cap per nori-worker instance (default: NORI_POSTGRES_MAX_CONNS or 10)
  NORI_POSTGRES_LISTEN_URL_SECRET
                              Optional Secret Manager ref (e.g. nori-postgres-listen-url:latest) for LISTEN/NOTIFY
  NORI_DEPLOY_SMOKE_TOKEN_FILE
                              Optional file with ID token for post-deploy authenticated web smoke check
  NORI_HOME_AI_KEY_SECRET    Shared Home AI encryption key secret, in name:version form.
                              Set the verification URL and optional download, Work, and database settings.
USAGE
      exit 0
      ;;
    *)
      echo "Unknown option: $arg" >&2
      exit 2
      ;;
  esac
done

if [[ "$CLOUD" != "gcp" && "$CLOUD" != "aws" ]]; then
  echo "NORI_CLOUD must be 'gcp' or 'aws' (got '$CLOUD')." >&2
  exit 1
fi

if [[ "$TOPOLOGY" != "combined" && "$TOPOLOGY" != "split" ]]; then
  echo "NORI_DEPLOY_TOPOLOGY must be 'combined' or 'split' (got '$TOPOLOGY')." >&2
  exit 1
fi

if [[ "$DEPLOY_CORE" -eq 1 && "$TOPOLOGY" == "split" ]]; then
  if [[ -z "$MIGRATION_JOB" ]]; then
    echo "NORI_GCP_MIGRATION_JOB must be set for split core deployments (pre-deploy --migrate-only is mandatory)." >&2
    exit 1
  fi
  if ! [[ "$WORKER_MIN_INSTANCES" =~ ^[0-9]+$ ]] || [[ "$WORKER_MIN_INSTANCES" -lt 1 ]]; then
    echo "NORI_WORKER_MIN_INSTANCES must be an integer >= 1 (got '$WORKER_MIN_INSTANCES')." >&2
    exit 1
  fi
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

BRANCH="$(git rev-parse --abbrev-ref HEAD)"
if [[ "$ALLOW_BRANCH" -eq 0 && "$BRANCH" != "main" ]]; then
  echo "Refusing to deploy from branch '$BRANCH' (expected 'main'; pass --allow-branch to override)." >&2
  exit 1
fi

DIRTY="$(git status --porcelain -- core web contracts scripts/deploy-gcp.sh)"
if [[ -n "$DIRTY" ]]; then
  echo "Refusing to deploy with uncommitted changes under core/, web/, or contracts/:" >&2
  echo "$DIRTY" >&2
  exit 1
fi

if [[ "$DEPLOY_WEB" -eq 1 && -n "${NORI_DEPLOY_SMOKE_TOKEN_FILE:-}" && ! -s "$NORI_DEPLOY_SMOKE_TOKEN_FILE" ]]; then
  echo "NORI_DEPLOY_SMOKE_TOKEN_FILE must contain an ID token for a provisioned account." >&2
  exit 1
fi

if [[ "$DEPLOY_CORE" -eq 1 && -n "${NORI_HOME_AI_KEY_SECRET:-}" ]]; then
  : "${NORI_HOME_AI_VERIFY_URL:?Set the Nori Web pairing URL}"
fi

SHA="$(git rev-parse --short HEAD)"
CORE_IMAGE="${REGISTRY}/nori-core:${SHA}"
WEB_IMAGE="${REGISTRY}/nori-web:${SHA}"

echo "==> Target: project=${PROJECT} region=${REGION} topology=${TOPOLOGY} cloud=${CLOUD} branch=${BRANCH} sha=${SHA}"
if [[ "$DEPLOY_CORE" -eq 1 ]]; then
  echo "    core image:   ${CORE_IMAGE}"
  [[ -n "$MIGRATION_JOB" ]] && echo "    migrate job:  ${MIGRATION_JOB}"
  if [[ "$TOPOLOGY" == "split" ]]; then
    echo "    api service:  ${API_SERVICE} (min=${API_MIN_INSTANCES} max=${API_MAX_INSTANCES} pg_max_conns=${API_POSTGRES_MAX_CONNS})"
    echo "    worker svc:   ${WORKER_SERVICE} (min=${WORKER_MIN_INSTANCES} max=${WORKER_MAX_INSTANCES} pg_max_conns=${WORKER_POSTGRES_MAX_CONNS})"
  else
    echo "    core service: ${CORE_SERVICE}"
  fi
fi
[[ "$DEPLOY_WEB" -eq 1 ]] && echo "    web image:    ${WEB_IMAGE} (${WEB_SERVICE})"

run() {
  echo "+ $*"
  if [[ "$DRY_RUN" -eq 0 ]]; then
    "$@"
  fi
}

if [[ "$SKIP_TESTS" -eq 0 ]]; then
  echo "==> Running pre-deploy checks (make check)..."
  run make check
fi

if [[ "$DEPLOY_CORE" -eq 1 ]]; then
  echo "==> Building & pushing nori-core (${SHA})..."
  run docker buildx build \
    --platform linux/amd64 \
    -f core/infrastructure/Dockerfile \
    --build-arg "SENTRY_RELEASE=${SHA}" \
    -t "${CORE_IMAGE}" \
    -t "${REGISTRY}/nori-core:latest" \
    --push \
    core
fi

if [[ "$DEPLOY_WEB" -eq 1 ]]; then
  echo "==> Building & pushing nori-web (${SHA})..."
  run docker buildx build \
    --platform linux/amd64 \
    -f web/Dockerfile \
    --build-arg "SENTRY_RELEASE=${SHA}" \
    -t "${WEB_IMAGE}" \
    -t "${REGISTRY}/nori-web:latest" \
    --push \
    .
fi

service_url() {
  gcloud run services describe "$1" --project="$PROJECT" --region="$REGION" --format='value(status.url)'
}

probe_url() {
  local url="$1" service="$2" status
  local args=(--silent --show-error --fail --output /dev/null --write-out '%{http_code}' --max-time 15 --retry 5 --retry-all-errors)
  if [[ "$service" == "$WEB_SERVICE" && -n "${NORI_DEPLOY_SMOKE_TOKEN_FILE:-}" ]]; then
    status="$(printf 'Authorization: Bearer %s\nSec-Fetch-Site: same-origin\n' "$(<"$NORI_DEPLOY_SMOKE_TOKEN_FILE")" |
      curl "${args[@]}" -H @- "$url")" || return 1
    [[ "$status" == 200 ]]
  else
    status="$(curl "${args[@]}" "$url")" || return 1
    [[ "$status" == 200 || ("$service" == "$WEB_SERVICE" && "$status" == 302) ]]
  fi
}

check_revision_ready() {
  local revision="$1"
  local status
  status="$(gcloud run revisions describe "$revision" --project="$PROJECT" --region="$REGION" \
    --flatten='status.conditions[]' --format='value(status.conditions.type,status.conditions.status)' |
    awk -F '\t' '$1 == "Ready" { print $2 }')"
  [[ "$status" == "True" ]]
}

deploy_checked() {
  local service="$1" image="$2" path="$3" tag="release-${SHA}"
  shift 3
  run gcloud run deploy "$service" --project="$PROJECT" --region="$REGION" \
    --image="$image" --no-traffic --tag="$tag" --quiet "$@"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    echo "+ check tagged $service ($path)"
    echo "+ promote tagged $service to 100% and remove $tag"
    return
  fi

  local candidate revision url
  candidate="$(gcloud run services describe "$service" --project="$PROJECT" --region="$REGION" \
    --flatten='status.traffic[]' --format='value(status.traffic.tag,status.traffic.revisionName,status.traffic.url)' |
    awk -F '\t' -v tag="$tag" '$1 == tag { print $2 "\t" $3 }')"
  IFS=$'\t' read -r revision url <<< "$candidate"

  local ready=0
  if [[ "$path" == "revision-ready" ]]; then
    [[ -n "$revision" ]] && check_revision_ready "$revision" && ready=1
  else
    if [[ -n "$revision" && -n "$url" ]] && probe_url "${url}${path}" "$service"; then
      if [[ "$service" != "$WEB_SERVICE" || -n "${NORI_DEPLOY_SMOKE_TOKEN_FILE:-}" ]] || check_revision_ready "$revision"; then
        ready=1
      fi
    fi
  fi

  if [[ "$ready" -ne 1 ]]; then
    echo "Tagged $service revision failed readiness. Existing traffic was preserved." >&2
    run gcloud run services update-traffic "$service" --project="$PROJECT" --region="$REGION" --remove-tags="$tag" --quiet || true
    return 1
  fi
  run gcloud run services update-traffic "$service" --project="$PROJECT" --region="$REGION" \
    --to-revisions="$revision=100" --remove-tags="$tag" --quiet
  if [[ "$path" != "revision-ready" ]]; then
    probe_url "$(service_url "$service")$path" "$service"
  fi
}

if [[ "$DEPLOY_CORE" -eq 1 ]]; then
  if [[ -n "$MIGRATION_JOB" ]]; then
    echo "==> Running pre-deploy migration job (${MIGRATION_JOB})..."
    run gcloud run jobs update "$MIGRATION_JOB" --project="$PROJECT" --region="$REGION" \
      --image="$CORE_IMAGE" --args=--migrate-only --update-env-vars="NORI_CLOUD=${CLOUD}" --quiet
    run gcloud run jobs execute "$MIGRATION_JOB" --project="$PROJECT" --region="$REGION" --wait --quiet
  fi

  common_core_hygiene=(
    --remove-secrets=OPENAI_API_KEY,ANTHROPIC_API_KEY,OPENROUTER_API_KEY,NORI_MODEL_GATEWAY_API_KEY
    --remove-env-vars=NORI_MODEL_PROVIDER,NORI_MODEL_GATEWAY_URL,NORI_MODEL_PICKER_USERS,NORI_BYOK_REQUIRED,NORI_AGENT_MODEL,MODEL_NAME,NORI_OUTER_REASONING_EFFORT,NORI_INNER_REASONING_EFFORT
  )
  secret_updates=()
  if [[ -n "${NORI_POSTGRES_LISTEN_URL_SECRET:-}" ]]; then
    secret_updates+=("NORI_POSTGRES_LISTEN_URL=$NORI_POSTGRES_LISTEN_URL_SECRET")
  fi
  home_env=""
  if [[ -n "${NORI_HOME_AI_KEY_SECRET:-}" ]]; then
    secret_updates+=("NORI_HOME_AI_KEY=$NORI_HOME_AI_KEY_SECRET")
    for name in NORI_HOME_AI_CORE_DB_SECRET NORI_HOME_AI_CORE_DB_LISTEN_SECRET; do
      if [[ -n "${!name:-}" ]]; then
        key=NORI_HOME_AI_POSTGRES_URL
        if [[ "$name" == NORI_HOME_AI_CORE_DB_LISTEN_SECRET ]]; then key=NORI_HOME_AI_POSTGRES_LISTEN_URL; fi
        secret_updates+=("$key=${!name}")
      fi
    done
    home_env=",NORI_HOME_AI_VERIFY_URL=$NORI_HOME_AI_VERIFY_URL,NORI_HOME_AI_WORK_ENABLED=${NORI_HOME_AI_WORK_ENABLED:-false}"
    for name in NORI_HOME_AI_DOWNLOAD_MACOS NORI_HOME_AI_DOWNLOAD_LINUX_AMD64 NORI_HOME_AI_DOWNLOAD_LINUX_ARM64; do
      if [[ -n "${!name:-}" ]]; then home_env+=",$name=${!name}"; fi
    done
  fi
  if [[ "${#secret_updates[@]}" -gt 0 ]]; then
    common_core_hygiene+=(--update-secrets="$(IFS=,; printf '%s' "${secret_updates[*]}")")
  fi

  if [[ "$TOPOLOGY" == "split" ]]; then
    api_args=(
      "${common_core_hygiene[@]}"
      --min-instances="${API_MIN_INSTANCES}"
      --max-instances="${API_MAX_INSTANCES}"
      --concurrency="${API_CONCURRENCY}"
      --update-env-vars="NORI_ROLE=api,NORI_CLOUD=${CLOUD},NORI_POSTGRES_MAX_CONNS=${API_POSTGRES_MAX_CONNS}${home_env}"
    )
    deploy_checked "$API_SERVICE" "$CORE_IMAGE" /readyz "${api_args[@]}"

    # nori-worker uses --ingress=internal, so verify Cloud Run's internal revision readiness
    # condition rather than curling its internal-only URL from the developer workstation.
    worker_args=(
      "${common_core_hygiene[@]}"
      --args=--metrics-addr=0.0.0.0:8080
      --ingress=internal
      --no-cpu-throttling
      --min-instances="${WORKER_MIN_INSTANCES}"
      --max-instances="${WORKER_MAX_INSTANCES}"
      --update-env-vars="NORI_ROLE=worker,NORI_CLOUD=${CLOUD},NORI_POSTGRES_MAX_CONNS=${WORKER_POSTGRES_MAX_CONNS}${home_env}"
    )
    deploy_checked "$WORKER_SERVICE" "$CORE_IMAGE" revision-ready "${worker_args[@]}"
  else
    combined_env="NORI_CLOUD=${CLOUD}${home_env}"
    if [[ -n "${NORI_POSTGRES_MAX_CONNS:-}" ]]; then combined_env+=",NORI_POSTGRES_MAX_CONNS=$NORI_POSTGRES_MAX_CONNS"; fi
    core_args=(
      "${common_core_hygiene[@]}"
      --update-env-vars="$combined_env"
    )
    deploy_checked "$CORE_SERVICE" "$CORE_IMAGE" /readyz "${core_args[@]}"
  fi
fi

if [[ "$DEPLOY_WEB" -eq 1 ]]; then
  deploy_checked "$WEB_SERVICE" "$WEB_IMAGE" /api/health --update-env-vars="NORI_CLOUD=${CLOUD}"
  if [[ -n "${NORI_DEPLOY_SMOKE_TOKEN_FILE:-}" && "$DRY_RUN" -eq 0 ]]; then
    web_url="$(service_url "$WEB_SERVICE")"
    smoke_token="$(<"$NORI_DEPLOY_SMOKE_TOKEN_FILE")"
    printf 'Authorization: Bearer %s\nSec-Fetch-Site: same-origin\n' "$smoke_token" |
      curl --silent --show-error --fail --output /dev/null --max-time 15 --retry 5 --retry-all-errors -H @- "${web_url}/api/v1/capabilities"
  fi
fi

echo "==> Deploy complete (${SHA})."
