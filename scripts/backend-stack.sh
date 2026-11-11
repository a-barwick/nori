#!/usr/bin/env bash
# Backend session's local stack (core 8081, database nori_backend, namespace nori-backend,
# queue nori-tasks-backend). Holds no secret: project keys come from the main checkout's .env
# through NORI_ENV_FILE, which is read in place and never copied or printed.
#
#   scripts/backend-stack.sh setup     # once per machine: database and Temporal namespace (via the Compose containers)
#   scripts/backend-stack.sh users     # once per database: test users A and B
#   scripts/backend-stack.sh core      # API on 127.0.0.1:8081 (foreground)
#   scripts/backend-stack.sh worker    # task worker, build id be-<sha> (foreground); then:
#   scripts/backend-stack.sh current   # make the running worker's build current
#
# Stub mode: with NORI_MODEL_TEST_URL=http://127.0.0.1:<port> set, every model request goes to that loopback stub, the
# provider keys are replaced by a placeholder (they are empty in .env on this machine), and the intent classifier and
# hosted telemetry are off. Used to verify model timeouts (D-053) and prompt contents (D-054) without provider keys.
#
# Local only. It never talks to production or the hosted Temporal namespace: the Temporal key from
# .env is blanked and TEMPORAL_LOCAL=true points at the Compose dev server.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
export NORI_ENV_FILE="${NORI_ENV_FILE:-/Users/austin/Documents/dev/nori/.env}"
export NORI_CLOUD=local NORI_STORE_BACKEND=postgres
export NORI_POSTGRES_URL=postgres://nori@127.0.0.1:15432/nori_backend
export TEMPORAL_ADDRESS=127.0.0.1:17233 TEMPORAL_NAMESPACE=nori-backend TEMPORAL_LOCAL=true
export TEMPORAL_TASK_QUEUE=nori-tasks-backend TEMPORAL_API_KEY= TEMPORAL_API_KEY_SECRET_VERSION=
export NORI_COMPUTER_ENABLED=false NORI_BACKGROUND_TASKS_ENABLED=true NORI_LOCAL_ARTIFACT_DIR="$HOME/.nori-backend/artifacts"
export NORI_CREDENTIAL_STORE=local NORI_AUTOMATION_UNATTENDED=confirm_writes
export NORI_OAUTH_REDIRECT_URL=http://127.0.0.1:3211/api/v1/connections/oauth/callback
if [ -n "${NORI_MODEL_TEST_URL:-}" ]; then
	export OPENAI_API_KEY=stub-placeholder OPENROUTER_API_KEY=stub-placeholder NORI_JEV_ENABLED=false
	export ARIZE_API_KEY= SENTRY_DSN= NORI_USAGE_METER=store
fi
mkdir -p "$NORI_LOCAL_ARTIFACT_DIR"
build="${NORI_BUILD_ID:-be-$(git -C "$root" rev-parse --short HEAD)}"  # override only to drain tasks pinned to an older build
cd "$root/core"
case "${1:-}" in
setup)
	docker exec nori-local-postgres-1 psql -U nori -tc "SELECT 1 FROM pg_database WHERE datname='nori_backend'" | grep -q 1 ||
		docker exec nori-local-postgres-1 psql -U nori -c 'CREATE DATABASE nori_backend'
	docker exec nori-local-temporal-1 temporal operator namespace describe --address 127.0.0.1:7233 -n nori-backend >/dev/null 2>&1 ||
		docker exec nori-local-temporal-1 temporal operator namespace create --address 127.0.0.1:7233 -n nori-backend
	;;
users)
	go run ./cmd/nori-user -id 11111111-1111-4111-8111-111111111111 -email backend-a@nori.local -name "Backend A" -timezone Europe/London
	go run ./cmd/nori-user -id 22222222-2222-4222-8222-222222222222 -email backend-b@nori.local -name "Backend B" -timezone UTC
	;;
core) PORT=8081 exec go run ./cmd/nori-local ;;
worker)
	export PORT=  # an empty PORT keeps the worker out of hosted mode; .env sets one
	TEMPORAL_DEPLOYMENT_NAME=nori-task TEMPORAL_BUILD_ID="$build" exec go run ./cmd/nori-worker
	;;
current)
	docker exec nori-local-temporal-1 temporal worker deployment set-current-version --address 127.0.0.1:7233 \
		-n nori-backend --deployment-name nori-task --build-id "$build" --yes
	;;
*) sed -n 2,13p "$0"; exit 2 ;;
esac
