#!/usr/bin/env bash
set -euo pipefail

# Quick test-and-deploy script for GCP Cloud Run (nori-core + nori-web).
# Usage:
#   ./scripts/deploy-gcp.sh [--skip-tests] [--core-only | --web-only] [--allow-branch] [--dry-run]

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
      sed -n '3,6p' "$0"
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

# Ensure deployable paths (core/, web/, contracts/) have no uncommitted changes.
DIRTY="$(git status --porcelain -- core web contracts)"
if [[ -n "$DIRTY" ]]; then
  echo "Refusing to deploy with uncommitted changes under core/, web/, or contracts/:" >&2
  echo "$DIRTY" >&2
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

if [[ "$DEPLOY_CORE" -eq 1 ]]; then
  echo "==> Deploying nori-core to Cloud Run..."
  run gcloud run deploy nori-core \
    --project="${PROJECT}" \
    --region="${REGION}" \
    --image="${CORE_IMAGE}" \
    --quiet
fi

if [[ "$DEPLOY_WEB" -eq 1 ]]; then
  echo "==> Deploying nori-web to Cloud Run..."
  run gcloud run deploy nori-web \
    --project="${PROJECT}" \
    --region="${REGION}" \
    --image="${WEB_IMAGE}" \
    --quiet
fi

if [[ "$DRY_RUN" -eq 0 ]]; then
  echo "==> Verifying Cloud Run revisions & health..."
  if [[ "$DEPLOY_CORE" -eq 1 ]]; then
    CORE_URL="$(gcloud run services describe nori-core --project="${PROJECT}" --region="${REGION}" --format='value(status.url)')"
    CORE_REV="$(gcloud run services describe nori-core --project="${PROJECT}" --region="${REGION}" --format='value(status.latestReadyRevisionName)')"
    HTTP_CODE="$(curl -sS -o /dev/null -w '%{http_code}' "${CORE_URL}/readyz")"
    echo "    nori-core: ${CORE_REV} (${CORE_URL}/readyz -> HTTP ${HTTP_CODE})"
    if [[ "$HTTP_CODE" != "200" ]]; then
      echo "ERROR: nori-core readiness check failed (HTTP ${HTTP_CODE})" >&2
      exit 1
    fi
  fi
  if [[ "$DEPLOY_WEB" -eq 1 ]]; then
    WEB_URL="$(gcloud run services describe nori-web --project="${PROJECT}" --region="${REGION}" --format='value(status.url)')"
    WEB_REV="$(gcloud run services describe nori-web --project="${PROJECT}" --region="${REGION}" --format='value(status.latestReadyRevisionName)')"
    echo "    nori-web:  ${WEB_REV} (${WEB_URL})"
  fi
fi

echo "==> Deploy complete (${SHA})."
