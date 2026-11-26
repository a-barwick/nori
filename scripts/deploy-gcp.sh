#!/usr/bin/env bash
set -euo pipefail


PROJECT="${GCP_PROJECT:-bot-111549}"
REGION="${GCP_REGION:-us-central1}"
REPO="${GCP_AR_REPO:-nori-poc}"
REGISTRY="${REGION}-docker.pkg.dev/${PROJECT}/${REPO}"

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
USAGE
      exit 0
      ;;
    *)
      echo "Unknown option: $arg" >&2
      exit 2
      ;;
  esac
done

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

SHA="$(git rev-parse --short HEAD)"
CORE_IMAGE="${REGISTRY}/nori-core:${SHA}"
WEB_IMAGE="${REGISTRY}/nori-web:${SHA}"

echo "==> Target: project=${PROJECT} region=${REGION} branch=${BRANCH} sha=${SHA}"
[[ "$DEPLOY_CORE" -eq 1 ]] && echo "    core image: ${CORE_IMAGE}"
[[ "$DEPLOY_WEB" -eq 1 ]] && echo "    web image:  ${WEB_IMAGE}"

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

probe() {
  if [[ "$DRY_RUN" -eq 1 ]]; then
    echo "+ check $1 $2"
    return
  fi
  local url
  url="$(service_url "$1")"
  curl --silent --show-error --fail --output /dev/null --max-time 15 --retry 5 --retry-all-errors "${url}$2"
}

if [[ "$DEPLOY_CORE" -eq 1 ]]; then
  if [[ -n "${NORI_GCP_MIGRATION_JOB:-}" ]]; then
    run gcloud run jobs update "$NORI_GCP_MIGRATION_JOB" --project="$PROJECT" --region="$REGION" --image="$CORE_IMAGE" --args=--migrate-only --quiet
    run gcloud run jobs execute "$NORI_GCP_MIGRATION_JOB" --project="$PROJECT" --region="$REGION" --wait --quiet
  fi
  run gcloud run deploy nori-core --project="$PROJECT" --region="$REGION" --image="$CORE_IMAGE" --quiet
  probe nori-core /readyz
fi

if [[ "$DEPLOY_WEB" -eq 1 ]]; then
  run gcloud run deploy nori-web --project="$PROJECT" --region="$REGION" --image="$WEB_IMAGE" --quiet
  probe nori-web /api/health
  if [[ -n "${NORI_DEPLOY_SMOKE_TOKEN_FILE:-}" && "$DRY_RUN" -eq 0 ]]; then
    web_url="$(service_url nori-web)"
    smoke_token="$(<"$NORI_DEPLOY_SMOKE_TOKEN_FILE")"
    printf 'Authorization: Bearer %s\nSec-Fetch-Site: same-origin\n' "$smoke_token" |
      curl --silent --show-error --fail --output /dev/null --max-time 15 --retry 5 --retry-all-errors -H @- "${web_url}/api/v1/capabilities"
  fi
fi

echo "==> Deploy complete (${SHA})."
