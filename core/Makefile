.PHONY: run build test check sim-deep sim-pg

SIM_SEEDS ?= 500
SIM_STEPS ?= 120

run:
	go run ./cmd/nori

build:
	go build -o bin/nori ./cmd/nori

test:
	go test ./...

check:
	go vet ./...
	@test -z "$$(gofmt -l cmd internal)" || (echo "Run gofmt on cmd/ and internal/"; exit 1)
	go test ./...

sim-deep:
	go test -count=1 ./internal/sim/...
	go test -v -count=1 ./internal/sim -sim-seeds=$(SIM_SEEDS) -sim-steps=$(SIM_STEPS)

sim-pg:
	@if [ -z "$(NORI_TEST_POSTGRES_URL)" ]; then \
		echo "NORI_TEST_POSTGRES_URL is not set." >&2; \
		echo "Start local Postgres with 'make deps' and run:" >&2; \
		echo "  NORI_TEST_POSTGRES_URL=postgres://nori@127.0.0.1:15432/nori?sslmode=disable make sim-pg" >&2; \
		exit 1; \
	fi
	NORI_TEST_POSTGRES_URL="$(NORI_TEST_POSTGRES_URL)" go test -v -count=1 ./internal/sim/memstore -run TestBackendConformance_Postgres
