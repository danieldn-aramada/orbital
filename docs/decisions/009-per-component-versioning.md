# 009 — Per-component versioning for monorepo binaries

**Date:** 2026-06-09
**Status:** Accepted

## Context

The orbital repo produces three independent binaries:

- `cmd/orbital` — orbital server (deployed to AKS as Docker image)
- `cmd/orb` — orb edge service (separate deployment target)
- `cmd/orbital-cli` — user CLI (distributed via Homebrew tap)

Initially these shared a single `VERSION` derived from `git describe --tags --dirty`, with all three binaries link-injecting the same value into `internal/version.Version`. Tags used the unprefixed `v0.0.X` form.

This caused a real problem when `orbital-cli` became a published artifact: its version reported v0.0.17 because the server had been iterating, even though the CLI itself had no prior release. New users seeing `orbital --version` would infer a maturity history that didn't exist.

## Decision

Each binary has its own tag lineage. Component prefix identifies the lineage:

| Binary | Tag prefix | Example |
|---|---|---|
| orbital server | bare `v*` (continues existing lineage) | `v0.0.17` |
| orbital CLI | `cli/v*` | `cli/v0.0.1` |
| orb edge service | `orb/v*` | `orb/v0.0.1` |

The `internal/version.Version` package variable remains the single injection target — each `go build` invocation injects a different value at link time depending on which binary is being built.

## Implementation

In `Makefile`:

```makefile
SERVER_VERSION := $(shell git describe --tags --exclude 'cli/*' --exclude 'orb/*' --dirty 2>/dev/null || echo "v0.0.0-dev")
CLI_VERSION    := $(shell (git describe --tags --match 'cli/v*' --dirty 2>/dev/null || echo "cli/v0.0.0-dev") | sed 's|^cli/||')
ORB_VERSION    := $(shell (git describe --tags --match 'orb/v*' --dirty 2>/dev/null || echo "orb/v0.0.0-dev") | sed 's|^orb/||')

SERVER_LDFLAGS := -ldflags "-X $(MODULE)/internal/version.Version=$(SERVER_VERSION)"
CLI_LDFLAGS    := -ldflags "-X $(MODULE)/internal/version.Version=$(CLI_VERSION)"
ORB_LDFLAGS    := -ldflags "-X $(MODULE)/internal/version.Version=$(ORB_VERSION)"
```

Each `build-*` target uses its own LDFLAGS. Same `internal/version.Version` is replaced with the matching value at link time.

The release script `scripts/release-cli.sh` accepts a bare `v0.0.X` argument and prepends `cli/` when tagging and creating the GitHub release. Brew formula download URLs include the `cli/` segment in the path.

## Alternatives considered

**Unified versioning (Kubernetes pattern).** All binaries share one tag, one version. Used by Kubernetes (kubectl + kubelet + apiserver), Istio (istioctl + istiod), Argo CD. Appropriate when components ship in lockstep with strong version-skew compatibility contracts across N-2 versions. **Rejected** because orbital's CLI is a thin GraphQL client, loosely coupled to the server, with no version-skew contract. Forces a CLI version bump on every server release — exactly the problem we hit at v0.0.17.

**Separate repos per component (Hashicorp pattern).** terraform, vault, consul, nomad each have their own repo and version lineage. **Rejected** because the components share orbauth, the version package, and significant Go code. Splitting the repo isn't justified by current friction.

## Consequences

- Each binary's `--version` reports its own lineage. Server keeps v0.0.X; CLI starts at v0.0.1; orb starts at v0.0.1 when it ships.
- `git describe` derivations are more complex (component-aware match/exclude patterns).
- Release flows are now per-component. `scripts/release-cli.sh` tags `cli/v*` and only ships the CLI. Server has its existing release flow.
- API compatibility between CLI and server is no longer derivable from version equality. If contract drift becomes a real concern, add a `/api/v1/whoami` endpoint that includes server version + a compatibility-range check at CLI startup. Not needed yet.

## When to reconsider

- If CLI and server start hitting compatibility issues — formalize via an API version contract before considering re-unification.
- If GraphQL schema evolves in ways that break CLI behavior on older servers — add a runtime warning, not version coupling.
