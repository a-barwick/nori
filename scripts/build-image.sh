#!/usr/bin/env bash
set -euo pipefail

PROJECT="${GCP_PROJECT:-bot-111549}"
REGION="${GCP_REGION:-us-central1}"
REGISTRY="${NORI_REGISTRY:-${REGION}-docker.pkg.dev/${PROJECT}/${GCP_AR_REPO:-nori-poc}}"

usage() {
  cat <<'USAGE'
Build and push a Nori container image to Artifact Registry.

Usage:
  scripts/build-image.sh [--skip-existing] <core|web|site> <sha>

Environment:
  GCP_PROJECT, GCP_REGION, GCP_AR_REPO, NORI_REGISTRY
  SENTRY_ORG, SENTRY_PROJECT (optional; used for web source-map upload)
USAGE
}

log() { printf '==> %s\n' "$*"; }
die() { printf 'build-image: %s\n' "$*" >&2; exit 1; }

SKIP_EXISTING=0
POSITIONAL=()
for arg in "$@"; do
  case "$arg" in
    --skip-existing) SKIP_EXISTING=1 ;;
    -h|--help) usage; exit 0 ;;
    -*) usage >&2; exit 2 ;;
    *) POSITIONAL+=("$arg") ;;
  esac
done

[[ "${#POSITIONAL[@]}" -eq 2 ]] || { usage >&2; exit 2; }
GROUP="${POSITIONAL[0]}"
SHA="${POSITIONAL[1]}"
[[ -n "$SHA" ]] || die "commit SHA is required."

case "$GROUP" in
  core) IMAGE="$REGISTRY/nori-core:$SHA" ;;
  web) IMAGE="$REGISTRY/nori-web:$SHA" ;;
  site)
    if [[ ! -f site/Dockerfile ]]; then
      log "site/Dockerfile not found; skipping site image."
      exit 0
    fi
    IMAGE="$REGISTRY/nori-site:$SHA"
    ;;
  *) die "unknown group '$GROUP' (expected core, web, or site)." ;;
esac

if [[ "$SKIP_EXISTING" -eq 1 ]]; then
  if gcloud artifacts docker images describe "$IMAGE" --project="$PROJECT" --format='value(image_summary.digest)' >/dev/null 2>&1; then
    log "Image $IMAGE already exists; skipping build."
    exit 0
  fi
fi

gcloud auth configure-docker "${REGION}-docker.pkg.dev" --quiet
if ! docker buildx inspect nori-builder >/dev/null 2>&1; then
  docker buildx create --name nori-builder --driver docker-container --use
else
  docker buildx use nori-builder
fi

case "$GROUP" in
  core)
    log "Building and pushing $IMAGE"
    docker buildx build --platform linux/amd64 -f core/infrastructure/Dockerfile \
      --cache-from "type=registry,ref=$REGISTRY/nori-core:buildcache" \
      --cache-to "type=registry,ref=$REGISTRY/nori-core:buildcache,mode=max" \
      --build-arg "SENTRY_RELEASE=$SHA" \
      -t "$IMAGE" --push core
    ;;
  web)
    cid=""
    sentry_token_file=""
    cleanup() {
      if [[ -n "$cid" ]]; then docker rm -f "$cid" >/dev/null 2>&1 || true; fi
      if [[ -n "$sentry_token_file" ]]; then rm -f "$sentry_token_file"; fi
    }
    trap cleanup EXIT

    web_sentry_args=()
    if [[ -n "${SENTRY_ORG:-}" && -n "${SENTRY_PROJECT:-}" ]]; then
      sentry_token_file="$(mktemp)"
      chmod 600 "$sentry_token_file"
      if gcloud secrets versions access latest --secret=nori-sentry-auth-token --project="$PROJECT" > "$sentry_token_file" 2>/dev/null && [[ -s "$sentry_token_file" ]]; then
        web_sentry_args+=(
          --build-arg "SENTRY_ORG=$SENTRY_ORG"
          --build-arg "SENTRY_PROJECT=$SENTRY_PROJECT"
          --secret "id=sentry_auth_token,src=$sentry_token_file"
        )
      else
        echo "::warning::Skipping Sentry source-map upload: nori-sentry-auth-token is not accessible or empty."
      fi
    fi

    log "Building $IMAGE"
    docker buildx build --platform linux/amd64 -f web/Dockerfile \
      --cache-from "type=registry,ref=$REGISTRY/nori-web:buildcache" \
      --cache-to "type=registry,ref=$REGISTRY/nori-web:buildcache,mode=max" \
      --build-arg "SENTRY_RELEASE=$SHA" \
      "${web_sentry_args[@]}" \
      -t "$IMAGE" --load .

    log "Checking container startup health for $IMAGE"
    cid="$(docker run -d -p 127.0.0.1:18080:8080 "$IMAGE")"
    ready=0
    for _ in $(seq 1 30); do
      if curl -fsS http://127.0.0.1:18080/api/health >/dev/null 2>&1; then
        ready=1
        break
      fi
      if ! docker inspect -f '{{.State.Running}}' "$cid" 2>/dev/null | grep -qx true; then
        break
      fi
      sleep 1
    done
    if [[ "$ready" -ne 1 ]]; then
      echo "nori-web container failed startup health check:" >&2
      docker logs "$cid" >&2 || true
      exit 1
    fi
    docker rm -f "$cid" >/dev/null
    cid=""

    log "Pushing $IMAGE"
    docker push "$IMAGE"
    ;;
  site)
    log "Building and pushing $IMAGE"
    docker buildx build --platform linux/amd64 -f site/Dockerfile \
      --cache-from "type=registry,ref=$REGISTRY/nori-site:buildcache" \
      --cache-to "type=registry,ref=$REGISTRY/nori-site:buildcache,mode=max" \
      -t "$IMAGE" --push site
    ;;
esac
