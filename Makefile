SHELL := /bin/bash

MODULE := github.com/armada/orbital

# Per-component versioning — each binary has its own tag lineage.
# Server: bare v* tags (e.g. v0.0.17). CLI: cli/v* tags. Orb: orb/v* tags.
SERVER_VERSION ?= $(shell git describe --tags --exclude 'cli/*' --exclude 'orb/*' --dirty 2>/dev/null || echo "v0.0.0-dev")
CLI_VERSION    ?= $(shell (git describe --tags --match 'cli/v*' --dirty 2>/dev/null || echo "cli/v0.0.0-dev") | sed 's|^cli/||')

CLI_LDFLAGS    := -ldflags "-X $(MODULE)/internal/version.Version=$(CLI_VERSION)"

BIN_DIR      := bin

COMPOSE_FILE := deploy/local/docker-compose.yml

# Packages included in unit test runs and coverage reports.
# Excludes generated code (ent/*) and the Swagger docs stub.
TEST_PKGS := $(shell go list ./... | grep -vE '(/ent$$|/ent/|/docs$$)')
ACR          := armadaeksatest.azurecr.io
IMAGE        := $(ACR)/orbital:$(SERVER_VERSION)

.PHONY: help up down run-orbital run-orb seed test-unit test-integration test-e2e test-e2e-ui release-check release-check-down docs build-css build-orbctl push seed-aks smoke-aks

help: ## Show this help
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

## ── daily ─────────────────────────────────────────────────────────────────────

up: ## Start the local stack (DGraph, Postgres, MinIO, Zot, orb DGraph)
	docker compose -f $(COMPOSE_FILE) up -d

down: ## Stop the local stack
	docker compose -f $(COMPOSE_FILE) down -v

run-orbital: ## Run orbital server (go run; fast dev iteration). Restore requires dgraph in PATH
	go run -ldflags "-X $(MODULE)/internal/version.Version=v0.0.0-dev" ./cmd/orbital

run-orb: ## Run orb edge service (go run; fast dev iteration). Import requires dgraph in PATH
	go run -ldflags "-X $(MODULE)/internal/version.Version=v0.0.0-dev" ./cmd/orb start

seed: ## Seed DGraph with example data + admin user (local)
	bash scripts/seed.sh

## ── tests ─────────────────────────────────────────────────────────────────────

test-unit: ## Run unit tests with coverage summary (no external services required)
	@echo "Running unit tests..."
	@go test -short -coverprofile=coverage.out -covermode=atomic $(TEST_PKGS)
	@go tool cover -func=coverage.out | tail -1

test-integration: ## Run integration tests against real services (requires: make up)
	@docker compose -f $(COMPOSE_FILE) exec -T postgres psql -U orbital -c "CREATE DATABASE orbital_test;" 2>/dev/null || true
	@echo "Running integration tests..."
	@go test -v -count=1 -tags integration -timeout 10m $(TEST_PKGS)
	@echo "Reseeding DGraph for E2E tests..."
	@bash scripts/seed.sh

test-e2e: ## Run Playwright UI tests for orbital + orb (requires both running; HEADED=true to watch)
	npx playwright test

test-e2e-ui: ## Open Playwright UI mode for interactive local test watching
	npx playwright test --ui

release-check: ## Pre-release validation: build orbital+orb images, start as containers
	docker build --target=orbital -t orbital:local --build-arg VERSION=v0.0.0-dev .
	docker build --target=orb     -t orb:local     --build-arg VERSION=v0.0.0-dev .
	docker compose -f $(COMPOSE_FILE) --profile orbital --profile orb up -d orbital orb
	@echo "Waiting for orbital + orb to be ready..."
	@until curl -fs http://localhost:8001/healthz >/dev/null && curl -fs http://localhost:8010/healthz >/dev/null; do sleep 1; done
	bash scripts/seed.sh
	npx playwright test --config=playwright.release-check.config.ts

release-check-down: ## Stop the release-check containers (deps from `make up` are unaffected)
	docker compose -f $(COMPOSE_FILE) --profile orbital --profile orb stop orbital orb
	docker compose -f $(COMPOSE_FILE) --profile orbital --profile orb rm -f orbital orb

## ── as needed ─────────────────────────────────────────────────────────────────

docs: ## Regenerate Swagger docs for orbital + orb (requires swag)
	swag init -g main.go -o docs --dir cmd/orbital,internal/handler
	swag init -g doc.go -o docs/orb --dir cmd/orb,internal/orbserver,internal/orb

build-css: ## Compile web/sass/main.scss → web/shared/static/css/main.css (requires: npm install)
	npm run build-css


build-orbctl: ## Build the orbctl CLI → bin/orbctl
	go build $(CLI_LDFLAGS) -o $(BIN_DIR)/orbctl ./cmd/orbctl

## ── release / AKS ─────────────────────────────────────────────────────────────

push: ## Build and push orbital image to ACR (requires: az acr login --name armadaeksatest)
	docker buildx build --platform linux/amd64 --target=orbital --build-arg VERSION=$(SERVER_VERSION) -t $(IMAGE) --push .

seed-aks: ## Seed AKS dev DGraph + Postgres admin user. CLEAN=1 drops DGraph first.
	@if [ "$(CLEAN)" = "1" ]; then \
		bash scripts/seed-aks.sh --clean; \
	else \
		bash scripts/seed-aks.sh; \
	fi
	bash scripts/seed-aks-postgres.sh

smoke-aks: ## Smoke tests against AKS dev (requires: kubectl port-forward svc/orbital 8001:8001 -n orbital)
	npx playwright test --config=playwright.release-check.config.ts
