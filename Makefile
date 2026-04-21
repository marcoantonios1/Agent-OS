.PHONY: build test lint run migrate test-api test-email test-calendar

DB ?= ./data/agentos.db

build:
	go build ./...

test:
	go test ./... -race

lint:
	go vet ./...

run:
	docker compose up --build

migrate:
	go run ./cmd/migrate/ -path $(DB)

test-api:
	bash scripts/test_api.sh

test-email:
	bash scripts/test_email.sh

test-calendar:
	bash scripts/test_calendar.sh
