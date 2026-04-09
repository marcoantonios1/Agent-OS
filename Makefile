.PHONY: build test lint run test-api test-email test-calendar

build:
	go build ./...

test:
	go test ./... -race

lint:
	go vet ./...

run:
	set -a && . ./.env && set +a && go run ./cmd/agentos/

test-api:
	bash scripts/test_api.sh

test-email:
	bash scripts/test_email.sh

test-calendar:
	bash scripts/test_calendar.sh
