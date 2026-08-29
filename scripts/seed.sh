#!/usr/bin/env bash
# Seed local dev environment. FOR DEVELOPMENT USE ONLY.
# Run after orbital is started (migrations must have applied).
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

PSQL="${PSQL_CMD:-psql postgres://orbital:orbital-local-dev-secret@localhost:5432/orbital}"

# This script creates accounts whose passwords are their usernames. That is
# fine on a laptop and catastrophic anywhere else, so refuse to run against a
# database that is not local. A comment saying "development only" is not a
# guard — PSQL_CMD is overridable and the script would cheerfully seed whatever
# it was pointed at.
#
# Override deliberately with SEED_ALLOW_REMOTE=1 if you genuinely mean it
# (a remote test fixture, say). There is no accidental path to it.
if [ "${SEED_ALLOW_REMOTE:-0}" != "1" ]; then
  case "${PSQL}" in
    *localhost*|*127.0.0.1*|*@/*|*'@ '*) ;;
    *)
      echo "REFUSING: PSQL_CMD does not look local, and this script seeds weak-password accounts." >&2
      echo "  PSQL_CMD = ${PSQL}" >&2
      echo "  Set SEED_ALLOW_REMOTE=1 if you really intend this." >&2
      exit 1
      ;;
  esac
fi

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
# FOR DEV ONLY — weak password
# bcrypt hash for password "user" (cost 12)
USER_HASH='$2a$12$dwBXGF5dTeZ88g3wz4..xeiyEGdzt/XblXlVi52tp8D1qXVRWV/Sa'
${PSQL} -c "
  INSERT INTO users (email, name, preferred_username, password_hash, verified, role, created_at)
  VALUES ('user@armada.ai', 'User', 'user@armada.ai', '${USER_HASH}', true, 'readonly', NOW())
  ON CONFLICT (email) DO NOTHING;
" >/dev/null

# TWO devs, not one. `dev` is the role that governs every mutation in the
# product, so it needs a seeded representative at all — and peer review needs
# two peers: with a single dev you can exercise `required_approvals: 1` and
# nothing else, and the first person to set 2 is back to creating identities by
# hand. See docs/reference/CHANGE-CONTROL.md.
echo "==> Creating dev users..."
# FOR DEV ONLY — weak password
# bcrypt hash for password "dev" (cost 12)
DEV_HASH='$2a$12$04ApjnkdNuE1kLbYHdYXAu/vRCZDjhhT4S/H04ytMjQAPr/D901e6'
${PSQL} -c "
  INSERT INTO users (email, name, preferred_username, password_hash, verified, role, created_at)
  VALUES ('dev@armada.ai',  'Dev One', 'dev@armada.ai',  '${DEV_HASH}', true, 'dev', NOW()),
         ('dev2@armada.ai', 'Dev Two', 'dev2@armada.ai', '${DEV_HASH}', true, 'dev', NOW())
  ON CONFLICT (email) DO NOTHING;
" >/dev/null

echo "==> Done."
echo "    admin@armada.ai / admin   (admin — bypasses approval policies)"
echo "    dev@armada.ai   / dev     (dev   — gated by approval policies)"
echo "    dev2@armada.ai  / dev     (dev   — the second reviewer for N-of-M)"
echo "    user@armada.ai  / user    (readonly)"
