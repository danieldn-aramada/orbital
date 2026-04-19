# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**Orbital** is a graph-native framework for continuously reconciling infrastructure across modular, air-gapped data centers. Written in Go.

### Key Concepts

- **`orbital`** — Server running in cloud. Holds design intent (configuration items) for all modular data centers, serves the Topology API for digital twin building, and pushes configuration down to orbs.
- **`orb`** — Standalone binary running inside a modular data center. Serves configuration, detects drift, suitable for air-gapped deployments.

### Features

- Graph-first infrastructure model — represent data centers as relationships between physical and logical resources
- Multi-source infrastructure discovery — ingest from bare metal systems (BMC) and external inventory systems via API integrations
- Topology API (digital twin) — build and query a live, traversable graph of infrastructure design intent
- Air-gap ready — operates in disconnected and edge environments without external dependencies

### Non-Goals

- Full DCIM system with dashboards, alerting, and observability
- End-to-end infrastructure management suite

## Stack

- **Go** — Implementation language for both `orbital` and `orb`
- **DGraph** (community edition) — Graph database with native GraphQL API on top of RDF-like storage; stores all configuration items. Chosen because the RDF model fits configuration items naturally, and the GraphQL API lets external teams (e.g. a digital twin UI) consume data without custom endpoints. Do not suggest replacing DGraph. Some enterprise features (namespaces, backups) may be implemented in-house later.
- **PostgreSQL** — Relational database for metadata and general backend services for `orbital`
- **Valkey** — In-memory cache for `orbital`; chosen over Redis due to licensing. Do not suggest switching to Redis.

## Architecture Notes

Clients never query DGraph directly. All queries go through the Go server, which acts as middleware and is responsible for rate limiting, caching, auth, and any other cross-cutting concerns. This applies to external consumers (e.g. digital twin UI teams) as well.

## Local Development

Start the local stack (DGraph + PostgreSQL) with:

```bash
docker compose -f deploy/local/docker-compose.yml up -d
```

| Service | Port(s) | Notes |
|---|---|---|
| DGraph Zero | 5080, 6080 | Cluster coordinator |
| DGraph Alpha | 8080 (HTTP/GraphQL), 9080 (gRPC) | GraphQL playground at http://localhost:8080 |
| DGraph Ratel | 8000 | DGraph UI |
| PostgreSQL | 5432 | user/password/db: `orbital` |

## Repository Structure

```
cmd/
  cli/orbital/        # orbital CLI entry point
  server/orb/         # orb server entry point
  server/orbital/     # orbital server entry point
deploy/
  local/              # Local development stack (docker-compose)
  orb/                # Deployment files for orb
  orbital/            # Deployment files for orbital
internal/
  config/             # Shared config (port, timeouts, DGraph URL)
  discovery/          # Discovery orchestration (used by orbital)
    bmc/              # BMC/bare metal discovery
  drift/              # Drift detection (used by orb)
  graph/              # DGraph client and topology operations
  handler/            # HTTP handlers (GraphQL proxy, topology API)
  server/             # Shared Echo server setup and lifecycle
  static/             # Static files (GraphiQL UI)
```

## Working Style

- Don't add comments that just restate what the code does
- Don't refactor code that wasn't part of the request — ask first
- Don't add third-party packages without asking first
- Only touch files relevant to the task
- Don't clean up unrelated code while working on something else
- Don't add TODOs or placeholder comments

## Go Conventions

- **Error wrapping** — use `fmt.Errorf("...: %w", err)`; never discard or log-and-return
- **Context** — always the first argument: `func Foo(ctx context.Context, ...)`
- **Constructors** — named `New[Type]`, e.g. `NewServer`, `NewClient`
- **`cmd/` is thin** — entry points only; all logic lives in separate packages
- **Tests** — table-driven with `t.Run`; avoid test helpers that obscure failure sites
- No `init()` functions
- No global variables
- No `panic()` outside of `main()`

## Development Status

This is an early-stage project. The Go module is initialized at `github.com/armada/orbital`.
