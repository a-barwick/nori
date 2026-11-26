#!/usr/bin/env bash
set -euo pipefail

project="${GCP_PROJECT:-bot-111549}"
region="${GCP_REGION:-us-central1}"
registry="$region-docker.pkg.dev/$project/${GCP_AR_REPO:-nori-poc}"
service="nori-web-mac"

usage() {
  echo "Usage: NORI_MAC_GOOGLE_CLIENT_ID=... $0 [--dry-run]"
}
case "${1:-}" in
  "") dry_run=0 ;;
  --dry-run) dry_run=1 ;;
  -h|--help) usage; exit 0 ;;
  *) usage >&2; exit 2 ;;
esac
[[ "$#" -le 1 ]] || { usage >&2; exit 2; }

: "${NORI_MAC_GOOGLE_CLIENT_ID:?Set the dedicated Google Desktop OAuth client ID}"
[[ "$NORI_MAC_GOOGLE_CLIENT_ID" =~ ^[a-zA-Z0-9_-]+\.apps\.googleusercontent\.com$ ]] || exit 2
cd "$(dirname "$0")/.."
if [[ "$dry_run" -eq 0 && -n "$(git status --porcelain -- web scripts/deploy-gcp-mac-web.sh)" ]]; then
  echo "Commit the Web source and deployment script before deploying." >&2
  exit 1
fi

sha="$(git rev-parse --short HEAD)"
image="$registry/nori-web:mac-$sha"
tag="release-$sha"
cloud() { gcloud run "$@" --project="$project" --region="$region"; }

if [[ "$dry_run" -eq 1 ]]; then
  echo "Build $image, grant the Mac service read access to the existing IAP policy, and deploy a tagged $service revision."
  echo "Verify health and anonymous rejection before promoting the tagged revision."
  exit 0
fi

# The runtime follows the current policy using a permission on this one IAP resource.
iap() {
  local command="$1"
  shift
  gcloud iap web "$command" --project="$project" --resource-type=cloud-run --service=nori-web --region="$region" "$@"
}
policy_resource="$(gcloud iap settings get --project="$project" --resource-type=cloud-run --service=nori-web --region="$region" --format='value(name)')"
[[ "$policy_resource" =~ ^projects/[0-9]+/iap_web/cloud_run-[a-z0-9-]+/services/nori-web$ ]]
iap get-iam-policy --format=json | jq -e '
  [.bindings[]? | select(.role=="roles/iap.httpsResourceAccessor")] as $access
  | ($access | length) > 0
    and all($access[]; (.condition == null and all(.members[]; test("^user:[^@]+@[^@]+$"))))' >/dev/null

account="$service@$project.iam.gserviceaccount.com"
role="noriMacIapPolicyReader"
permission="iap.webServices.getIamPolicy"
if ! gcloud iam service-accounts describe "$account" --project="$project" >/dev/null 2>&1; then
  gcloud iam service-accounts create "$service" --project="$project" --display-name="Nori Mac Web" --quiet
fi
if ! gcloud iam roles describe "$role" --project="$project" >/dev/null 2>&1; then
  gcloud iam roles create "$role" --project="$project" --title="Read Nori IAP access policy" --permissions="$permission" --stage=GA --quiet
fi
gcloud iam roles describe "$role" --project="$project" --format=json |
  jq -e --arg permission "$permission" '.includedPermissions == [$permission]' >/dev/null
iap add-iam-policy-binding --member="serviceAccount:$account" --role="projects/$project/roles/$role" --quiet >/dev/null

web="$(cloud services describe nori-web --format=json)"
read_environment() {
  jq -er --arg name "$1" '.spec.template.spec.containers[0].env[] | select(.name==$name) | .value' <<< "$web"
}
core="$(read_environment NORI_RUNTIME_URL)"
audience="$(read_environment NORI_SERVICE_AUDIENCE)"
token_secret="$(jq -er '.spec.template.spec.containers[0].env[] | select(.name=="NORI_SERVICE_TOKEN_KEY") | .valueFrom.secretKeyRef | "\(.name):\(.key)"' <<< "$web")"
gcloud secrets add-iam-policy-binding "${token_secret%:*}" --project="$project" \
  --member="serviceAccount:$account" --role=roles/secretmanager.secretAccessor --quiet >/dev/null

docker buildx build --platform linux/amd64 -f web/Dockerfile --build-arg "SENTRY_RELEASE=$sha" -t "$image" --push .
env_vars="^|^NORI_CLOUD=aws|NORI_AUTH_PROVIDER=oidc|NORI_OIDC_ISSUER=https://accounts.google.com|NORI_OIDC_AUDIENCE=$NORI_MAC_GOOGLE_CLIENT_ID|NORI_OIDC_IAP_POLICY_RESOURCE=$policy_resource|NORI_RUNTIME_URL=$core|NORI_SERVICE_AUDIENCE=$audience"
edge_token=""
if cloud services describe "$service" --format=json >/dev/null 2>&1; then
  traffic_flags=(--no-traffic)
else
  # Cloud Run requires initial traffic on new services. Keep the first revision
  # private while checking both application health and its authorization gate.
  traffic_flags=(--invoker-iam-check)
  edge_token="$(gcloud auth print-identity-token)"
fi
cloud deploy "$service" --image="$image" --service-account="$account" --ingress=all \
  "${traffic_flags[@]}" --min-instances=0 --max-instances=2 --tag="$tag" --quiet \
  --set-env-vars="$env_vars" --set-secrets="NORI_SERVICE_TOKEN_KEY=$token_secret"

candidate="$(cloud services describe "$service" --flatten='status.traffic[]' \
  --format='value(status.traffic.tag,status.traffic.revisionName,status.traffic.url)' | awk -F '\t' -v tag="$tag" '$1==tag {print $2 "\t" $3}')"
IFS=$'\t' read -r revision url <<< "$candidate"
status() {
  local curl_flags=(--silent --show-error --output /dev/null --write-out '%{http_code}' --max-time 15)
  if [[ -n "$edge_token" ]]; then
    printf 'X-Serverless-Authorization: Bearer %s\n' "$edge_token" | curl "${curl_flags[@]}" -H @- "$@"
  else
    curl "${curl_flags[@]}" "$@"
  fi
}
if [[ -z "$revision" || -z "$url" ]] ||
   [[ "$(status --retry 5 --retry-all-errors --fail "$url/api/health")" != 200 ]] ||
   [[ "$(status "$url/")" != 403 ]] ||
   [[ "$(status -H 'Authorization: Bearer invalid' "$url/")" != 403 ]]; then
  echo "The tagged Mac Web revision failed its health or authentication checks." >&2
  cloud services update-traffic "$service" --remove-tags="$tag" --quiet || true
  exit 1
fi

cloud services update-traffic "$service" --to-revisions="$revision=100" --remove-tags="$tag" --quiet
cloud services update "$service" --no-invoker-iam-check --quiet
edge_token=""
url="$(cloud services describe "$service" --format='value(status.url)')"
[[ "$(status --fail "$url/api/health")" == 200 && "$(status "$url/")" == 403 ]]
printf 'Mac Web is ready at %s\n' "$url"
