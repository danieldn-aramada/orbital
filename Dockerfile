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

# Build binary
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags="-X github.com/armada/orbital/internal/version.Version=${VERSION}" -o orbital ./cmd/orbital/main.go

# ---- DGraph tools stage ----
# Provides the dgraph binary for running dgraph live during restore.
# Must match the dgraph alpha version deployed alongside orbital.
FROM dgraph/dgraph:v25.3.1 AS dgraph-tools

# ---- Runtime stage ----
FROM alpine:3.21

RUN apk add --no-cache \
    curl \
    bash \
    bind-tools \
    netcat-openbsd \
    procps

WORKDIR /app

# Copy orbital binary + assets
COPY --from=builder /app/orbital .
COPY --from=builder /app/web ./web
COPY --from=builder /app/schema ./schema

# Copy dgraph binary for restore (dgraph live runs as a subprocess)
COPY --from=dgraph-tools /usr/local/bin/dgraph /usr/local/bin/dgraph

EXPOSE 8001

CMD ["./orbital"]
