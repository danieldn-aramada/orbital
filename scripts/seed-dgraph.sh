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

# apply_schema POSTs the GraphQL schema and VERIFIES it actually installed. DGraph
# can return {"code":"Success"} while the schema didn't take — notably right after a
# drop_all, when the cluster is still settling and the OLD schema stays live (this is
# why a rename like NetworkPort->NetworkInterface silently failed on AKS). So we:
#   (1) capture the HTTP status — the old `curl -sf` swallowed HTTP errors into an
#       empty body that passed the jq check, i.e. a silent false success; and
#   (2) introspect every object type declared in schema.graphql and retry until they
#       are all live, failing loudly if they never take.
apply_schema() {
  local label="$1"
  local url="$2"
  echo "==> Applying DGraph schema (${label})..."
  local want
  want=$(grep -oE '^type [A-Za-z0-9_]+' schema/schema.graphql | awk '{print $2}')
  local attempt
  for attempt in 1 2 3 4 5 6; do
    local out code body
    out=$(curl -s -w $'\n%{http_code}' -X POST "${url}/admin/schema" \
      -H "Content-Type: application/graphql" \
      --data-binary @schema/schema.graphql)
    code=$(printf '%s\n' "$out" | tail -1)
    body=$(printf '%s\n' "$out" | sed '$d')
    if [ "$code" != "200" ] || printf '%s' "$body" | jq -e '.errors' >/dev/null 2>&1; then
      echo "    attempt ${attempt}: apply failed (HTTP ${code}) — $(printf '%s' "$body" | jq -rc '.errors // .' 2>/dev/null)" >&2
      sleep 3
      continue
    fi
    # Verify every type in schema.graphql is introspectable (catches the raced apply).
    local live missing="" t
    live=$(curl -s -X POST "${url}/graphql" -H "Content-Type: application/json" \
      -d '{"query":"{ __schema { types { name } } }"}' | jq -r '.data.__schema.types[].name' 2>/dev/null)
    for t in $want; do
      printf '%s\n' "$live" | grep -qxF "$t" || missing="$missing $t"
    done
    if [ -z "$missing" ]; then
      echo "    schema applied + verified (${label})"
      return 0
    fi
    echo "    attempt ${attempt}: apply reported success but schema not live yet (missing:${missing} ) — retrying..." >&2
    sleep 3
  done
  echo "ERROR: schema apply did not take on ${label} after retries" >&2
  exit 1
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
  -d '{"query": "mutation { deleteNetworkInterface(filter: { has: orbId }) { numUids } deleteNetworkAdapter(filter: { has: orbId }) { numUids } deleteNetworkDevice(filter: { has: orbId }) { numUids } }"}' >/dev/null

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
