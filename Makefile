SHELL := /bin/bash

APP_NAME := easypay
BIN_DIR  := bin
PKG      := github.com/quangdangfit/easypay
LDFLAGS  := -s -w

.PHONY: help run build tidy lint test test-integration migrate up down logs clean bench

help:
	@echo "Targets:"
	@echo "  run               - run the server (go run)"
	@echo "  build             - build binary into ./bin"
	@echo "  tidy              - go mod tidy"
	@echo "  lint              - go vet"
	@echo "  test              - unit tests"
	@echo "  test-integration  - integration tests (needs Docker)"
	@echo "  migrate           - apply SQL migrations"
	@echo "  up / down / logs  - manage docker-compose stack"

run:
	go run ./cmd/server

build:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(APP_NAME) ./cmd/server

tidy:
	go mod tidy

lint:
	go vet ./...

test:
	go test ./internal/... ./pkg/... -v -race -count=1

test-integration:
	EASYPAY_INTEGRATION=1 go test -tags integration ./integration_test/... -v -count=1 -timeout 600s

migrate:
	@./scripts/migrate.sh up

bench:
	go run ./cmd/bench --concurrency 50 --total 1000

up:
	docker compose up -d

down:
	docker compose down -v

logs:
	docker compose logs -f
