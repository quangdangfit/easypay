SHELL := /bin/bash

APP_NAME := easypay
BIN_DIR  := bin
PKG      := github.com/quangdangfit/easypay
LDFLAGS  := -s -w

.PHONY: help run build tidy lint test unittest test-integration inttest migrate up down logs clean bench mocks

# Packages to count toward coverage (everything user-written, excluding
# main entry points, integration tests, generated mocks, and the bench tool).
SOURCE_PKGS := $(shell go list ./... | grep -v '/cmd/' | grep -v '/integration_test' | grep -v '/internal/mocks' | tr '\n' ',')

help:
	@echo "Targets:"
	@echo "  run               - run the server (go run)"
	@echo "  build             - build binary into ./bin"
	@echo "  tidy              - go mod tidy"
	@echo "  lint              - golangci-lint run ./..."
	@echo "  test              - unit tests with -race"
	@echo "  unittest          - unit tests with coverage profile (CI target)"
	@echo "  test-integration  - integration tests (needs Docker)"
	@echo "  inttest           - integration tests with coverage profile (CI target)"
	@echo "  bench             - run bench harness (50 workers x 1000 req)"
	@echo "  mocks             - regenerate gomock mocks (go.uber.org/mock)"
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
	@command -v golangci-lint >/dev/null 2>&1 || { echo "Install: https://golangci-lint.run/welcome/install/"; exit 1; }
	golangci-lint run ./...

test:
	go test ./internal/... ./pkg/... -v -race -count=1

# unittest runs the suite with coverage written to coverage.out (consumed by
# CI for Codecov upload) and prints a human-readable summary.
unittest:
	go test -timeout 9000s -v -race -coverprofile=coverage.out -coverpkg=$(SOURCE_PKGS) ./internal/... ./pkg/... 2>&1 | tee report.out
	@echo
	@go tool cover -func=coverage.out | tail -n 5

test-integration:
	EASYPAY_INTEGRATION=1 go test -tags integration ./integration_test/... -v -count=1 -timeout 600s

# inttest runs the integration suite with coverage attributed back to the
# same SOURCE_PKGS as unittest, so Codecov can merge the two profiles.
inttest:
	EASYPAY_INTEGRATION=1 go test -tags integration -timeout 600s -count=1 -coverprofile=coverage-integration.out -coverpkg=$(SOURCE_PKGS) ./integration_test/...
	@echo
	@go tool cover -func=coverage-integration.out | tail -n 5

mocks:
	@command -v mockgen >/dev/null 2>&1 || { echo "Install: go install go.uber.org/mock/mockgen@latest"; exit 1; }
	go generate ./...

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
