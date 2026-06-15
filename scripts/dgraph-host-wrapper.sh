#!/bin/bash
# Host-side wrapper that proxies `dgraph` calls through a Docker container.
#
# Why this exists: the dgraph binary is Linux-only. `make run-orbital` and
# `make run-orb` are `go run` invocations on the host, and on macOS hosts
# there is no `dgraph` binary on PATH. Orbital's restore flow and orb's
# import flow both exec `dgraph live` as a subprocess (see settled rule in
# CLAUDE.md: "Tests validate the deployable artifact, not dev-only code
# paths" — there is exactly one dgraph-exec code path, the production one).
#
# This wrapper resolves the gap at the *environment* layer (PATH), not the
# code layer. Go's exec.Command("dgraph", ...) finds this script first on
# PATH; the script forwards to a containerized dgraph. The orbital/orb Go
# code is unchanged and identical to what runs in production.
#
# Install with: make dev-deps
#
# ─── Why docker run, not docker exec into the running alpha container ─────
#
# `docker exec local-dgraph-alpha-1 dgraph live ...` would be ~1-2s faster
# per invocation (no container startup), and it's tempting. We do NOT use
# it deliberately, because:
#
# 1. **Production parity.** In production, `dgraph live` runs as a SEPARATE
#    process in a SEPARATE container (orbital's own container, with the
#    dgraph binary baked in), talking to dgraph-alpha over gRPC across a
#    real network. `docker run` here mirrors that — fresh container, gRPC
#    over the compose network. `docker exec` into the alpha container runs
#    `dgraph live` IN alpha, communicating over loopback. Different network
#    shape, different process tree, potentially hides gRPC-layer bugs.
#
# 2. **Routing complexity.** orbital writes to dgraph-alpha, orb writes to
#    dgraph-orb-alpha. With docker run + --network=local_default, compose
#    DNS routes via the --alpha flag the caller passed. With docker exec,
#    the wrapper would have to parse $@ to pick which container, or be
#    hardcoded per-app — re-introducing the divergent-code-path problem.
#
# The ~1-2s container startup is negligible vs the actual live-load time.
# If the gRPC-vs-loopback distinction ever stops mattering and routing
# becomes simpler, this can be revisited — but do not switch "for speed"
# without thinking through the production-parity implications.
# ──────────────────────────────────────────────────────────────────────────

set -euo pipefail

# Rewrite localhost:<port> → host.docker.internal:<port> in every argument.
# When this wrapper runs inside a Docker container (--network=local_default),
# "localhost" resolves to the container's own loopback, not the Mac host.
# host.docker.internal is Docker Desktop's stable alias for the Mac host IP,
# so gRPC connections to -a localhost:9082 / -z localhost:5082 reach the
# compose-exposed ports as intended.
args=()
for arg in "$@"; do
  args+=("${arg//localhost/host.docker.internal}")
done

# /tmp is mounted because orbital's restore and orb's import write the
# downloaded backup zip to a host-side temp dir, then pass that path as
# the --files arg to `dgraph live`. The wrapper container needs to see
# the same files at the same paths.
exec docker run --rm \
  --network=local_default \
  -v /tmp:/tmp \
  -v "$PWD:$PWD" -w "$PWD" \
  dgraph/dgraph:v25.3.1 \
  dgraph "${args[@]}"
