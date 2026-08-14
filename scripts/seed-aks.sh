#!/usr/bin/env bash
# Seed an AKS dev environment.
# Port-forwards DGraph blue and scratch, runs seed-dgraph.sh, then cleans up.
#
# Usage: ./scripts/seed-aks.sh [--namespace <ns>] [--clean]
#   Default namespace: netbox
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

NAMESPACE="netbox"
CLEAN_FLAG=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --namespace|-n) NAMESPACE="$2"; shift 2 ;;
    --clean)        CLEAN_FLAG="--clean"; shift ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

# Host ports we forward AKS DGraph onto. These are the SAME ports the local dev
# stack (make up) binds — local DGraph blue :8080, scratch :8081. If one is already
# in use when we start, kubectl port-forward silently fails to bind (the OS refuses
# the address), yet curl still finds a listener, so every seed mutation goes to the
# WRONG DGraph (local, not AKS) with no visible error. That footgun ate hours of
# debugging. Refuse to run rather than seed the wrong cluster — see preflight_ports.
FORWARD_PORTS=(8080 8081 6081)

PIDS=()
PF_LOGDIR="$(mktemp -d)"

cleanup() {
  echo ""
  echo "==> Cleaning up port-forwards..."
  if [ "${#PIDS[@]}" -gt 0 ]; then
    for pid in "${PIDS[@]}"; do
      kill "$pid" 2>/dev/null || true
    done
  fi
  rm -rf "$PF_LOGDIR"
}
trap cleanup EXIT

# Abort loudly if any forward port is already bound. The near-certain cause is a
# running local stack (make up), which would otherwise hijack this seed.
preflight_ports() {
  local busy=() p
  for p in "${FORWARD_PORTS[@]}"; do
    if nc -z localhost "$p" 2>/dev/null; then
      busy+=("$p")
    fi
  done
  if [ "${#busy[@]}" -gt 0 ]; then
    echo "ERROR: host port(s) already in use: ${busy[*]}" >&2
    echo "  These are needed to port-forward AKS DGraph in namespace '${NAMESPACE}'," >&2
    echo "  but they're occupied — almost certainly your local dev stack (make up) is" >&2
    echo "  running (local DGraph binds :8080/:8081). If left running, this seed would" >&2
    echo "  silently hit LOCAL DGraph instead of AKS." >&2
    echo "  Fix: run 'make down' (or stop whatever holds these ports), then retry." >&2
    exit 1
  fi
}

wait_for_port() {
  local port="$1" label="$2" log="$3"
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
  echo "    kubectl port-forward output for ${label}:" >&2
  sed 's/^/      /' "$log" >&2 2>/dev/null || true
  exit 1
}

preflight_ports

echo "==> Starting port-forwards (namespace: ${NAMESPACE})..."

kubectl port-forward svc/dgraph-blue-dgraph-alpha    8080:8080 -n "$NAMESPACE" >"$PF_LOGDIR/blue.log"    2>&1 &
PIDS+=($!)

kubectl port-forward svc/dgraph-scratch-dgraph-alpha 8081:8080 -n "$NAMESPACE" >"$PF_LOGDIR/scratch.log" 2>&1 &
PIDS+=($!)

kubectl port-forward svc/dgraph-scratch-dgraph-zero  6081:6080 -n "$NAMESPACE" >"$PF_LOGDIR/zero.log"    2>&1 &
PIDS+=($!)

wait_for_port 8080 "dgraph-blue"         "$PF_LOGDIR/blue.log"
wait_for_port 8081 "dgraph-scratch"      "$PF_LOGDIR/scratch.log"
wait_for_port 6081 "dgraph-scratch-zero" "$PF_LOGDIR/zero.log"

bash scripts/seed-dgraph.sh $CLEAN_FLAG

echo "==> Done."
