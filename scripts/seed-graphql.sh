#!/usr/bin/env bash
# Seed DGraph by POSTing *.graphql mutation files.
# Port-forward DGraph (or orbital's /api/v1/graphql) before running.
#
# Usage: ./scripts/seed-graphql.sh [url]
#   Default: http://localhost:8080/graphql
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

URL="${1:-http://localhost:8080/graphql}"

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
