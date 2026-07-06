#!/usr/bin/env bash
# Seed MinIO with sample divergence snapshots so orbital's S3 poller has
# something to ingest. FOR LOCAL DEV ONLY.
#
# Usage:
#   bash scripts/seed-divergence-s3.sh
#
# What this does:
#   1. Inserts stub RegistryArtifact rows so orbital's ingester discovers the
#      seeded DCs (it only polls for DCs that have a completed publish).
#   2. Writes snapshot JSON files to s3://orbital/divergence/<repo>/<ts>.json
#      via the minio/mc container on the local compose network.
#
# The S3 poller is enabled by default in dev (config.go), so a running
# `make run-orbital` will ingest these snapshots within ~10s.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

PSQL="${PSQL_CMD:-psql postgres://orbital:orbital-local-dev-secret@localhost:5432/orbital}"
MC_IMAGE="minio/mc:RELEASE.2025-08-13T08-35-41Z"
COMPOSE_NETWORK="${COMPOSE_NETWORK:-local_default}"
S3_BUCKET="${S3_BUCKET:-orbital}"

# (datacenter_id, repository, snapshot_filename) tuples. Repository matches
# what cb-bundler would publish under for each DC. Filenames are RFC3339
# timestamps with colons removed (latest wins lexicographically).
DC_COLO_ID="colo:colo-galleon"
DC_COLO_REPO="orbital/colo-galleon"
DC_ALASKA_ID="alaska-dot:alaska-dot-galleon"
DC_ALASKA_REPO="orbital/alaska-dot-galleon"
SNAPSHOT_TS="2026-06-11T120000Z"

# ────────────────────────────────────────────────────────────────────────────
# Step 1 — stub RegistryArtifact rows so orbital's ingester discovers the DCs.
# ────────────────────────────────────────────────────────────────────────────
echo "==> Stubbing RegistryArtifact rows..."
# Idempotent: clear prior seed rows (identified by tag='seed') before inserting.
${PSQL} -v ON_ERROR_STOP=1 <<SQL >/dev/null
DELETE FROM registry_artifacts WHERE tag = 'seed';
INSERT INTO registry_artifacts (
  export_job_id, datacenter_id, datacenter_name, registry, repository, tag,
  status, initiated_at, completed_at, signed, enriched
) VALUES
  (gen_random_uuid(), '${DC_COLO_ID}',   'colo-galleon',       'localhost:5001', '${DC_COLO_REPO}',   'seed', 'completed', NOW(), NOW(), false, false),
  (gen_random_uuid(), '${DC_ALASKA_ID}', 'alaska-dot-galleon', 'localhost:5001', '${DC_ALASKA_REPO}', 'seed', 'completed', NOW(), NOW(), false, false);
SQL

# ────────────────────────────────────────────────────────────────────────────
# Step 2 — write snapshot JSON files to disk, then upload via mc.
# ────────────────────────────────────────────────────────────────────────────
TMPDIR=$(mktemp -d)
trap 'rm -rf "${TMPDIR}"' EXIT

mkdir -p "${TMPDIR}/divergence/${DC_COLO_REPO}"
mkdir -p "${TMPDIR}/divergence/${DC_ALASKA_REPO}"

cat > "${TMPDIR}/divergence/${DC_COLO_REPO}/${SNAPSHOT_TS}.json" <<JSON
{
  "publishedAt": "2026-06-11T12:00:00Z",
  "overrides": [
    {
      "orbId": "colo:3V5Y2Z3",
      "field": "oobMAC",
      "type": "Server",
      "intendedValue": "c8:4b:d6:a6:75:9b",
      "overrideValue": "aa:bb:cc:dd:ee:99",
      "who": "local:admin",
      "when": "2026-06-08T10:00:00Z"
    },
    {
      "orbId": "colo:JV8Y2Z3",
      "field": "hostname",
      "type": "Server",
      "intendedValue": "r10-u06.colo-galleon",
      "overrideValue": "srv-002-renamed.colo.internal",
      "who": "local:admin",
      "when": "2026-06-09T14:30:00Z"
    }
  ]
}
JSON

cat > "${TMPDIR}/divergence/${DC_ALASKA_REPO}/${SNAPSHOT_TS}.json" <<JSON
{
  "publishedAt": "2026-06-11T12:00:00Z",
  "overrides": [
    {
      "orbId": "alaska-dot:55W8K44",
      "field": "manufacturer",
      "type": "Server",
      "intendedValue": "Dell",
      "overrideValue": "Dell Inc.",
      "who": "local:admin",
      "when": "2026-06-07T16:00:00Z"
    }
  ]
}
JSON

echo "==> Uploading snapshots to s3://${S3_BUCKET}/divergence/ via mc..."
docker run --rm \
  --network "${COMPOSE_NETWORK}" \
  -v "${TMPDIR}:/seed:ro" \
  --entrypoint sh \
  "${MC_IMAGE}" -c "
    mc alias set local http://minio:9000 minioadmin minioadmin >/dev/null &&
    mc mb --ignore-existing local/${S3_BUCKET} >/dev/null &&
    mc cp --recursive /seed/divergence local/${S3_BUCKET}/
  "

echo "==> Done."
echo
echo "Snapshots uploaded:"
echo "  s3://${S3_BUCKET}/divergence/${DC_COLO_REPO}/${SNAPSHOT_TS}.json"
echo "  s3://${S3_BUCKET}/divergence/${DC_ALASKA_REPO}/${SNAPSHOT_TS}.json"
echo
echo "Orbital's poller is enabled by default in dev. /divergence-reports will"
echo "populate within ~10s of a running 'make run-orbital'."
