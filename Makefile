.PHONY: dev web web-build web-start core local-web local-core check deploy deploy-check deploy-status deploy-rollback deploy-unlock pause unpause smoke

# The fixture and migration checks use deterministic local providers.
.PHONY: chat chat-build chat-fixture migration-prep migration-doctor migration-baseline migration-integration migration-readiness
chat:
	cd chat && NORI_CHAT_WEB_URL="http://127.0.0.1:$(NORI_WEB_PORT)" bun --no-env-file run dev

chat-build:
	cd chat && bun --no-env-file run check && bun --no-env-file run build

chat-fixture:
	cd chat && bun --no-env-file run fixture

migration-prep:
	python3 migration/verify.py prep

migration-doctor:
	python3 migration/verify.py doctor

migration-baseline:
	python3 migration/verify.py baseline

migration-integration:
	python3 migration/verify.py integration

migration-readiness:
	python3 migration/verify.py readiness

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

web-build: chat-build
	cd web && node ../scripts/run-with-env.mjs "$(NORI_ENV_FILE)" bun --no-env-file run build

web-start:
	cd web && node ../scripts/run-with-env.mjs "$(NORI_ENV_FILE)" bun --no-env-file run start

core:
	$(MAKE) -C core run

check:
	$(MAKE) -C core check
	cd chat && bun --no-env-file run check
	cd web && ./node_modules/.bin/next typegen && ./node_modules/.bin/tsc --noEmit -p . && ./node_modules/.bin/eslint src
	python3 .agents/skills/review-docs/scripts/check.py --changed
	python3 docs/agent_map.py check

# Production operations. See core/infrastructure/README.md.
deploy deploy-check deploy-status deploy-rollback deploy-unlock:
	python3 core/infrastructure/release/deploy.py $(patsubst deploy-%,%,$@)

smoke:
	python3 core/infrastructure/release/deploy.py smoke

pause unpause:
	cd core && NORI_TF_BACKEND=gcs python3 infrastructure/app/ops.py $@
