#!/usr/bin/env bash
# Seed DGraph with schema and example data.
# Expects DGraph blue on :8080 and scratch on :8081.
#
# Usage: ./scripts/seed-dgraph.sh [--dgraph <url>]
#   Default: http://localhost:8080
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

DGRAPH="${DGRAPH_URL:-http://localhost:8080}"

apply_schema() {
  local label="$1"
  local url="$2"
  echo "==> Applying DGraph schema (${label})..."
  local resp
  resp=$(curl -sf -X POST "${url}/admin/schema" \
    -H "Content-Type: application/graphql" \
    --data-binary @schema/schema-demo.graphql)
  if echo "$resp" | jq -e '.errors' >/dev/null 2>&1; then
    echo "ERROR: schema apply failed (${label}):" >&2
    echo "$resp" | jq -r '.errors[].message' >&2
    exit 1
  fi
}

apply_schema "blue"    "http://localhost:8080"
apply_schema "scratch" "http://localhost:8081"

echo "==> Cleaning stale nodes..."
curl -sf -X POST "${DGRAPH}/graphql" \
  -H "Content-Type: application/json" \
  -d '{"query": "mutation { deleteRack(filter: { orbId: { eq: \"alaska-dot-cruiser:Rack-1\" } }) { numUids } }"}' >/dev/null
curl -sf -X POST "${DGRAPH}/graphql" \
  -H "Content-Type: application/json" \
  -d '{"query": "mutation { deleteIPAddress(filter: { has: orbId }) { numUids } }"}' >/dev/null

echo "==> Seeding DGraph..."
for f in examples/seed/*.graphql; do
  echo "    $(basename "$f" .graphql)"
  resp=$(curl -sf -X POST "${DGRAPH}/graphql" \
    -H "Content-Type: application/json" \
    -d "{\"query\": $(jq -Rs . < "$f")}")
  if echo "$resp" | jq -e '.errors' >/dev/null 2>&1; then
    echo "ERROR: seed failed for $(basename "$f"):" >&2
    echo "$resp" | jq -r '.errors[].message' >&2
    exit 1
  fi
done

echo "==> Done."
