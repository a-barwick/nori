.PHONY: dev web web-build web-start core local-web local-core check sim-deep sim-pg sim-personas-validate sim-personas-smoke sim-personas-memory sim-personas-full deps deps-stop

NORI_ENV_FILE ?= $(abspath .env)
NORI_TASK_ENV_FILE ?= $(NORI_ENV_FILE)
NORI_LOCAL_AGENT_URL ?= http://127.0.0.1:8080
NORI_WEB_PORT ?= 3210
NORI_WEB_DEV_ARGS ?=

dev:
	$(MAKE) -j2 local-web local-core

local-web:
	cd web && NORI_LOCAL_AGENT_URL="$(NORI_LOCAL_AGENT_URL)" node ../scripts/run-with-env.mjs "$(NORI_ENV_FILE)" bun --no-env-file run dev $(NORI_WEB_DEV_ARGS) --port $(NORI_WEB_PORT)

local-core:
	cd core && NORI_ENV_FILE="$(NORI_ENV_FILE)" NORI_TASK_ENV_FILE="$(NORI_TASK_ENV_FILE)" go run ./cmd/nori-local

web:
	cd web && node ../scripts/run-with-env.mjs "$(NORI_ENV_FILE)" bun --no-env-file run dev $(NORI_WEB_DEV_ARGS) --port $(NORI_WEB_PORT)

web-build:
	cd web && node ../scripts/run-with-env.mjs "$(NORI_ENV_FILE)" bun --no-env-file run build

web-start:
	cd web && node ../scripts/run-with-env.mjs "$(NORI_ENV_FILE)" bun --no-env-file run start

core:
	$(MAKE) -C core run

check:
	$(MAKE) -C core check
	cd core && go run ./cmd/contracts-check -strict -emit-ts ../web/src/lib/contracts/fixtures.check.ts
	cd web && node scripts/contract-types.mjs --check && ./node_modules/.bin/next typegen && ./node_modules/.bin/tsc --noEmit -p . && ./node_modules/.bin/eslint src && bun test

sim-deep:
	$(MAKE) -C core sim-deep

sim-pg:
	$(MAKE) -C core sim-pg

sim-personas-validate:
	python3 scripts/persona-sim/validate.py

sim-personas-smoke:
	python3 scripts/persona-sim/runner.py --preset smoke --tag smoke

sim-personas-memory:
	python3 scripts/persona-sim/runner.py --preset memory-pack --concurrency 3 --tag memory-pack

sim-personas-full:
	python3 scripts/persona-sim/runner.py --preset full --concurrency 3 --tag full

# Local dependencies retain their named volumes across stop/start.
deps:
	docker compose -f core/infrastructure/local/compose.yaml up -d --wait

deps-stop:
	docker compose -f core/infrastructure/local/compose.yaml stop
