.PHONY: dev web web-build web-start core local-web local-core check deploy deploy-check deploy-status deploy-rollback deploy-unlock pause unpause

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
	cd web && node scripts/contract-types.mjs --check && ./node_modules/.bin/next typegen && ./node_modules/.bin/tsc --noEmit -p . && ./node_modules/.bin/eslint src

deploy deploy-check deploy-status deploy-rollback deploy-unlock:
	python3 core/infrastructure/release/deploy.py $(patsubst deploy-%,%,$@)

pause unpause:
ifeq ($(NORI_ENVIRONMENT),sandbox)
	cd core && NORI_TF_BACKEND=gcs NORI_ENVIRONMENT=sandbox python3 infrastructure/app/ops.py $@
else
	python3 core/infrastructure/release/deploy.py $@
endif

# Local dependencies retain their named volumes across stop/start.
.PHONY: deps deps-stop
deps:
	docker compose -f core/infrastructure/local/compose.yaml up -d --wait

deps-stop:
	docker compose -f core/infrastructure/local/compose.yaml stop
