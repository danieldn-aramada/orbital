# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**Orbital** is a configuration management framework for modular data centers, focused on multi-site and air-gapped deployments. Written in Go.

### Key Concepts

- **`orbital`** — Cloud control plane holding design intent (configuration items) for all modular data centers. Serves APIs for digital twin building and pushes configuration down to orbs.
- **`orb`** — Standalone edge service running inside a modular data center. Serves configuration, detects drift, suitable for air-gapped deployments.
- **`orbital import [orb]`** — Merges existing configuration from a modular data center into orbital.
- **`orbital export [orb]`** — Exports configuration from orbital and pushes it down to an orb instance.

## Stack

- **Go** — Implementation language for both `orbital` and `orb`
- **DGraph** — Graph database with GraphQL API on top of RDF-like storage; stores all configuration items
- **PostgreSQL** — Relational database for metadata and general backend services for `orbital`
- **Valkey** — In-memory cache for `orbital`

## Repository Structure

```
cmd/
  cli/orbital/     # CLI for the orbital control plane
  server/orb/      # Edge orb service (entry point: main.go)
  server/orbital/  # Control plane server
deploy/
  orb/             # Deployment files for edge orb service
  orbital/         # Deployment files for orbital control plane
```

## Development Status

This is an early-stage project. Go module files (`go.mod`/`go.sum`) have not yet been initialized. Before adding packages or running Go commands, initialize the module:

```bash
go mod init github.com/armada/orb   # or the appropriate module path
```
