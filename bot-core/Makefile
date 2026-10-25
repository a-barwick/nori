.PHONY: run build check

run:
	go run ./cmd/bot-core

build:
	go build -o bin/bot-core ./cmd/bot-core

check:
	go vet ./...
	@test -z "$$(gofmt -l cmd)" || (echo "Run gofmt on cmd/"; exit 1)
