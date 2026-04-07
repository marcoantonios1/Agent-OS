.PHONY: build test lint run test-api

build:
	go build ./...

test:
	go test ./... -race

lint:
	go vet ./...

run:
	go run ./cmd/agentos/

test-api:
	bash scripts/test_api.sh
