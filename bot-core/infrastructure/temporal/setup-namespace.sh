#!/bin/sh
# Uses the vendor CLI for readiness, listing and creation; all failures propagate.
set -eu
: "${TEMPORAL_ADDRESS:?TEMPORAL_ADDRESS is required}"

attempt=1
until temporal operator cluster health --address "$TEMPORAL_ADDRESS"; do
  if [ "$attempt" -ge 30 ]; then
    echo "Temporal did not become healthy" >&2
    exit 1
  fi
  attempt=$((attempt + 1))
  sleep 2
done
temporal operator namespace list --address "$TEMPORAL_ADDRESS" --output json > /tmp/namespaces.json
if grep -Eq '"name"[[:space:]]*:[[:space:]]*"bot"' /tmp/namespaces.json; then
  echo "Namespace bot already exists"
else
  temporal operator namespace create --address "$TEMPORAL_ADDRESS" \
    --namespace bot --retention 3d
fi
