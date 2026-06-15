#!/usr/bin/env bash
# Seed a divergence report on IdracSettings.sshEnabled for the colo:CWJHDX3
# server, mirroring the freeze/supersede flow we exercised in tests.
#
# Run AFTER:
#   make up
#   make seed              # creates the colo:colo-galleon DC + CWJHDX3 server
#   make run-orbital       # starts the ingester (poll interval ~10s)
#
# Default (no args): seed the initial pending divergence.
# Then walk through:
#   bash scripts/seed-divergence-idrac-test.sh           # initial divergence
#   # → accept via UI at http://localhost:8001/divergence-reports
#   bash scripts/seed-divergence-idrac-test.sh --same    # echo same override → freeze
#   bash scripts/seed-divergence-idrac-test.sh --diff    # different override → supersede
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

PSQL="${PSQL_CMD:-psql postgres://orbital:orbital@localhost:5432/orbital}"
MC_IMAGE="minio/mc:RELEASE.2025-08-13T08-35-41Z"
COMPOSE_NETWORK="${COMPOSE_NETWORK:-local_default}"
S3_BUCKET="${S3_BUCKET:-orbital}"

DC_ID="colo:colo-galleon"
DC_NAME="colo-galleon"
# DC_REPO_PATH is what the S3 path uses (NO registry host); REGISTRY_HOST is
# the OCI registry. The ingester's NormalizeRepoPath strips the first segment
# of registry_artifacts.repository as the host, so the row MUST include the
# host prefix or it over-strips and the ingester lists the wrong S3 prefix.
DC_REPO_PATH="orbital/colo-galleon"
REGISTRY_HOST="localhost:5001"
DC_REPO_FULL="${REGISTRY_HOST}/${DC_REPO_PATH}"
IDRAC_ORBID="colo:CWJHDX3-idrac"
FIELD="sshEnabled"

MODE="initial"
INTENDED="false"
OVERRIDE="true"
WHO="local:admin"

case "${1:-}" in
  --same)
    MODE="freeze"
    INTENDED="false"; OVERRIDE="true"; WHO="local:admin"   # identical to initial
    ;;
  --diff)
    MODE="supersede"
    INTENDED="false"; OVERRIDE="false"; WHO="drifted-again"  # override flipped
    ;;
  --help|-h)
    grep '^#' "$0" | sed 's/^# //; s/^#//'
    exit 0
    ;;
  "")
    ;;
  *)
    echo "unknown flag: $1 (use --same, --diff, or no flag)" >&2
    exit 2
    ;;
esac

echo "==> mode: ${MODE}  (intended=${INTENDED} override=${OVERRIDE} who=${WHO})"

# Step 1 — stub a RegistryArtifact for this DC so the ingester's discoverDCs
# picks it up. Idempotent: re-runs replace the seed row.
if [[ "${MODE}" == "initial" ]]; then
  echo "==> Stubbing RegistryArtifact for ${DC_ID}..."
  ${PSQL} -v ON_ERROR_STOP=1 <<SQL >/dev/null
DELETE FROM registry_artifacts WHERE tag = 'seed-idrac-test';
INSERT INTO registry_artifacts (
  export_job_id, datacenter_id, datacenter_name, registry, repository, tag,
  status, initiated_at, completed_at, signed, enriched
) VALUES (
  gen_random_uuid(), '${DC_ID}', '${DC_NAME}', '${REGISTRY_HOST}', '${DC_REPO_FULL}',
  'seed-idrac-test', 'completed', NOW(), NOW(), false, false
);
SQL
fi

# Step 2 — write a snapshot JSON and upload via the mc container on the compose network.
TMPDIR=$(mktemp -d)
trap 'rm -rf "${TMPDIR}"' EXIT

PUBLISHED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
KEY_TS=$(date -u +%Y-%m-%dT%H-%M-%SZ)
mkdir -p "${TMPDIR}/divergence/${DC_REPO_PATH}"
cat > "${TMPDIR}/divergence/${DC_REPO_PATH}/${KEY_TS}.json" <<JSON
{
  "publishedAt": "${PUBLISHED_AT}",
  "overrides": [
    {
      "orbId": "${IDRAC_ORBID}",
      "field": "${FIELD}",
      "type": "IdracSettings",
      "intendedValue": ${INTENDED},
      "overrideValue": ${OVERRIDE},
      "who": "${WHO}",
      "when": "${PUBLISHED_AT}"
    }
  ]
}
JSON

echo "==> Uploading s3://${S3_BUCKET}/divergence/${DC_REPO_PATH}/${KEY_TS}.json"
docker run --rm \
  --network "${COMPOSE_NETWORK}" \
  -v "${TMPDIR}:/seed:ro" \
  --entrypoint sh \
  "${MC_IMAGE}" -c "
    mc alias set local http://minio:9000 minioadmin minioadmin >/dev/null &&
    mc mb --ignore-existing local/${S3_BUCKET} >/dev/null &&
    mc cp --recursive /seed/divergence local/${S3_BUCKET}/
  " >/dev/null

case "${MODE}" in
  initial)
    cat <<MSG

==> Initial divergence seeded.

Wait ~10s for the ingester poll, then:
  1. Open http://localhost:8001/divergence-reports
     → ${DC_NAME} should appear with 1 pending entry (${IDRAC_ORBID}.${FIELD}: ${INTENDED} → ${OVERRIDE})
  2. Expand the row, click Accept on ${FIELD}, click Submit Decisions, confirm.
     → updateIdracSettings mutation fires; resolution row written.
  3. Open the CWJHDX3 server tab → Audit Log tab.
     → Both 'resolveDivergence' and 'updateIdracSettings' events should appear,
       with the red/green diff on the updateIdracSettings row.

Then test the two re-ingest paths:
  bash $0 --same    # same override → freeze (resolution intact, values unchanged)
  bash $0 --diff    # different override → supersede (resolution deleted, values updated)
MSG
    ;;
  freeze)
    cat <<MSG

==> Same-override snapshot uploaded. Within ~10s the ingester re-applies it.

Expected:
  - divergence_resolutions row for ${FIELD}: STILL there (accept)
  - divergence_entries row for ${FIELD}: override_value UNCHANGED (${OVERRIDE}), last_seen_at advanced

Verify:
  psql postgres://orbital:orbital@localhost:5432/orbital -c "
    SELECT e.field, e.override_value::text, e.who, e.last_seen_at,
           (SELECT action FROM divergence_resolutions r
            WHERE r.entry_orb_id = e.entry_orb_id AND r.field = e.field) AS resolution
    FROM divergence_entries e WHERE e.entry_orb_id = '${IDRAC_ORBID}' AND e.field = '${FIELD}';"
MSG
    ;;
  supersede)
    cat <<MSG

==> Different-override snapshot uploaded. Within ~10s the ingester re-applies it.

Expected:
  - divergence_resolutions row for ${FIELD}: GONE (superseded)
  - divergence_entries row for ${FIELD}: override_value UPDATED to ${OVERRIDE}, who updated to ${WHO}
  - orbital logs: "divergence ingester: superseded resolution — edge override changed since decision"

Verify:
  psql postgres://orbital:orbital@localhost:5432/orbital -c "
    SELECT e.field, e.override_value::text, e.who, e.last_seen_at,
           (SELECT action FROM divergence_resolutions r
            WHERE r.entry_orb_id = e.entry_orb_id AND r.field = e.field) AS resolution
    FROM divergence_entries e WHERE e.entry_orb_id = '${IDRAC_ORBID}' AND e.field = '${FIELD}';"

  Resolution column should be NULL. UI should show ${FIELD} as pending again.
MSG
    ;;
esac
