#!/usr/bin/env bash
# Seed local dev environment.
# Run after orbital is started (migrations must have applied).
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

DGRAPH="http://localhost:8080"
PSQL="psql postgres://orbital:orbital@localhost:5432/orbital"

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

echo "==> Creating admin user..."
# bcrypt hash for password "admin" (cost 12)
HASH='$2a$12$Wb3DtBrZbW9528J/FKL81ON73s7PEPNkup9FN8JN.jGBtM03.sckG'
${PSQL} -c "
  INSERT INTO users (email, name, preferred_username, password_hash, verified, created_at)
  VALUES ('admin@armada.ai', 'Admin', 'admin@armada.ai', '${HASH}', true, NOW())
  ON CONFLICT (email) DO NOTHING;
" >/dev/null

echo "==> Done. admin@armada.ai / admin"
