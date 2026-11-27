.PHONY: infra-check infra-plan api-types api-check contracts contracts-check dev web web-build web-start core local-web local-core check sim-deep sim-pg sim-personas-validate sim-personas-smoke sim-personas-quick sim-personas-core sim-personas-memory sim-personas-full deps deps-stop release

NORI_ENV_FILE ?= $(abspath .env)
NORI_TASK_ENV_FILE ?= $(NORI_ENV_FILE)
NORI_LOCAL_AGENT_URL ?= http://127.0.0.1:8080
NORI_WEB_PORT ?= 3210
NORI_WEB_DEV_ARGS ?=

dev:
	$(MAKE) -j2 local-web local-core

local-web:
	cd web && NORI_LOCAL_AGENT_URL="$(NORI_LOCAL_AGENT_URL)" node ../scripts/run-with-env.mjs "$(NORI_ENV_FILE)" bun --no-env-file ws-server.mjs --dev $(NORI_WEB_DEV_ARGS) --port $(NORI_WEB_PORT)

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

api-types:
	bun -e 'import { readFileSync, writeFileSync } from "node:fs"; const doc = Bun.YAML.parse(readFileSync("api/openapi.yaml", "utf8")); writeFileSync("api/openapi.json", JSON.stringify(doc) + "\n");'
	cd web && bun run api:types
	node scripts/gen-swift-openapi.mjs

api-check:
	cd core && go test ./internal/api -run TestOpenAPI
	cd web && bun run api:types:check
	node scripts/gen-swift-openapi.mjs --check

contracts: api-types

contracts-check: api-check

check: api-check infra-check
	$(MAKE) -C core check
	cd web && ./node_modules/.bin/next typegen && ./node_modules/.bin/tsc --noEmit -p . && ./node_modules/.bin/eslint src && bun test src
	npm --prefix macos test
	node --test scripts/*.test.mjs
	python3 -m unittest discover -s scripts/persona-sim
	$(MAKE) sim-personas-validate

sim-deep:
	$(MAKE) -C core sim-deep

sim-pg:
	$(MAKE) -C core sim-pg

sim-personas-validate:
	python3 scripts/persona-sim/validate.py

sim-personas-smoke:
	python3 scripts/persona-sim/runner.py --preset smoke --tag smoke

sim-personas-quick:
	python3 scripts/persona-sim/suite.py --preset core --weeks 1 --outer-reasoning low --inner-reasoning low --tag quick $(if $(BASELINE),--compare $(BASELINE),)

sim-personas-core:
	python3 scripts/persona-sim/suite.py --preset core --tag core $(if $(BASELINE),--compare $(BASELINE),)

sim-personas-memory:
	python3 scripts/persona-sim/suite.py --preset memory-pack --tag memory-pack $(if $(BASELINE),--compare $(BASELINE),)

sim-personas-full:
	python3 scripts/persona-sim/suite.py --preset full --tag full $(if $(BASELINE),--compare $(BASELINE),)

deps:
	docker compose -f core/infrastructure/local/compose.yaml up -d --wait

deps-stop:
	docker compose -f core/infrastructure/local/compose.yaml stop

release:
	./scripts/release.sh

# infra/ is the source of truth for GCP. CI applies it on main; see core/infrastructure/README.md.
infra-check:
	terraform -chdir=infra fmt -check -recursive
	terraform -chdir=infra init -backend=false -input=false -lockfile=readonly >/dev/null
	terraform -chdir=infra validate

infra-plan:
	terraform -chdir=infra init -input=false -lockfile=readonly >/dev/null
	terraform -chdir=infra plan -input=false
	infra/check-unmanaged.sh
