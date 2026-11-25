.PHONY: dev web web-build web-start core local-web local-core check sim-deep sim-pg sim-personas-validate sim-personas-smoke sim-personas-quick sim-personas-core sim-personas-memory sim-personas-full deps deps-stop deploy-gcp

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

# Multi-persona runs go through suite.py: one runner process + ephemeral DB per persona (runner.py
# --concurrency shares one DB across personas and cross-contaminates them). Optional BASELINE=<scorecard.json>.
sim-personas-quick:
	python3 scripts/persona-sim/suite.py --preset core --weeks 1 --outer-reasoning low --inner-reasoning low --tag quick $(if $(BASELINE),--compare $(BASELINE),)

sim-personas-core:
	python3 scripts/persona-sim/suite.py --preset core --tag core $(if $(BASELINE),--compare $(BASELINE),)

sim-personas-memory:
	python3 scripts/persona-sim/suite.py --preset memory-pack --tag memory-pack $(if $(BASELINE),--compare $(BASELINE),)

sim-personas-full:
	python3 scripts/persona-sim/suite.py --preset full --tag full $(if $(BASELINE),--compare $(BASELINE),)

# Local dependencies retain their named volumes across stop/start.
deps:
	docker compose -f core/infrastructure/local/compose.yaml up -d --wait

deps-stop:
	docker compose -f core/infrastructure/local/compose.yaml stop

deploy-gcp:
	./scripts/deploy-gcp.sh
