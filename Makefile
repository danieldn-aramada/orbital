SHELL := /bin/bash

MODULE  := github.com/armada/orbital
VERSION := $(shell git describe --tags --dirty 2>/dev/null || echo "v0.0.0-dev")
LDFLAGS := -ldflags "-X $(MODULE)/internal/version.Version=$(VERSION)"

BIN_DIR      := bin
ORBITAL_BIN  := $(BIN_DIR)/orbital
ORB_BIN      := $(BIN_DIR)/orb

COMPOSE_FILE := deploy/local/docker-compose.yml

.PHONY: help build build-orbital build-orb run-orbital test lint up down seed

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

build: build-orbital build-orb ## Build all binaries

build-orbital: ## Build the orbital server binary → bin/orbital
	go build $(LDFLAGS) -o $(ORBITAL_BIN) ./cmd/orbital

build-orb: ## Build the orb edge binary → bin/orb
	go build $(LDFLAGS) -o $(ORB_BIN) ./cmd/orb

run-orbital: ## Run orbital server (sources deploy/local/.env and .env.local if present)
	bash -c 'source deploy/local/.env; [ -f deploy/local/.env.local ] && source deploy/local/.env.local; go run $(LDFLAGS) ./cmd/orbital'

test: ## Run all tests
	go test ./...

lint: ## Run go vet
	go vet ./...

up: ## Start local stack (DGraph + PostgreSQL)
	docker compose -f $(COMPOSE_FILE) up -d

down: ## Stop local stack
	docker compose -f $(COMPOSE_FILE) down -v

seed: ## Seed DGraph with example data
	bash scripts/seed.sh
