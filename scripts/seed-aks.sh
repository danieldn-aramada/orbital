#!/usr/bin/env bash
# Seed an AKS dev environment.
# Port-forwards DGraph blue and scratch, runs seed-dgraph.sh, then cleans up.
#
# Usage: ./scripts/seed-aks.sh [--namespace <ns>]
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

PIDS=()

cleanup() {
  echo ""
  echo "==> Cleaning up port-forwards..."
  for pid in "${PIDS[@]}"; do
    kill "$pid" 2>/dev/null || true
  done
}
trap cleanup EXIT

wait_for_port() {
  local port="$1"
  local label="$2"
  echo -n "    Waiting for ${label} on :${port}..."
  for i in $(seq 1 30); do
    if nc -z localhost "$port" 2>/dev/null; then
      echo " ready"
      return 0
    fi
    sleep 1
    echo -n "."
  done
  echo " timed out" >&2
  exit 1
}

echo "==> Starting port-forwards (namespace: ${NAMESPACE})..."

kubectl port-forward svc/dgraph-blue-dgraph-alpha    8080:8080 -n "$NAMESPACE" >/dev/null 2>&1 &
PIDS+=($!)

kubectl port-forward svc/dgraph-scratch-dgraph-alpha 8081:8080 -n "$NAMESPACE" >/dev/null 2>&1 &
PIDS+=($!)

kubectl port-forward svc/dgraph-scratch-dgraph-zero  6081:6080 -n "$NAMESPACE" >/dev/null 2>&1 &
PIDS+=($!)

wait_for_port 8080 "dgraph-blue"
wait_for_port 8081 "dgraph-scratch"
wait_for_port 6081 "dgraph-scratch-zero"

bash scripts/seed-dgraph.sh

echo "==> Done."
