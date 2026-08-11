#!/usr/bin/env bash
# Seed DGraph with schema and example data.
# Expects DGraph blue on :8080 and scratch on :8081.
#
# Usage: ./scripts/seed-dgraph.sh [--dgraph <url>] [--clean]
#   Default: http://localhost:8080
#   --clean: drop all data from DGraph blue before seeding (clean slate)
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

DGRAPH="${DGRAPH_URL:-http://localhost:8080}"
CLEAN=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dgraph) DGRAPH="$2"; shift 2 ;;
    --clean)  CLEAN=true; shift ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

apply_schema() {
  local label="$1"
  local url="$2"
  echo "==> Applying DGraph schema (${label})..."
  local resp
  resp=$(curl -sf -X POST "${url}/admin/schema" \
    -H "Content-Type: application/graphql" \
    --data-binary @schema/schema.graphql)
  if echo "$resp" | jq -e '.errors' >/dev/null 2>&1; then
    echo "ERROR: schema apply failed (${label}):" >&2
    echo "$resp" | jq -r '.errors[].message' >&2
    exit 1
  fi
}

if [ "$CLEAN" = "true" ]; then
  echo "==> Dropping all data from DGraph blue (--clean)..."
  drop_resp=$(curl -sf -X POST "${DGRAPH}/alter" \
    -H "Content-Type: application/json" \
    -d '{"drop_all": true}')
  echo "    Response: ${drop_resp}"
  if echo "$drop_resp" | jq -e '.errors' >/dev/null 2>&1; then
    echo "ERROR: drop_all failed:" >&2
    echo "$drop_resp" | jq -r '.errors[].message' >&2
    exit 1
  fi
  echo "    Done."
fi

apply_schema "blue"    "http://localhost:8080"
apply_schema "scratch" "http://localhost:8081"

echo "==> Cleaning stale nodes..."
curl -sf -X POST "${DGRAPH}/graphql" \
  -H "Content-Type: application/json" \
  -d '{"query": "mutation { deleteRack(filter: { orbId: { eq: \"alaska-dot-cruiser:Rack-1\" } }) { numUids } }"}' >/dev/null
curl -sf -X POST "${DGRAPH}/graphql" \
  -H "Content-Type: application/json" \
  -d '{"query": "mutation { deleteIPAddress(filter: { has: orbId }) { numUids } }"}' >/dev/null
# Network nodes are fully regenerated from *-network.graphql each seed → delete-all so removed
# devices/adapters/ports/edges don't linger under upsert (mirrors the IPAddress delete above).
curl -sf -X POST "${DGRAPH}/graphql" \
  -H "Content-Type: application/json" \
  -d '{"query": "mutation { deleteNetworkPort(filter: { has: orbId }) { numUids } deleteNetworkAdapter(filter: { has: orbId }) { numUids } deleteNetworkDevice(filter: { has: orbId }) { numUids } }"}' >/dev/null

seed_file() {
  local f="$1"
  echo "    $(basename "$f" .graphql)"
  local resp
  resp=$(curl -sf -X POST "${DGRAPH}/graphql" \
    -H "Content-Type: application/json" \
    -d "{\"query\": $(jq -Rs . < "$f")}")
  if echo "$resp" | jq -e '.errors' >/dev/null 2>&1; then
    echo "ERROR: seed failed for $(basename "$f"):" >&2
    echo "$resp" | jq -r '.errors[].message' >&2
    exit 1
  fi
}

echo "==> Seeding DGraph (base)..."
for f in examples/seed/*.graphql; do
  case "$(basename "$f" .graphql)" in
    *-idrac|*-storage|*-clusters|*-network) continue ;;
  esac
  seed_file "$f"
done

echo "==> Seeding DGraph (supplementary)..."
# Clusters must come after base — they reference DataCenters + Servers seeded above.
for f in examples/seed/*-idrac.graphql examples/seed/*-storage.graphql examples/seed/*-clusters.graphql examples/seed/*-network.graphql; do
  [ -f "$f" ] || continue
  seed_file "$f"
done

echo "==> Done."
