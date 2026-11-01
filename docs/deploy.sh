#!/bin/sh
# Builds docs/site into an image and deploys it to the nori-docs Cloud Run service.
# The service sits behind IAP, so only accounts granted access can read it.
set -eu

project=bot-111549
region=us-central1
service=nori-docs
account="$service@$project.iam.gserviceaccount.com"
image="$region-docker.pkg.dev/$project/nori-poc/docs:$(date -u +%Y%m%d-%H%M%S)"

cd "$(dirname "$0")"
docker build --platform linux/amd64 -f Dockerfile -t "$image" site
docker push "$image"

# The service account has no roles: serving static files needs none.
gcloud iam service-accounts describe "$account" --project "$project" >/dev/null 2>&1 ||
  gcloud iam service-accounts create "$service" --project "$project" --display-name "Nori docs"

gcloud run deploy "$service" \
  --project "$project" --region "$region" --image "$image" \
  --service-account "$account" --port 8080 \
  --cpu 1 --memory 256Mi --min-instances 0 --max-instances 1 \
  --no-allow-unauthenticated --iap --quiet
