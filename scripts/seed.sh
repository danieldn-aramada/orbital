#!/usr/bin/env bash
# Seed local dev environment.
# Run after orbital is started (migrations must have applied).
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

PSQL="${PSQL_CMD:-psql postgres://orbital:orbital@localhost:5432/orbital}"

bash scripts/seed-dgraph.sh

echo "==> Creating admin user..."
# bcrypt hash for password "admin" (cost 12)
HASH='$2a$12$Wb3DtBrZbW9528J/FKL81ON73s7PEPNkup9FN8JN.jGBtM03.sckG'
${PSQL} -c "
  INSERT INTO users (email, name, preferred_username, password_hash, verified, created_at)
  VALUES ('admin@armada.ai', 'Admin', 'admin@armada.ai', '${HASH}', true, NOW())
  ON CONFLICT (email) DO NOTHING;
" >/dev/null

echo "==> Done. admin@armada.ai / admin"
