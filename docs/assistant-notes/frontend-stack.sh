#!/usr/bin/env bash
# Frontend session's local stack (no secrets in this file). Default: real models and real providers, no recordings.
# NORI_FE_RECORDED=1 switches to the hand-written provider recordings with a made-up Google client.
# Core 8082, web 3212, database nori_frontend, Temporal namespace nori-frontend, task queue nori-tasks-frontend.
# Project keys come from the main checkout's .env through NORI_ENV_FILE; it is read, never copied or printed.
# The web signs in locally as a test identity, never a real address.
#
# Usage (from the worktree root):  docs/assistant-notes/frontend-stack.sh [user|core|worker|web|current|stop|all]
# Logs, the dev vault and artifacts go to $NORI_FE_LOGS (default: $TMPDIR/nori-frontend-stack), outside the repository.
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
env_file="${NORI_ENV_FILE:-/Users/austin/Documents/dev/nori/.env}"
logs="${NORI_FE_LOGS:-${TMPDIR:-/tmp}/nori-frontend-stack}"
# Local test identity (never a real address). The real-provider walk of 2026-11-10 uses a fresh one with no
# recorded connections: NORI_FE_EMAIL=frontend.real@example.com.
email="${NORI_FE_EMAIL:-frontend.test@example.com}"
build="${TEMPORAL_BUILD_ID:-fe-$(git -C "$root" rev-parse --short HEAD)}"
mkdir -p "$logs" "$logs/vault" "$logs/artifacts"

common=(
  NORI_ENV_FILE="$env_file" NORI_TASK_ENV_FILE="$env_file"
  NORI_CLOUD=local NORI_STORE_BACKEND=postgres
  NORI_POSTGRES_URL="postgres://nori@127.0.0.1:15432/nori_frontend?sslmode=disable"
  TEMPORAL_ADDRESS=127.0.0.1:17233 TEMPORAL_LOCAL=true
  TEMPORAL_NAMESPACE=nori-frontend TEMPORAL_TASK_QUEUE=nori-tasks-frontend TEMPORAL_DEPLOYMENT_NAME=nori-task
  TEMPORAL_BUILD_ID="$build"
  NORI_CREDENTIAL_STORE=local NORI_CREDENTIAL_DIR="$logs/vault"
  NORI_LOCAL_ARTIFACT_DIR="$logs/artifacts"
  NORI_OAUTH_REDIRECT_URL="http://127.0.0.1:3212/api/v1/connections/oauth/callback"
  NORI_AUTOMATION_UNATTENDED=confirm_writes
  NORI_BACKGROUND_TASKS_ENABLED=true
)
if [[ -n "${NORI_FE_RECORDED:-}" ]]; then
  # Replay answers Google's token exchange, so the OAuth client only has to be non-empty here. These are not credentials.
  common+=(NORI_PROVIDER_REPLAY="$root/contracts/assistant/recordings"
           GOOGLE_CALENDAR_CLIENT_ID=recorded-client-id GOOGLE_CALENDAR_CLIENT_SECRET=recorded-client-secret)
else
  # Real providers: the Google client comes from the env file. An empty-but-set value keeps .env's replay setting out.
  common+=(NORI_PROVIDER_REPLAY=)
fi
# Model keys come from the env file (restored there on 2026-11-10). Without them, NORI_FE_NO_MODEL=1 starts with a
# placeholder so screens load (chat replies and tasks then fail at the model call).
if [[ -n "${NORI_FE_NO_MODEL:-}" ]]; then common+=(OPENAI_API_KEY=no-model-key-placeholder OPENROUTER_API_KEY=no-model-key-placeholder); fi

core()   { (cd "$root/core" && env "${common[@]}" PORT=8082 nohup go run ./cmd/nori-local >"$logs/core.log" 2>&1 &) ; }
# PORT must be empty for the worker (polling mode); an empty-but-set variable keeps .env's PORT out.
worker() { (cd "$root/core" && env "${common[@]}" PORT= nohup go run ./cmd/nori-worker >"$logs/worker.log" 2>&1 &) ; }
# One-time: the local test user the web signs in as (time zone matches the first run's test user).
user()   { (cd "$root/core" && env "${common[@]}" go run ./cmd/nori-user -email "$email" -name "Frontend Test" -timezone America/Los_Angeles) ; }
current() {
  docker exec nori-local-temporal-1 temporal worker deployment set-current-version \
    --address 127.0.0.1:7233 --namespace nori-frontend --deployment-name nori-task --build-id "$build" --yes
}
web()    { (cd "$root/web" && NORI_LOCAL_AGENT_URL=http://127.0.0.1:8082 NORI_LOCAL_EMAIL="$email" \
            nohup node ../scripts/run-with-env.mjs "$env_file" bun --no-env-file run dev --port 3212 >"$logs/web.log" 2>&1 &) ; }

# Stops only this stack: processes started with namespace nori-frontend, and the listeners on 8082 and 3212.
# Never stop by program name; other sessions run their own cores and workers on this machine.
stop() {
  local pids
  pids="$(for p in $(pgrep -f 'nori-local|nori-worker' || true); do
            ps eww -p "$p" -o command= 2>/dev/null | grep -q 'TEMPORAL_NAMESPACE=nori-frontend ' && echo "$p"; done)
        $(lsof -tiTCP:8082 -sTCP:LISTEN || true) $(lsof -tiTCP:3212 -sTCP:LISTEN || true)"
  [[ -n "${pids//[[:space:]]/}" ]] && kill $pids 2>/dev/null || true
}

case "${1:-all}" in
  user) user ;; core) core ;; worker) worker ;; web) web ;; current) current ;; stop) stop ;;
  all) core; worker; web; echo "build $build: once the worker log shows it polling, run: $0 current" ;;
esac
