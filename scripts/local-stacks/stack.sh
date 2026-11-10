#!/usr/bin/env bash
# Local assistant stacks for integration work. Holds no secret: project keys are read from NORI_ENV_FILE by the
# processes themselves (process environment wins over the file), and nothing here prints a value.
#
#   scripts/local-stacks/stack.sh <recorded|real> <setup|users|core|worker|set-current|web|check|status>
#
# recorded: core 8083, database nori_integration, namespace nori-integration, hand-written provider recordings.
# real:     core 8085, database nori_real, namespace nori-real, local file vault, no replay, web 3213 -> 8085.
#
# core, worker and web run in the foreground; start each in its own terminal or as a background job.
# See scripts/local-stacks/README.md.
set -euo pipefail

stack=${1:?usage: stack.sh <recorded|real> <command>}
cmd=${2:?usage: stack.sh <recorded|real> <command>}

root=$(cd "$(dirname "$0")/../.." && pwd)
# The main checkout holds .env; worktrees share its git directory.
main_checkout=$(cd "$(git -C "$root" rev-parse --path-format=absolute --git-common-dir)/.." && pwd)
export NORI_ENV_FILE=${NORI_ENV_FILE:-$main_checkout/.env}
export NORI_TASK_ENV_FILE=$NORI_ENV_FILE
stack_home=${NORI_STACK_HOME:-$HOME/.nori-stacks}
# Optional extra mode-600 env file for this stack (e.g. a VAPID pair kept outside .env); never committed.
extra_env=$stack_home/$stack.env

case "$stack" in
recorded)
	port=8083 db=nori_integration ns=nori-integration queue=nori-tasks-integration web_port=3214
	build_prefix=int
	export NORI_PROVIDER_REPLAY=$root/contracts/assistant/recordings
	# Made-up client: the recordings accept any client, and nothing reaches Google.
	export GOOGLE_CALENDAR_CLIENT_ID=recorded-google-client GOOGLE_CALENDAR_CLIENT_SECRET=recorded-google-secret
	;;
real)
	port=8085 db=nori_real ns=nori-real queue=nori-tasks-real web_port=3213
	build_prefix=real
	export NORI_PROVIDER_REPLAY=
	;;
*)
	echo "unknown stack: $stack" >&2
	exit 2
	;;
esac

artifacts=$stack_home/$stack/artifacts
export NORI_CLOUD=local NORI_STORE_BACKEND=postgres
export NORI_POSTGRES_URL=postgres://nori@127.0.0.1:15432/$db
export TEMPORAL_ADDRESS=127.0.0.1:17233 TEMPORAL_NAMESPACE=$ns TEMPORAL_LOCAL=true TEMPORAL_TASK_QUEUE=$queue
export TEMPORAL_API_KEY= TEMPORAL_API_KEY_SECRET_VERSION= TEMPORAL_DEPLOYMENT_NAME=nori-task
# The project .env may be chat-only (NORI_BACKGROUND_TASKS_ENABLED=false); the stacks need the task engine.
export NORI_BACKGROUND_TASKS_ENABLED=true NORI_COMPUTER_ENABLED=false NORI_LOCAL_ARTIFACT_DIR=$artifacts
export NORI_CREDENTIAL_STORE=local NORI_CREDENTIAL_DIR=$artifacts/credentials
export NORI_OAUTH_REDIRECT_URL=http://127.0.0.1:$web_port/api/v1/connections/oauth/callback
# NORI_STACK_NO_MODEL=1: when the env file has no model keys, start anyway with placeholder keys and send all outbound
# HTTP(S) to a dead local proxy, so nothing leaves the machine (no model, no provider). API scenarios run; model turns
# and real provider calls fail. Use it only to check the plumbing; it is never evidence about a model or provider.
if [[ ${NORI_STACK_NO_MODEL:-} == 1 ]]; then
	for key in OPENAI_API_KEY OPENROUTER_API_KEY; do
		if ! grep -Eq "^$key=.+" "$NORI_ENV_FILE"; then export "$key=placeholder-no-model"; fi
	done
	export HTTPS_PROXY=http://127.0.0.1:9 HTTP_PROXY=http://127.0.0.1:9 NO_PROXY=127.0.0.1,localhost
