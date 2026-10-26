.PHONY: dev web core local-web local-core check

NORI_ENV_FILE ?= $(abspath .env)
NORI_TASK_ENV_FILE ?= $(NORI_ENV_FILE)
NORI_LOCAL_AGENT_URL ?= http://127.0.0.1:8080

dev:
	$(MAKE) -j2 local-web local-core

local-web:
	cd web && NORI_LOCAL_AGENT_URL="$(NORI_LOCAL_AGENT_URL)" bun run dev --port 3210

local-core:
	cd core && NORI_ENV_FILE="$(NORI_ENV_FILE)" NORI_TASK_ENV_FILE="$(NORI_TASK_ENV_FILE)" go run ./cmd/nori-local

web:
	cd web && bun --env-file=../.env run dev --port 3210

core:
	$(MAKE) -C core run

check:
	$(MAKE) -C core check
	cd web && ./node_modules/.bin/next typegen && ./node_modules/.bin/tsc --noEmit -p . && ./node_modules/.bin/eslint src
