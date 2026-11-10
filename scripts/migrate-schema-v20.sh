#!/usr/bin/env bash
# Carry data across the two fields schema v20 renamed. Run it AFTER applying v20.
#
#   NetworkDevice.serial          → NetworkDevice.serialNumber
#   IPAddress.kubernetesNodeIpv4  → IPAddress.kubernetesNodeIPv4
#
# A GraphQL rename is a new DQL predicate, so the values have to be copied.
# Safe to re-run. The old predicates are left in place, unreachable through
# GraphQL; drop them with `drop_attr` once the graph has been checked.
#
# Usage: ./scripts/migrate-schema-v20.sh [--dgraph <url>]
set -euo pipefail

DGRAPH="${DGRAPH_URL:-http://localhost:8080}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dgraph) DGRAPH="$2"; shift 2 ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

count() {
  curl -sf -X POST "$DGRAPH/query" -H 'Content-Type: application/dql' \
    --data-binary "{ q(func: has($1)) { n: count(uid) } }" | jq -r '.data.q[0].n // 0'
}

echo "DGraph: $DGRAPH"

curl -sf -X POST "$DGRAPH/mutate?commitNow=true" -H 'Content-Type: application/rdf' --data-binary '
upsert {
  query { d as var(func: has(NetworkDevice.serial)) { s as NetworkDevice.serial } }
  mutation { set { uid(d) <NetworkDevice.serialNumber> val(s) . } }
}' > /dev/null
echo "NetworkDevice.serial → serialNumber: $(count NetworkDevice.serial) → $(count NetworkDevice.serialNumber)"

pairs=$(curl -sf -X POST "$DGRAPH/query" -H 'Content-Type: application/dql' \
  --data-binary '{ q(func: has(IPAddress.kubernetesNodeIpv4)) { uid IPAddress.kubernetesNodeIpv4 { uid } } }' |
  jq -c '[.data.q[] | {uid, "IPAddress.kubernetesNodeIPv4": {uid: .["IPAddress.kubernetesNodeIpv4"].uid}}]')
if [[ "$pairs" != "[]" ]]; then
  curl -sf -X POST "$DGRAPH/mutate?commitNow=true" -H 'Content-Type: application/json' \
    --data-binary "{\"set\": $pairs}" > /dev/null
fi
echo "IPAddress.kubernetesNodeIpv4 → kubernetesNodeIPv4: $(count IPAddress.kubernetesNodeIpv4) → $(count IPAddress.kubernetesNodeIPv4)"
