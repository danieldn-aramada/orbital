#!/usr/bin/env bash
# Seed AKS dev PostgreSQL with the admin user.
# Port-forwards orbital-postgres, creates the admin user, then cleans up.
#
# Usage: ./scripts/seed-aks-postgres.sh [--namespace <ns>]
#   Default namespace: netbox
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

NAMESPACE="netbox"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --namespace|-n) NAMESPACE="$2"; shift 2 ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

cleanup() {
  echo ""
  echo "==> Cleaning up port-forward..."
  kill "$PF_PID" 2>/dev/null || true
}
trap cleanup EXIT

echo "==> Starting port-forward (namespace: ${NAMESPACE})..."
kubectl port-forward svc/orbital-postgres 5432:5432 -n "$NAMESPACE" >/dev/null 2>&1 &
PF_PID=$!

echo -n "    Waiting for postgres on :5432..."
for i in $(seq 1 30); do
  if nc -z localhost 5432 2>/dev/null; then
    echo " ready"
    break
  fi
  sleep 1
  echo -n "."
  if [[ $i -eq 30 ]]; then
    echo " timed out" >&2
    exit 1
  fi
done

echo "==> Creating admin user..."
HASH='$2a$12$Wb3DtBrZbW9528J/FKL81ON73s7PEPNkup9FN8JN.jGBtM03.sckG'
psql postgres://orbital:orbital@localhost:5432/orbital -c "
  INSERT INTO users (email, name, preferred_username, password_hash, verified, created_at)
  VALUES ('admin@armada.ai', 'Admin', 'admin@armada.ai', '${HASH}', true, NOW())
  ON CONFLICT (email) DO NOTHING;
" >/dev/null

echo "==> Done. admin@armada.ai / admin"
