#!/bin/sh
# Builds docs/site into an image and deploys it to the nori-docs Cloud Run service.
# The service is public: anyone can read the site without signing in.
set -eu

project=bot-111549
region=us-central1
service=nori-docs
account="$service@$project.iam.gserviceaccount.com"
image="$region-docker.pkg.dev/$project/nori-poc/docs:$(date -u +%Y%m%d-%H%M%S)"

cd "$(dirname "$0")"
# A page that differs from the published site must pass every check.
python3 ../.agents/skills/review-docs/scripts/check.py --live
public=$(mktemp -d)
trap 'rm -rf "$public"' EXIT
python3 agent_map.py publish "$public"
# The privacy policy and terms the Google sign-in screen links to. They aren't docs pages.
cp -R legal "$public/legal"
docker build --platform linux/amd64 -f Dockerfile -t "$image" "$public"
docker push "$image"

# The service account has no roles: serving static files needs none.
gcloud iam service-accounts describe "$account" --project "$project" >/dev/null 2>&1 ||
  gcloud iam service-accounts create "$service" --project "$project" --display-name "Nori docs"

gcloud run deploy "$service" \
  --project "$project" --region "$region" --image "$image" \
  --service-account "$account" --port 8080 \
  --cpu 1 --memory 256Mi --min-instances 0 --max-instances 1 \
  --allow-unauthenticated --no-iap --quiet
