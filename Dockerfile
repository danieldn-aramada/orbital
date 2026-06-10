# Single Dockerfile, two target images. Build with:
#   docker build --target=orbital -t orbital:local .
#   docker build --target=orb     -t orb:local     .
# Without --target the default is `orbital` (kept last so existing CI/`make push` keeps working).

# ---- Build stage ----
FROM golang:1.25.5-alpine AS builder

WORKDIR /app
ENV CGO_ENABLED=0

# Cache deps
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# Copy source
COPY . .

# Build both binaries into /out so each runtime target can pick the one it needs.
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags="-X github.com/armada/orbital/internal/version.Version=${VERSION}" -o /out/orbital ./cmd/orbital && \
    go build -ldflags="-X github.com/armada/orbital/internal/version.Version=${VERSION}" -o /out/orb     ./cmd/orb

# ---- DGraph tools stage ----
# Provides the dgraph binary for running `dgraph live` during restore (orbital)
# and import (orb). Both apps run it as a subprocess inside their own container.
FROM dgraph/dgraph:v25.3.1 AS dgraph-tools

# ---- Shared runtime base ----
# Common alpine + tools + web/schema layout used by both images. Anything that
# both apps need at runtime goes here so the two final images stay in lockstep.
FROM alpine:3.21 AS runtime-base

RUN apk add --no-cache \
    curl \
    bash \
    bind-tools \
    netcat-openbsd \
    procps \
    gcompat \
    libc6-compat

# gcompat + libc6-compat provide the glibc compat layer needed to run the
# dgraph binary (built on Ubuntu against glibc) on alpine (musl). Without
# them dgraph fails with a misleading "no such file or directory" on exec.

WORKDIR /app

# Templates + static assets. Orb embeds these via //go:embed but orbital still
# loads from disk; copy once at the shared layer so both images have them.
COPY --from=builder /app/web ./web
COPY --from=builder /app/schema ./schema

# dgraph binary on PATH so SubprocessBackend / SubprocessRestoreBackend can exec it.
COPY --from=dgraph-tools /usr/local/bin/dgraph /usr/local/bin/dgraph

# ---- Orb image ----
FROM runtime-base AS orb
RUN mkdir -p /var/lib/orb
COPY --from=builder /out/orb ./
EXPOSE 8010
CMD ["./orb", "start"]

# ---- Orbital image (default target) ----
# Kept last so `docker build .` (no --target) produces the orbital image,
# matching prior CI/`make push` behavior.
FROM runtime-base AS orbital
COPY --from=builder /out/orbital ./
EXPOSE 8001
CMD ["./orbital"]
