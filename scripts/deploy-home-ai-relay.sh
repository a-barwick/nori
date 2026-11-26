#!/usr/bin/env bash
set -euo pipefail

# Ships a new relay image. infra/run.tf and infra/home_ai_edge.tf own the relay's
# service account, ingress, scaling, secrets, load balancer and Cloud Armor policy.

usage() {
  echo "Build the Home AI relay image at HEAD, stage it with no traffic, and promote it after it passes"
  echo "its startup probe and answers /readyz through the load balancer."
  echo "Usage: $0 [--dry-run]"
}

dry_run=0
case "${1:-}" in
  "") ;;
  --dry-run) dry_run=1 ;;
  -h|--help) usage; exit 0 ;;
  *) usage >&2; exit 2 ;;
esac
[[ "$#" -le 1 ]] || { usage >&2; exit 2; }

project="${GCP_PROJECT:-bot-111549}"
region="${GCP_REGION:-us-central1}"
repo="${GCP_AR_REPO:-nori-poc}"
service="nori-connector-relay"
public_origin="${NORI_HOME_AI_RELAY_PUBLIC_ORIGIN:-https://relay.noribot.chat}"

cd "$(dirname "$0")/.."
sha="$(git rev-parse --short HEAD)"
if [[ "$dry_run" != 1 && -n "$(git status --porcelain -- core scripts/deploy-home-ai-relay.sh)" ]]; then
  echo "Commit the relay changes before deploying." >&2
  exit 1
fi

image="$region-docker.pkg.dev/$project/$repo/nori-connector-relay:$sha"
tag="release-$sha"
cloud() { gcloud run "$@" --project="$project" --region="$region"; }

if [[ "$dry_run" == 1 ]]; then
  printf '%q ' docker buildx build --platform linux/amd64 -f core/infrastructure/ConnectorRelay.Dockerfile --tag "$image" --push core
  printf '\n'
  printf '%q ' gcloud run deploy "$service" --image="$image" --no-traffic --tag="$tag" --quiet --project="$project" --region="$region"
  printf '\n'
  echo "Check the tagged revision is Ready, promote it, and verify $public_origin/readyz."
  exit 0
fi

previous="$(cloud services describe "$service" --format=json |
  jq -er '[.status.traffic[] | select(.percent>0) | "\(.revisionName)=\(.percent)"] | join(",")')"

docker buildx build --platform linux/amd64 -f core/infrastructure/ConnectorRelay.Dockerfile --tag "$image" --push core
cloud deploy "$service" --image="$image" --no-traffic --tag="$tag" --quiet

revision="$(cloud services describe "$service" --format=json |
  jq -er --arg tag "$tag" '.status.traffic[] | select(.tag==$tag) | .revisionName')"
if ! cloud revisions describe "$revision" --format=json |
  jq -e 'any(.status.conditions[]; .type=="Ready" and .status=="True")' >/dev/null; then
  cloud services update-traffic "$service" --remove-tags="$tag" --quiet || true
  echo "The tagged relay revision failed readiness." >&2
  exit 1
fi

cloud services update-traffic "$service" --to-revisions="$revision=100" --remove-tags="$tag" --quiet
if [[ "$(curl --silent --show-error --fail --output /dev/null --write-out '%{http_code}' --max-time 15 --retry 5 --retry-all-errors "$public_origin/readyz" || true)" != 200 ]]; then
  cloud services update-traffic "$service" --to-revisions="$previous" --quiet
  echo "Relay health failed through the load balancer." >&2
  exit 1
fi
printf 'Relay %s is serving at %s\n' "$revision" "$public_origin"
