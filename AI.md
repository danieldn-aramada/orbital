# AI Usage Log

This file is a minimal audit log of AI assistance used during development. It records when AI was used, which model, and the scope — not the full narrative. Architectural decisions, working conventions, and "do not suggest" rules live in `CLAUDE.md`, which is the source of truth for AI behavior in this repo.

AI was used as a collaborative engineering partner, not a code generator. All architectural decisions were made by the human engineer.

## Log

| Date     | Model                | Scope                                      |
|----------|----------------------|--------------------------------------------|
| 2026-04  | claude-sonnet-4-6    | Architecture, scaffold, roadmap            |
| 2026-04  | claude-sonnet-4-6    | Orb CLI scaffold and output UX             |
| 2026-05  | claude-sonnet-4-6    | SDD v0.3 review, project boundary, configbundle integration, roadmap and MVP definition, reporting auth architecture (transport-agnostic intake API, orb identity + Ed25519 signing, orbs PostgreSQL registry), deployment-generic terminology cleanup |
| 2026-05  | claude-sonnet-4-6    | UI: datacenter tab (htmx, htmx:after-swap inner tab wiring, load-once tab caching with dataset.loaded, skeleton, Grafana todo button, overflow fix, cache busting); Playwright E2E tests; example graphql seeds parsed from xlsx (seattle-galleon, houston-galleon, alaska-dot-galleon) |
| 2026-05  | claude-sonnet-4-6    | Spike 8 (backup): async DGraph backup to S3/Azure Blob, checksum dedup, retention, presigned download, test connection, `orbital-<version>-<timestamp>.zip` naming; Spike 6 partial (export + OCI): subgraph export API with blue-green DGraph, per-job scratch dirs via `destination` param, global job serialization, OCI artifact publish (oras-go v2 + cosign, air-gap safe), Edge Delivery UI, swagger annotations, `make docs` target |
