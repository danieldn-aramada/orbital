#!/usr/bin/env bash
# Seed DGraph by POSTing *.graphql mutation files.
# Port-forward DGraph (or orbital's /api/v1/graphql) before running.
#
# Usage: ./scripts/seed-graphql.sh [url]
#   Default: http://localhost:8080/graphql
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

URL="${1:-http://localhost:8080/graphql}"

# Seed data is gitignored, so a clone has none. Without this the loop below runs
# once with the glob pattern itself as the filename and fails on the redirect.
if ! compgen -G "examples/seed/*.graphql" > /dev/null; then
  echo "ERROR: no files match examples/seed/*.graphql — nothing to seed." >&2
  echo "       examples/seed/ is gitignored; obtain the seed set out of band." >&2
  exit 1
fi

for f in examples/seed/*.graphql; do
  echo "==> $(basename "$f" .graphql)"
  resp=$(curl -sf -X POST "$URL" \
    -H "Content-Type: application/json" \
    -d "{\"query\": $(jq -Rs . < "$f")}")
  if echo "$resp" | jq -e '.errors' >/dev/null 2>&1; then
    echo "ERROR:" >&2
    echo "$resp" | jq -r '.errors[].message' >&2
    exit 1
  fi
done

echo "==> Done."
