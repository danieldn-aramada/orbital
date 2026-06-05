#!/usr/bin/env bash
# Seed local dev environment. FOR DEVELOPMENT USE ONLY — do not run against production.
# Run after orbital is started (migrations must have applied).
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

PSQL="${PSQL_CMD:-psql postgres://orbital:orbital@localhost:5432/orbital}"

bash scripts/seed-dgraph.sh

echo "==> Creating admin user..."
# bcrypt hash for password "admin" (cost 12)
HASH='$2a$12$Wb3DtBrZbW9528J/FKL81ON73s7PEPNkup9FN8JN.jGBtM03.sckG'
${PSQL} -c "
  INSERT INTO users (email, name, preferred_username, password_hash, verified, role, created_at)
  VALUES ('admin@armada.ai', 'Admin', 'admin@armada.ai', '${HASH}', true, 'admin', NOW())
  ON CONFLICT (email) DO UPDATE SET role = 'admin';
" >/dev/null

echo "==> Creating readonly user..."
# FOR DEV ONLY — weak password, do not seed in production
# bcrypt hash for password "user" (cost 12)
USER_HASH='$2a$12$dwBXGF5dTeZ88g3wz4..xeiyEGdzt/XblXlVi52tp8D1qXVRWV/Sa'
${PSQL} -c "
  INSERT INTO users (email, name, preferred_username, password_hash, verified, role, created_at)
  VALUES ('user@armada.ai', 'User', 'user@armada.ai', '${USER_HASH}', true, 'readonly', NOW())
  ON CONFLICT (email) DO NOTHING;
" >/dev/null

echo "==> Done. admin@armada.ai / admin  |  user@armada.ai / user"