fi
# Personal identifiers never appear here: the web signs in as a local test user.
case "$stack" in
recorded) web_email=integration-a@nori.local ;;
real) web_email=real-a@nori.local ;;
esac

load_extra() {
	if [[ -f $extra_env ]]; then
		set -a
		# shellcheck disable=SC1090
		. "$extra_env"
		set +a
	fi
}

temporal_cli() { docker exec nori-local-temporal-1 temporal "$@" --address 127.0.0.1:7233; }
build_id() { echo "$build_prefix-$(git -C "$root" rev-parse --short HEAD)"; }

case "$cmd" in
setup)
	mkdir -p "$artifacts/credentials"
	chmod 700 "$stack_home" "$stack_home/$stack" "$artifacts" "$artifacts/credentials"
	if ! docker exec nori-local-postgres-1 psql -U nori -d nori -Atc "select 1 from pg_database where datname='$db'" | grep -q 1; then
		docker exec nori-local-postgres-1 psql -U nori -d nori -c "CREATE DATABASE $db"
	fi
	temporal_cli operator namespace describe -n "$ns" >/dev/null 2>&1 || temporal_cli operator namespace create -n "$ns"
	echo "setup done: database $db, namespace $ns, artifacts $artifacts"
	;;
users)
	cd "$root/core"
	user() { go run ./cmd/nori-user "$@"; }
	case "$stack" in
	recorded)
		user -id 11111111-1111-4111-8111-111111111111 -email integration-a@nori.local -name "Integration A" -timezone America/Los_Angeles
		user -id 22222222-2222-4222-8222-222222222222 -email integration-b@nori.local -name "Integration B" -timezone UTC
		user -id 44444444-4444-4444-8444-444444444444 -email scenario-a@nori.local -name "Scenario A" -timezone UTC
		user -id 55555555-5555-4555-8555-555555555555 -email scenario-b@nori.local -name "Scenario B" -timezone UTC
		;;
	real)
		user -id 66666666-6666-4666-8666-666666666666 -email real-a@nori.local -name "Real A" -timezone America/Los_Angeles
		;;
	esac
	;;
core)
	load_extra
	cd "$root/core"
	PORT=$port exec go run ./cmd/nori-local
	;;
worker)
	load_extra
	cd "$root/core"
	# An empty PORT (not unset: .env sets one) keeps the worker out of hosted mode, which demands NORI_WORKER_SERVICES.
	export PORT=
	TEMPORAL_BUILD_ID=$(build_id) exec go run ./cmd/nori-worker
	;;
set-current)
	temporal_cli worker deployment set-current-version -n "$ns" --deployment-name nori-task --build-id "${3:-$(build_id)}" --yes
	;;
web)
	cd "$root/web"
	NORI_LOCAL_AGENT_URL=http://127.0.0.1:$port NORI_LOCAL_EMAIL=$web_email \
		exec node ../scripts/run-with-env.mjs "$NORI_ENV_FILE" bun --no-env-file run dev --port "$web_port"
	;;
check)
	cd "$root/core"
	if [[ $stack == recorded ]]; then
		go run ./cmd/contracts-check -live "http://127.0.0.1:$port" -recorded
	else
		go run ./cmd/contracts-check -live "http://127.0.0.1:$port"
	fi
	;;
stop)
	# Only this stack's processes: listeners on its ports and workers polling its namespace. Other sessions run
	# their own stacks on this machine, so never stop by program name.
	pids=$(lsof -nP -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null; lsof -nP -tiTCP:"$web_port" -sTCP:LISTEN 2>/dev/null) || true
	for pid in $(pgrep -f nori-worker || true); do
		if ps eww -p "$pid" | grep -q "TEMPORAL_NAMESPACE=$ns "; then pids="$pids $pid"; fi
	done
	if [[ -n ${pids// /} ]]; then
		# shellcheck disable=SC2086
		kill $pids && echo "stopped: $pids"
	else
		echo "nothing running for $stack"
	fi
	;;
status)
	for p in $port $web_port; do
		printf '%s ' "$p"
		curl -s -o /dev/null -w '%{http_code}\n' "http://127.0.0.1:$p/" || echo down
	done
	temporal_cli worker deployment describe --name nori-task -n "$ns" 2>/dev/null | grep -i "current" || true
	;;
*)
	echo "unknown command: $cmd" >&2
	exit 2
	;;
esac
