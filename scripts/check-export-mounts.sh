#!/usr/bin/env bash
# Verify each DGraph alpha can actually WRITE to its /dgraph/export bind mount.
#
# Why this exists: DGraph's native export writes into a bind-mounted host
# directory. On macOS, if that host directory is deleted and recreated after the
# container started, the container keeps a reference to the old inode — the
# mountpoint still lists fine (`ls -ld` succeeds), but every write inside it
# fails with ENOENT. DGraph then reports only:
#
#     resolving export failed because task failed
#
# ...which names neither the mount nor the directory. Three integration tests
# failed that way for at least two days, read as a code defect, and were fixed
# by `docker compose restart`. A probe write is the cheapest thing that tells
# the truth, so the failure says what to do instead of what went wrong.
# Usage: scripts/check-export-mounts.sh [blue] [scratch] [test]   (default: all)
# Silent when every mount is writable or its container is not running; prints
# one warning per stale mount and exits 1.
set -uo pipefail

FAILED=0
check() {
  local container="$1" host_dir="$2" probe=".mount-probe-$$"
  # compose SERVICE name: strip the project prefix and the replica suffix.
  local service="${container#local-}"; service="${service%-1}"
  docker inspect "$container" >/dev/null 2>&1 || return 0
  if docker exec "$container" sh -c "touch /dgraph/export/$probe" 2>/dev/null &&
     [ -e "$host_dir/$probe" ]; then
    docker exec "$container" sh -c "rm -f /dgraph/export/$probe" 2>/dev/null
    return 0
  fi
  docker exec "$container" sh -c "rm -f /dgraph/export/$probe" 2>/dev/null
  rm -f "$host_dir/$probe" 2>/dev/null
  echo "warning: $service cannot write $host_dir (stale bind mount); exports and backups from it will fail."
  echo "         fix: docker compose -f deploy/local/docker-compose.yml restart $service"
  FAILED=1
}

[ $# -eq 0 ] && set -- blue scratch test
for cluster in "$@"; do
  case "$cluster" in
    blue)    check local-dgraph-alpha-1         .local/exports/blue ;;
    scratch) check local-dgraph-alpha-scratch-1 .local/exports/scratch ;;
    test)    check local-dgraph-alpha-test-1    .local/exports/test ;;
    *)       echo "unknown cluster: $cluster (want blue, scratch or test)"; exit 2 ;;
  esac
done
exit "$FAILED"
