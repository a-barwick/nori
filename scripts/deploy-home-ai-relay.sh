#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "Usage: NORI_HOME_AI_RELAY_ACCOUNT=... NORI_HOME_AI_RELAY_DB_SECRET=... NORI_HOME_AI_KEY_SECRET=... NORI_HOME_AI_VERIFY_URL=... $0 [--dry-run]"
}

dry_run=0
case "${1:-}" in
  "") ;;
  --dry-run) dry_run=1 ;;
  -h|--help) usage; exit 0 ;;
  *) usage >&2; exit 2 ;;
esac
if (( $# > 1 )); then
  usage >&2
  exit 2
fi

: "${NORI_HOME_AI_RELAY_ACCOUNT:?Set the relay service account}"
: "${NORI_HOME_AI_RELAY_DB_SECRET:?Set the Secret Manager name and version containing the relay Postgres URL}"
: "${NORI_HOME_AI_KEY_SECRET:?Set the Secret Manager name and version containing the relay encryption key}"
: "${NORI_HOME_AI_VERIFY_URL:?Set the public Nori pairing page URL}"

cd "$(dirname "$0")/.."
project="${GCP_PROJECT:-bot-111549}"
region="${GCP_REGION:-us-central1}"
repo="${GCP_AR_REPO:-nori-poc}"
service="${NORI_HOME_AI_RELAY_SERVICE:-nori-connector-relay}"
sha="$(git rev-parse --short HEAD)"
if [[ -n "$(git status --porcelain -- core scripts/deploy-home-ai-relay.sh)" && "$dry_run" != 1 ]]; then
  echo "Commit the relay changes before deploying." >&2
  exit 1
fi

image="$region-docker.pkg.dev/$project/$repo/nori-connector-relay:$sha"
tag="release-$sha"
run() {
  if [[ "$dry_run" == 1 ]]; then
    printf '%q ' "$@"
    printf '\n'
  else
    "$@"
  fi
}

run docker buildx build --platform linux/amd64 -f core/infrastructure/ConnectorRelay.Dockerfile --tag "$image" --push core
cloud() { gcloud run "$@" --project="$project" --region="$region"; }
relay_secrets="NORI_HOME_AI_POSTGRES_URL=$NORI_HOME_AI_RELAY_DB_SECRET,NORI_HOME_AI_KEY=$NORI_HOME_AI_KEY_SECRET"
if [[ -n "${NORI_HOME_AI_RELAY_DB_LISTEN_SECRET:-}" ]]; then
  relay_secrets+=",NORI_HOME_AI_POSTGRES_LISTEN_URL=$NORI_HOME_AI_RELAY_DB_LISTEN_SECRET"
fi
edge_token=""
if [[ "$dry_run" == 1 ]] || cloud services describe "$service" --format=json >/dev/null 2>&1; then
  traffic_flags=(--no-traffic)
else
  traffic_flags=(--invoker-iam-check)
  edge_token="$(gcloud auth print-identity-token)"
fi
args=(
  --image="$image" "${traffic_flags[@]}" --tag="$tag" --quiet
  --service-account="$NORI_HOME_AI_RELAY_ACCOUNT"
  --timeout=3600 --concurrency="${NORI_HOME_AI_RELAY_CONCURRENCY:-80}" --max-instances="${NORI_HOME_AI_RELAY_MAX_INSTANCES:-4}"
  --update-secrets="$relay_secrets"
  --update-env-vars="NORI_HOME_AI_VERIFY_URL=$NORI_HOME_AI_VERIFY_URL"
)
if [[ -n "${NORI_HOME_AI_CLOUD_SQL_INSTANCE:-}" ]]; then
  args+=(--add-cloudsql-instances="$NORI_HOME_AI_CLOUD_SQL_INSTANCE")
fi
run cloud deploy "$service" "${args[@]}"
if [[ "$dry_run" == 1 ]]; then
  echo "Probe the tagged /readyz and promote its revision."
  exit 0
fi

candidate="$( cloud services describe "$service" --flatten='status.traffic[]' \
  --format='value(status.traffic.tag,status.traffic.revisionName,status.traffic.url)' |
  awk -F '\t' -v tag="$tag" '$1 == tag { print $2 "\t" $3 }')"
IFS=$'\t' read -r revision url <<< "$candidate"
probe_ready() {
  local flags=(--silent --show-error --fail --output /dev/null --write-out '%{http_code}' --max-time 15 --retry 5 --retry-all-errors)
  local code
  if [[ -n "$edge_token" ]]; then
    code="$(printf 'X-Serverless-Authorization: Bearer %s\n' "$edge_token" | curl "${flags[@]}" -H @- "$1")" || return 1
  else
    code="$(curl "${flags[@]}" "$1")" || return 1
  fi
  [[ "$code" == 200 ]]
}
if [[ -z "$revision" || -z "$url" ]] || ! probe_ready "$url/readyz"; then
  cloud services update-traffic "$service" --remove-tags="$tag" --quiet || true
  echo "Connector relay readiness failed." >&2
  exit 1
fi
cloud services update-traffic "$service" --to-revisions="$revision=100" --remove-tags="$tag" --quiet
cloud services update "$service" --no-invoker-iam-check --quiet
edge_token=""
service_url="$(cloud services describe "$service" --format='value(status.url)')"
probe_ready "$service_url/readyz"
