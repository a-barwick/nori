#!/bin/sh
# Build a static container image for Nori Dokyu from docs/site/.
# Pass an image tag as the first argument (defaults to nori-docs:local).
set -eu

image="${1:-nori-docs:local}"

cd "$(dirname "$0")"
public=$(mktemp -d)
trap 'rm -rf "$public"' EXIT
python3 publish.py "$public"
docker build --platform linux/amd64 -f Dockerfile -t "$image" "$public"
echo "Built $image"
