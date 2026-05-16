SHELL := /bin/bash

MODULE  := github.com/armada/orbital
VERSION := $(shell git describe --tags --dirty 2>/dev/null || echo "v0.0.0-dev")
LDFLAGS := -ldflags "-X $(MODULE)/internal/version.Version=$(VERSION)"

BIN_DIR      := bin
ORBITAL_BIN  := $(BIN_DIR)/orbital
ORB_BIN      := $(BIN_DIR)/orb

COMPOSE_FILE := deploy/local/docker-compose.yml
ACR          := armadaeksatest.azurecr.io
IMAGE        := $(ACR)/orbital:$(VERSION)

.PHONY: help build build-orbital build-orbital-cli build-orb run-orbital push test test-e2e lint up down seed seed-aks-clean docs build-css watch-css

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

build: build-orbital build-orb ## Build all binaries

docs: ## Regenerate Swagger docs (requires swag: go install github.com/swaggo/swag/cmd/swag@latest)
	swag init -g cmd/orbital/main.go -o docs

build-css: ## Compile web/sass/main.scss → web/static/css/main.css (requires: npm install)
	npm run build-css

watch-css: ## Watch and recompile SCSS on change (requires: npm install)
	npm run build-css-dev

build-orbital: docs ## Build the orbital server binary → bin/orbital
	go build $(LDFLAGS) -o $(ORBITAL_BIN) ./cmd/orbital

build-orbital-cli: ## Build the orbital admin CLI (experimental) → bin/orbital-cli
	CGO_ENABLED=1 go build $(LDFLAGS) -o $(BIN_DIR)/orbital-cli ./cmd/orbital-cli

build-orb: ## Build the orb edge binary → bin/orb
	go build $(LDFLAGS) -o $(ORB_BIN) ./cmd/orb

run-orbital: ## Run orbital server
	go run -ldflags "-X $(MODULE)/internal/version.Version=v0.0.0-dev" ./cmd/orbital

test: ## Run all Go tests
	go test ./...

test-e2e: ## Run Playwright e2e tests (requires orbital running on :8001)
	npx playwright test

lint: ## Run go vet
	go vet ./...

up: ## Start local stack (DGraph + PostgreSQL)
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
