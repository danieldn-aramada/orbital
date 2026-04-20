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
RUN go build -o orbital ./cmd/server/orbital/main.go

# ---- Runtime stage ----
FROM alpine:3.19

WORKDIR /app

# Copy binary + static assets
COPY --from=builder /app/orbital .
COPY --from=builder /app/internal/static ./internal/static

EXPOSE 8001

CMD ["./orbital"]