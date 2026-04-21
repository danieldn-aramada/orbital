# AI Usage Log

This file documents how AI (Claude, via Claude Code) was used during the development of Orbital. The goal is transparency and reproducibility — anyone reading this should understand what was delegated to AI, what instructions were given, and what human decisions shaped the outcome.

AI was used as a collaborative engineering partner, not a code generator. All architectural decisions were made by the human engineer. Claude was used to implement, document, and pressure-test those decisions.

---

## Sessions

### April 2026 — Architecture, Scaffold, and Roadmap

**Tool:** Claude Code (claude-sonnet-4-6) via CLI

**What was done:**
- Established Go project structure (`cmd/`, `internal/`, `pkg/`, `schema/`, `deploy/`)
- Built Echo server for orb with graceful shutdown and config package
- Designed and documented full system architecture through Q&A dialogue
- Wrote `CLAUDE.md` — conventions, architecture decisions, working style
- Rewrote README motivation and architecture sections
- Defined GraphQL schema `schema/schema-v1.graphql` with `ConfigItem` interface
- Created public Go types in `pkg/orbital/types.go` mirroring the schema
- Refactored `cmd/server/orbital/main.go` to match established repo patterns
- Consolidated static files into `internal/static/`
- Added Kubernetes NetworkPolicy to restrict DGraph access to orbital only
- Created `ROADMAP.md` with Gantt chart, spike definitions, and external integration dependencies

**Key instructions given:**
- "Don't add comments that just restate what the code does"
- "Keep cmd/ thin — entry points only, all logic in packages"
- "Don't refactor code that wasn't part of the request"
- "Orbital is an API-first system, not a framework — design PLM/ITSM integrations behind Go interfaces, no tight coupling to any vendor"
- "Do not use expiring JWTs for orb auth — orbs may be disconnected for months"
- "Do not suggest replacing DGraph or switching to Redis"
- "Proxy reads, own writes — GraphQL proxy stays for topology queries, orbital validates and handles admin mutations directly"

**Decisions made by the human engineer (not AI):**
- DGraph as the graph database (evaluated separately before this session)
- Valkey over Redis (licensing)
- GitHub Actions runner pattern for orb registration
- Air-gap sync via DGraph export (`json.gz` + `schema.gz`)
- One shared DGraph instance per orbital deployment (not per data center)
- `version: String` over `version: Float` in the schema
- "system" over "framework" in the project description
- Roadmap spike prioritization and sequencing
- Decision to question schema migration automation (build vs runbook framing)

**What AI suggested that was accepted:**
- `pkg/orbital/` over `api/` for public types (import ergonomics — `orbital.Server` vs `api.Server`)
- Pointer types (`*string`, `*int64`) for nullable GraphQL fields
- Separating NetworkPolicy (L3/L4) from Istio AuthorizationPolicy (L7) concerns
- Framing spike 5 as a decision ("build vs runbook") rather than an assumption
- `AI.md` as the transparency mechanism over inline comments or commit trailers alone

**What AI suggested that was rejected:**
- Alpha/Beta release stages (unnecessary for an internal tool)
- Dev/stage/prod callout in the SDLC diagram (deployment topology, not lifecycle)
- Embedding static files at this stage (deferred — would require bundling all dependencies)
