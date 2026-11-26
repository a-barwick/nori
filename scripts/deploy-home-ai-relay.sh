#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "Set GCP_PROJECT, GCP_AR_REPO, NORI_HOME_AI_RELAY_ACCOUNT, NORI_HOME_AI_RELAY_DB_SECRET, NORI_HOME_AI_KEY_SECRET,"
  echo "NORI_HOME_AI_VERIFY_URL, NORI_HOME_AI_RELAY_PUBLIC_ORIGIN and NORI_HOME_AI_RELAY_EDGE_BACKEND."
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

: "${NORI_HOME_AI_RELAY_ACCOUNT:?Set the relay service account}"
: "${NORI_HOME_AI_RELAY_DB_SECRET:?Set the Secret Manager name and version containing the relay Postgres URL}"
: "${NORI_HOME_AI_KEY_SECRET:?Set the Secret Manager name and version containing the relay encryption key}"
: "${NORI_HOME_AI_VERIFY_URL:?Set the public Nori pairing page URL}"
: "${NORI_HOME_AI_RELAY_PUBLIC_ORIGIN:?Set the relay load balancer HTTPS origin}"
: "${NORI_HOME_AI_RELAY_EDGE_BACKEND:?Set the global load balancer backend with a Cloud Armor policy}"
public_origin="$NORI_HOME_AI_RELAY_PUBLIC_ORIGIN"
if ! [[ "$public_origin" =~ ^https://[A-Za-z0-9-]+([.][A-Za-z0-9-]+)+$ ]] || [[ "$public_origin" == *.run.app ]]; then
  echo "Use the relay load balancer's HTTPS origin." >&2
  exit 2
fi

cd "$(dirname "$0")/.."
: "${GCP_PROJECT:?Set GCP_PROJECT to the target GCP project ID}"
: "${GCP_AR_REPO:?Set GCP_AR_REPO to the target Artifact Registry repository name}"
project="$GCP_PROJECT"
region="${GCP_REGION:-us-central1}"
repo="$GCP_AR_REPO"
service="${NORI_HOME_AI_RELAY_SERVICE:-nori-connector-relay}"
max_instances="${NORI_HOME_AI_RELAY_MAX_INSTANCES:-1}"
if ! [[ "$max_instances" =~ ^[1-9][0-9]*$ ]]; then
  echo "NORI_HOME_AI_RELAY_MAX_INSTANCES must be a positive integer." >&2
  exit 2
fi
sha="$(git rev-parse --short HEAD)"
if [[ -n "$(git status --porcelain -- core scripts/deploy-home-ai-relay.sh)" && "$dry_run" != 1 ]]; then
  echo "Commit the relay changes before deploying." >&2
  exit 1
fi

image="$region-docker.pkg.dev/$project/$repo/nori-connector-relay:$sha"
tag="release-$sha"
cloud() { gcloud run "$@" --project="$project" --region="$region"; }
compute_resource() {
  gcloud compute "$@" --project="$project" --format=json |
    jq -ce 'if type == "array" then select(length == 1) | .[0] else . end | select(type == "object")'
}

if [[ "$dry_run" != 1 ]]; then
  backend="$(compute_resource backend-services describe "$NORI_HOME_AI_RELAY_EDGE_BACKEND" --global)"
  security_policy="$(jq -er '.securityPolicy | split("/") | last' <<< "$backend")"
  group="$(jq -er 'select(.loadBalancingScheme=="EXTERNAL_MANAGED" or .loadBalancingScheme=="EXTERNAL") | .backends | select(length==1) | .[0].group' <<< "$backend")"
  [[ "$group" == */regions/"$region"/networkEndpointGroups/* ]]
  compute_resource network-endpoint-groups describe "${group##*/}" --region="$region" |
    jq -e --arg service "$service" '.networkEndpointType == "SERVERLESS" and .cloudRun.service == $service and (.cloudRun.tag // "") == ""' >/dev/null
  compute_resource security-policies describe "$security_policy" |
    jq -e '
      [.rules[] | select(.preview != true and .priority < 2147483647)] | sort_by(.priority) | first
      | (.action == "throttle" or .action == "rate_based_ban")
        and .match.versionedExpr == "SRC_IPS_V1"
        and (.match.config.srcIpRanges | index("*") != null)
        and .rateLimitOptions.enforceOnKey == "IP"
        and .rateLimitOptions.conformAction == "allow"
        and .rateLimitOptions.exceedAction == "deny(429)"
        and (.rateLimitOptions.rateLimitThreshold | .count > 0 and .intervalSec > 0 and .count <= 10 * .intervalSec)
    ' >/dev/null || { echo "The relay backend needs an active per-IP Cloud Armor throttle for every route." >&2; exit 1; }
  gcloud beta run domain-mappings list --project="$project" --region="$region" --format=json |
    jq -e --arg service "$service" 'all(.[]; .spec.routeName != $service)' >/dev/null ||
    { echo "Remove direct Cloud Run domain mappings from the relay before deploying." >&2; exit 1; }
fi

relay_secrets="NORI_HOME_AI_POSTGRES_URL=$NORI_HOME_AI_RELAY_DB_SECRET,NORI_HOME_AI_KEY=$NORI_HOME_AI_KEY_SECRET"
if [[ -n "${NORI_HOME_AI_RELAY_DB_LISTEN_SECRET:-}" ]]; then
  relay_secrets+=",NORI_HOME_AI_POSTGRES_LISTEN_URL=$NORI_HOME_AI_RELAY_DB_LISTEN_SECRET"
fi

traffic_flags=(--invoker-iam-check)
previous=""
if [[ "$dry_run" == 1 ]] || cloud services describe "$service" --format=json >/dev/null 2>&1; then
  traffic_flags=(--no-traffic)
  if [[ "$dry_run" != 1 ]]; then
    previous="$(cloud services describe "$service" --format=json |
      jq -er '[.status.traffic[] | select(.percent>0) | "\(.revisionName)=\(.percent)"] | join(",")')"
  fi
fi

args=(
  --image="$image" "${traffic_flags[@]}" --tag="$tag" --quiet
  --ingress=internal-and-cloud-load-balancing --no-default-url
  --service-account="$NORI_HOME_AI_RELAY_ACCOUNT"
  --timeout=3600 --concurrency="${NORI_HOME_AI_RELAY_CONCURRENCY:-40}"
  --max-instances="$max_instances"
  "--startup-probe=httpGet.path=/readyz,periodSeconds=3,timeoutSeconds=3,failureThreshold=40"
  --update-secrets="$relay_secrets"
  --update-env-vars="NORI_HOME_AI_VERIFY_URL=$NORI_HOME_AI_VERIFY_URL,NORI_HOME_AI_MAX_CONNS=${NORI_HOME_AI_RELAY_PG_MAX_CONNS:-4}"
)
if [[ -n "${NORI_HOME_AI_CLOUD_SQL_INSTANCE:-}" ]]; then
  args+=(--add-cloudsql-instances="$NORI_HOME_AI_CLOUD_SQL_INSTANCE")
fi

if [[ "$dry_run" == 1 ]]; then
  echo "Verify the serverless backend and Cloud Armor policy for $public_origin."
  printf '%q ' docker buildx build --platform linux/amd64 -f core/infrastructure/ConnectorRelay.Dockerfile --tag "$image" --push core
  printf '\n'
  printf '%q ' gcloud run deploy "$service" "${args[@]}" --project="$project" --region="$region"
  printf '\n'
  echo "Verify the tagged revision's startup probe, enable invocation through the load balancer, and check /readyz."
  exit 0
fi

docker buildx build --platform linux/amd64 -f core/infrastructure/ConnectorRelay.Dockerfile --tag "$image" --push core
cloud deploy "$service" "${args[@]}"

revision="$(cloud services describe "$service" --format=json |
  jq -er --arg tag "$tag" '.status.traffic[] | select(.tag==$tag) | .revisionName')"
if ! cloud revisions describe "$revision" --format=json |
  jq -e 'any(.status.conditions[]; .type=="Ready" and .status=="True")' >/dev/null; then
  cloud services update-traffic "$service" --remove-tags="$tag" --quiet || true
  echo "The tagged relay revision failed readiness." >&2
  exit 1
fi

cloud services update-traffic "$service" --to-revisions="$revision=100" --remove-tags="$tag" --quiet
cloud services update "$service" --no-invoker-iam-check --quiet
if [[ "$(curl --silent --show-error --fail --output /dev/null --write-out '%{http_code}' --max-time 15 --retry 5 --retry-all-errors "$public_origin/readyz" || true)" != 200 ]]; then
  if [[ -n "$previous" ]]; then
    cloud services update-traffic "$service" --to-revisions="$previous" --quiet
  else
    cloud services update "$service" --invoker-iam-check --quiet
  fi
  echo "Relay health failed through the load balancer." >&2
  exit 1
fi
