#!/bin/sh
set -eu

image="${1:-nori-docs:local}"

cd "$(dirname "$0")"
public=$(mktemp -d)
trap 'rm -rf "$public"' EXIT
python3 publish.py "$public"
docker build --platform linux/amd64 -f Dockerfile -t "$image" "$public"
echo "Built $image"
