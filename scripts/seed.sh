#!/usr/bin/env bash
# Seed local dev environment.
# Run after orbital is started (migrations must have applied).
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

DGRAPH="http://localhost:8080"
PSQL="psql postgres://orbital:orbital@localhost:5432/orbital"

echo "==> Applying DGraph schema..."
curl -sf -X POST "${DGRAPH}/admin/schema" \
  -H "Content-Type: application/graphql" \
  --data-binary @schema/schema-demo.graphql >/dev/null

echo "==> Seeding DGraph..."
for f in examples/seed/*.graphql; do
  echo "    $(basename "$f" .graphql)"
  curl -sf -X POST "${DGRAPH}/graphql" \
    -H "Content-Type: application/json" \
    -d "{\"query\": $(jq -Rs . < "$f")}" >/dev/null
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
