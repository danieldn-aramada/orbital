SHELL := /bin/bash

MODULE  := github.com/armada/orbital
VERSION := $(shell git describe --tags --dirty 2>/dev/null || echo "v0.0.0-dev")
LDFLAGS := -ldflags "-X $(MODULE)/internal/version.Version=$(VERSION)"

BIN_DIR      := bin
ORBITAL_BIN  := $(BIN_DIR)/orbital
ORB_BIN      := $(BIN_DIR)/orb

COMPOSE_FILE := deploy/local/docker-compose.yml

.PHONY: help build build-orbital build-orb run-orbital test test-e2e lint up down seed docs

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

build: build-orbital build-orb ## Build all binaries

docs: ## Regenerate Swagger docs (requires swag: go install github.com/swaggo/swag/cmd/swag@latest)
	swag init -g cmd/orbital/main.go -o docs

build-orbital: docs ## Build the orbital server binary → bin/orbital
	go build $(LDFLAGS) -o $(ORBITAL_BIN) ./cmd/orbital

build-orb: ## Build the orb edge binary → bin/orb
	go build $(LDFLAGS) -o $(ORB_BIN) ./cmd/orb

run-orbital: ## Run orbital server
	go run $(LDFLAGS) ./cmd/orbital

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

seed: ## Seed DGraph with example data
	bash scripts/seed.sh
