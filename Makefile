SHELL := /bin/bash

MODULE  := github.com/armada/orbital
VERSION := $(shell git describe --tags --dirty 2>/dev/null || echo "v0.0.0-dev")
LDFLAGS := -ldflags "-X $(MODULE)/internal/version.Version=$(VERSION)"

BIN_DIR      := bin
ORBITAL_BIN  := $(BIN_DIR)/orbital
ORB_BIN      := $(BIN_DIR)/orb

COMPOSE_FILE := deploy/local/docker-compose.yml

# Packages included in unit test runs and coverage reports.
# Excludes generated code (ent/*) and the Swagger docs stub.
TEST_PKGS := $(shell go list ./... | grep -vE '(/ent$$|/ent/|/docs$$)')
ACR          := armadaeksatest.azurecr.io
IMAGE        := $(ACR)/orbital:$(VERSION)

.PHONY: help build build-orbital build-orbital-cli build-orb run-orbital push test test-unit test-integration test-e2e test-e2e-orb test-stack-up cover cover-html lint up up-orb-deps up-orb down seed seed-aks-clean docs orb-docs build-css watch-css

help: ## Show this help
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

build: build-orbital build-orb ## Build all binaries

docs: ## Regenerate Swagger docs (requires swag: go install github.com/swaggo/swag/cmd/swag@latest)
	swag init -g cmd/orbital/main.go -o docs

orb-docs: ## Regenerate Swagger docs for orb (requires swag: go install github.com/swaggo/swag/cmd/swag@latest)
	swag init -g doc.go -o docs/orb --dir cmd/orb,internal/orbserver,internal/orb

build-css: ## Compile web/sass/main.scss → web/static/css/main.css (requires: npm install)
	npm run build-css

watch-css: ## Watch and recompile SCSS on change (requires: npm install)
	npm run build-css-dev

build-orbital: docs ## Build the orbital server binary → bin/orbital
	go build $(LDFLAGS) -o $(ORBITAL_BIN) ./cmd/orbital

build-orbital-cli: ## Build the orbital admin CLI (experimental) → bin/orbital-cli
	CGO_ENABLED=1 go build $(LDFLAGS) -o $(BIN_DIR)/orbital-cli ./cmd/orbital-cli

build-orb: orb-docs ## Build the orb edge binary → bin/orb
	go build $(LDFLAGS) -o $(ORB_BIN) ./cmd/orb

run-orbital: ## Run orbital server
	go run -ldflags "-X $(MODULE)/internal/version.Version=v0.0.0-dev" ./cmd/orbital

run-orb: ## Run orb edge service (requires: make up)
	go run -ldflags "-X $(MODULE)/internal/version.Version=v0.0.0-dev" ./cmd/orb start

seed-orb-schema: ## Apply DGraph schema to orb's local DGraph (empty — data comes from import)
	@echo "Applying schema to orb DGraph (localhost:8082)..."
	@curl -s -X POST localhost:8082/admin/schema --data-binary @schema/schema-demo.graphql | jq .

test-stack-up: ## Ensure local stack is up and healthy (used by test-integration)
	@docker compose -f $(COMPOSE_FILE) up -d --wait
	@docker compose -f $(COMPOSE_FILE) exec -T postgres psql -U orbital -c "CREATE DATABASE orbital_test;" 2>/dev/null || true

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

test-e2e: ## Run Playwright e2e tests (requires orbital running on :8001)
	npx playwright test

test-e2e-orb: ## Run Playwright orb UI tests (requires orb running on :8010)
	npx playwright test --config=playwright.orb.config.ts

test: test-unit test-integration test-e2e test-e2e-orb ## Run full test suite (unit + integration + e2e + e2e-orb)

cover: test-stack-up ## Run tests with coverage and print summary to terminal
	@echo "Running tests with coverage..."
	@go test -short -coverprofile=coverage.out -covermode=atomic $(TEST_PKGS)
	@go tool cover -func=coverage.out | tail -1

cover-html: cover ## Open interactive HTML coverage report in browser
	go tool cover -html=coverage.out -o coverage.html
	open coverage.html

lint: ## Run go vet
	go vet ./...

up: ## Start all local dependencies (DGraph, PostgreSQL, MinIO, Zot, orb DGraph)
	docker compose -f $(COMPOSE_FILE) up -d

down: ## Stop local stack
	docker compose -f $(COMPOSE_FILE) down -v

push: ## Build and push image to ACR (requires: az acr login --name armadaeksatest)
	docker buildx build --platform linux/amd64 --build-arg VERSION=$(VERSION) -t $(IMAGE) --push .

seed: ## Seed DGraph with example data (local)
	bash scripts/seed.sh

seed-aks-clean: ## Seed AKS dev DGraph — drop all first then reseed (clean slate)
	bash scripts/seed-aks.sh --clean

seed-aks: ## Seed AKS dev DGraph (port-forwards, seeds, cleans up)
	bash scripts/seed-aks.sh

seed-aks-postgres: ## Seed AKS dev PostgreSQL admin user (port-forwards, seeds, cleans up)
	bash scripts/seed-aks-postgres.sh

smoke-aks: ## Run smoke tests against AKS (requires: kubectl port-forward svc/orbital 8001:8001 -n netbox)
	npx playwright test --config=playwright.smoke.config.ts
