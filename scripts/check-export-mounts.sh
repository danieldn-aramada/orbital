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
set -uo pipefail

FAILED=0
check() {
  local container="$1" host_dir="$2" probe=".mount-probe-$$"
  # compose SERVICE name: strip the project prefix and the replica suffix.
  local service="${container#local-}"; service="${service%-1}"
  if ! docker inspect "$container" >/dev/null 2>&1; then
    echo "  SKIP $container (not running)"
    return
  fi
  if docker exec "$container" sh -c "touch /dgraph/export/$probe" 2>/dev/null &&
     [ -e "$host_dir/$probe" ]; then
    docker exec "$container" sh -c "rm -f /dgraph/export/$probe" 2>/dev/null
    echo "  ok   $container -> $host_dir"
  else
    docker exec "$container" sh -c "rm -f /dgraph/export/$probe" 2>/dev/null
    rm -f "$host_dir/$probe" 2>/dev/null
    echo "  FAIL $container -> $host_dir (stale bind mount: container cannot write, or the host cannot see it)"
    echo "       fix: docker compose -f deploy/local/docker-compose.yml restart $service"
    FAILED=1
  fi
}

echo "Checking DGraph export mounts..."
check local-dgraph-alpha-1         .local/exports/blue
check local-dgraph-alpha-scratch-1 .local/exports/scratch
check local-dgraph-alpha-test-1    .local/exports/test

if [ "$FAILED" -ne 0 ]; then
  echo
  echo "Export and backup tests WILL fail with 'resolving export failed because task failed'"
  echo "until the affected container is restarted. Nothing is wrong with the code."
  exit 1
fi
