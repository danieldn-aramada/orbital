# ---- Build stage ----
FROM golang:1.25.5-alpine AS builder

WORKDIR /app

ENV CGO_ENABLED=0

# Cache deps
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Build binary
ARG VERSION=dev
RUN go build -ldflags="-X github.com/armada/orbital/internal/version.Version=${VERSION}" -o orbital ./cmd/orbital/main.go

# ---- Runtime stage ----
FROM alpine:3.19

WORKDIR /app

# Copy binary + assets
COPY --from=builder /app/orbital .
COPY --from=builder /app/web ./web
COPY --from=builder /app/schema ./schema

EXPOSE 8001

CMD ["./orbital"]