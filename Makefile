.PHONY: build test lint run test-api test-email

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

test-email:
	bash scripts/test_email.sh
