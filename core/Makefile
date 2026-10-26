.PHONY: run build check

run:
	go run ./cmd/nori

build:
	go build -o bin/nori ./cmd/nori

check:
	go vet ./...
	@test -z "$$(gofmt -l cmd internal)" || (echo "Run gofmt on cmd/ and internal/"; exit 1)
