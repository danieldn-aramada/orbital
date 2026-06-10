/**
 * Pre-release smoke test: full operational checklist.
 *
 * Mirrors the manual pre-release verification steps:
 *   1.  Backup graph to MinIO
 *   2.  Export subgraph for colo-galleon
 *   3.  Publish artifact to Zot registry
 *   4.  Count ConfigItems in orbital DGraph (namespace=colo)
 *   5.  Orb imports the artifact from Zot
 *   6.  Assert orb ConfigItem count matches orbital
 *   7.  Mutate assetDataV2 on colo-galleon (sentinel value)
 *   8.  Restore from the backup taken in step 1
 *   9.  Assert assetDataV2 was reverted to pre-mutation value
 *
 * Prerequisites (run in separate terminals before this test):
 *   make up
 *   make run-orbital   (http://localhost:8001)
 *   make run-orb       (http://localhost:8010, with ORB_ENABLE_OCI_REGISTRY=true)
 *   make seed
 *
 * Run with:
 *   make test-smoke
 */

import { test, expect } from '@playwright/test';

// ── Constants ──────────────────────────────────────────────────────────────────

const ORB_BASE   = 'http://localhost:8010';
const ORB_DGRAPH = 'http://localhost:8082/graphql';
const ORB_DGRAPH_ADMIN = 'http://localhost:8082/admin';

// Orbital's DGraph — queried directly (no auth needed on port 8080).
const ORBITAL_DGRAPH = 'http://localhost:8080/graphql';

// Seeded values — must match examples/seed/colo-galleon.graphql.
const NAMESPACE  = 'colo';
const DC_ORB_ID  = 'colo:colo-galleon';

// Sentinel value written to assetDataV2 to verify restore reverts mutations.
const SENTINEL = '{"smoke_test_sentinel":true}';

// ── Polling helper ─────────────────────────────────────────────────────────────

async function pollUntil<T>(
  fn: () => Promise<T>,
  predicate: (val: T) => boolean,
  timeoutMs: number,
  intervalMs = 4000,
): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  let last: T | undefined;
  while (Date.now() < deadline) {
    last = await fn();
    if (predicate(last)) return last;
    await new Promise(r => setTimeout(r, intervalMs));
  }
  throw new Error(
    `pollUntil timed out after ${timeoutMs}ms. Last value: ${JSON.stringify(last)}`,
  );
}

// ── DGraph helpers (direct, no auth) ──────────────────────────────────────────

async function dgraphQuery(page: any, url: string, query: string, variables?: Record<string, any>): Promise<any> {
  const resp = await page.request.post(url, {
    data: variables ? { query, variables } : { query },
    headers: { 'Content-Type': 'application/json' },
  });
  if (!resp.ok()) {
    throw new Error(`DGraph request to ${url} failed: HTTP ${resp.status()} — ${await resp.text()}`);
  }
  const body = await resp.json();
  if (body.errors?.length) {
    throw new Error(`DGraph errors: ${JSON.stringify(body.errors)}`);
  }
  return body.data;
}

async function countConfigItems(page: any, dgraphURL: string, namespace: string): Promise<number> {
  const data = await dgraphQuery(
    page,
    dgraphURL,
    `query($ns: String!) {
      aggregateConfigItem(filter: { namespace: { eq: $ns } }) {
        count
      }
    }`,
    { ns: namespace },
  );
  return data?.aggregateConfigItem?.count ?? 0;
}

async function readAssetDataV2(page: any, dgraphURL: string, orbId: string): Promise<string | null> {
  const data = await dgraphQuery(
    page,
    dgraphURL,
    `query($orbId: String!) {
      queryDataCenter(filter: { orbId: { eq: $orbId } }) {
        assetDataV2
      }
    }`,
    { orbId },
  );
  const dcs: any[] = data?.queryDataCenter ?? [];
  if (dcs.length === 0) return null;
  return dcs[0].assetDataV2 ?? null;
}

// ── The test ───────────────────────────────────────────────────────────────────

test('pre-release checklist: backup → export → publish → orb import → mutate → restore', async ({ page }) => {
  // This test exercises the full pre-release pipeline end-to-end.
  // It runs against live orbital (:8001) and orb (:8010) servers.
  test.setTimeout(15 * 60 * 1000); // 15 minutes (restore alone can take 5+ min)

  // ── Step 1: Backup ─────────────────────────────────────────────────────────
  console.log('\n[smoke] ── Step 1: Backup ──────────────────────────────────');
  const backupTrigger = await page.request.post('/api/v1/backup');
  expect(backupTrigger.status(), 'backup trigger: expect 202').toBe(202);
  const { jobId: backupJobId } = await backupTrigger.json();
  expect(backupJobId, 'backup trigger: expect jobId').toBeTruthy();
  console.log(`[smoke] Backup job created: ${backupJobId}`);

  const backupJob = await pollUntil(
    async () => (await page.request.get(`/api/v1/backup/jobs/${backupJobId}`)).json(),
    j => ['completed', 'failed', 'skipped'].includes(j.status),
    120_000,
  );
  expect(
    backupJob.status,
    `backup job ${backupJobId} must reach "completed" (got "${backupJob.status}"${backupJob.error ? ': ' + backupJob.error : ''})`,
  ).toBe('completed');
  expect(backupJob.s3Key, 'completed backup must have s3Key').toBeTruthy();

  // Capture the backup's record ID for restore later.
  const backupRecordId: string = backupJob.id;
  console.log(`[smoke] Backup completed — id=${backupRecordId} s3Key=${backupJob.s3Key}`);

  // ── Step 2: Export ─────────────────────────────────────────────────────────
  console.log('\n[smoke] ── Step 2: Export ──────────────────────────────────');
  const exportTrigger = await page.request.post('/api/v1/export', {
    data: { orbId: DC_ORB_ID },
  });
  expect(exportTrigger.status(), 'export trigger: expect 202').toBe(202);
  const { jobId: exportJobId } = await exportTrigger.json();
  expect(exportJobId, 'export trigger: expect jobId').toBeTruthy();
  console.log(`[smoke] Export job created: ${exportJobId}`);

  const exportJob = await pollUntil(
    async () => (await page.request.get(`/api/v1/export/jobs/${exportJobId}`)).json(),
    j => ['completed', 'failed'].includes(j.status),
    120_000,
  );
  expect(
    exportJob.status,
    `export job ${exportJobId} must reach "completed" (got "${exportJob.status}"${exportJob.error ? ': ' + exportJob.error : ''})`,
  ).toBe('completed');
  console.log(`[smoke] Export completed — jobId=${exportJobId}`);

  // ── Step 3: Publish to Zot ─────────────────────────────────────────────────
  console.log('\n[smoke] ── Step 3: Publish ─────────────────────────────────');
  const publishResp = await page.request.post(`/api/v1/export/jobs/${exportJobId}/publish`);
  expect(publishResp.status(), 'publish: expect 202').toBe(202);
  const { artifactId, tag } = await publishResp.json();
  expect(artifactId, 'publish response: expect artifactId').toBeTruthy();
  expect(tag, 'publish response: expect tag').toBeTruthy();
  console.log(`[smoke] Publish job created: artifactId=${artifactId} tag=${tag}`);

  const artifact = await pollUntil(
    async () => (await page.request.get(`/api/v1/oci/artifacts/${artifactId}`)).json(),
    a => ['completed', 'failed'].includes(a.status),
    120_000,
  );
  expect(
    artifact.status,
    `artifact ${artifactId} must reach "completed" (got "${artifact.status}"${artifact.error ? ': ' + artifact.error : ''})`,
  ).toBe('completed');
  expect(artifact.digest, 'completed artifact must have digest').toBeTruthy();
  expect(artifact.signed, 'completed artifact must be signed').toBe(true);
  console.log(`[smoke] Published — tag=${tag} digest=${artifact.digest}`);

  // ── Step 4: Count ConfigItems in orbital DGraph ────────────────────────────
  console.log('\n[smoke] ── Step 4: Count orbital ConfigItems ───────────────');
  const orbitalCount = await countConfigItems(page, ORBITAL_DGRAPH, NAMESPACE);
  expect(
    orbitalCount,
    `orbital must have ConfigItems in namespace "${NAMESPACE}"`,
  ).toBeGreaterThan(0);
  console.log(`[smoke] Orbital ConfigItem count (namespace="${NAMESPACE}"): ${orbitalCount}`);

  // ── Step 5: Orb imports from Zot ──────────────────────────────────────────
  console.log(`\n[smoke] ── Step 5: Orb import tag="${tag}" ─────────────────`);
  const orbImportTrigger = await page.request.post(`${ORB_BASE}/api/v1/import`, {
    data: { tag },
  });
  expect(orbImportTrigger.status(), 'orb import trigger: expect 202').toBe(202);
  console.log(`[smoke] Orb import started (tag=${tag})...`);

  const orbImportState = await pollUntil(
    async () => (await page.request.get(`${ORB_BASE}/api/v1/import/status`)).json(),
    s => s.status === 'done' || s.status === 'failed',
    300_000, // 5 min — dgraph live loader is slow
    5000,
  );
  expect(
    orbImportState.status,
    `orb import must reach "done" (got "${orbImportState.status}"${orbImportState.error ? ': ' + orbImportState.error : ''})`,
  ).toBe('done');
  console.log('[smoke] Orb import completed');

  // ── Step 6: Assert orb ConfigItem count matches orbital ───────────────────
  console.log('\n[smoke] ── Step 6: Compare ConfigItem counts ───────────────');
  // Brief settle — DGraph needs a moment to finish indexing after live load.
  await new Promise(r => setTimeout(r, 3000));
  const orbCount = await countConfigItems(page, ORB_DGRAPH, NAMESPACE);
  console.log(`[smoke] Orb ConfigItem count (namespace="${NAMESPACE}"): ${orbCount}`);
  expect(
    orbCount,
    `orb count (${orbCount}) must equal orbital count (${orbitalCount}) for namespace "${NAMESPACE}"`,
  ).toBe(orbitalCount);

  // ── Step 7: Mutate assetDataV2 ────────────────────────────────────────────
  console.log('\n[smoke] ── Step 7: Mutate assetDataV2 ──────────────────────');
  const originalAssetData = await readAssetDataV2(page, ORBITAL_DGRAPH, DC_ORB_ID);
  console.log(`[smoke] Original assetDataV2 length: ${originalAssetData?.length ?? 0} chars`);

  // Apply the mutation through orbital's GraphQL proxy (auth via session cookie).
  const mutResp = await page.request.post('/graphql', {
    data: {
      query: `mutation UpdateDC($orbId: String!, $val: String!) {
        updateDataCenter(input: {
          filter: { orbId: { eq: $orbId } }
          set: { assetDataV2: $val }
        }) {
          dataCenter { orbId }
        }
      }`,
      variables: { orbId: DC_ORB_ID, val: SENTINEL },
    },
  });
  expect(mutResp.status(), 'mutation: expect 200').toBe(200);
  const mutBody = await mutResp.json();
  expect(mutBody.errors, `mutation must not return errors: ${JSON.stringify(mutBody.errors)}`).toBeFalsy();

  // Verify sentinel was written by reading back from DGraph directly.
  const mutatedValue = await readAssetDataV2(page, ORBITAL_DGRAPH, DC_ORB_ID);
  expect(
    mutatedValue,
    'assetDataV2 must equal sentinel value immediately after mutation',
  ).toBe(SENTINEL);
  console.log('[smoke] Mutation confirmed — sentinel value is present');

  // ── Step 8: Restore backup ────────────────────────────────────────────────
  console.log(`\n[smoke] ── Step 8: Restore backup ${backupRecordId} ─────────`);
  const restoreTrigger = await page.request.post('/api/v1/restore', {
    data: { backupId: backupRecordId },
  });
  expect(restoreTrigger.status(), 'restore trigger: expect 202').toBe(202);
  const { jobId: restoreJobId } = await restoreTrigger.json();
  expect(restoreJobId, 'restore trigger: expect jobId').toBeTruthy();
  console.log(`[smoke] Restore job created: ${restoreJobId} (this will take several minutes...)`);

  const restoreJob = await pollUntil(
    async () => (await page.request.get(`/api/v1/restore/jobs/${restoreJobId}`)).json(),
    j => ['completed', 'failed'].includes(j.status),
    600_000, // 10 min — drop_all + dgraph live + schema apply
    6000,
  );
  expect(
    restoreJob.status,
    `restore job ${restoreJobId} must reach "completed" (got "${restoreJob.status}"${restoreJob.error ? ': ' + restoreJob.error : ''})`,
  ).toBe('completed');
  console.log('[smoke] Restore completed');

  // ── Step 9: Assert assetDataV2 was reverted ───────────────────────────────
  console.log('\n[smoke] ── Step 9: Verify restore reverted mutation ─────────');
  // Brief settle — DGraph needs a moment after schema re-apply.
  await new Promise(r => setTimeout(r, 4000));

  const revertedValue = await readAssetDataV2(page, ORBITAL_DGRAPH, DC_ORB_ID);
  expect(
    revertedValue,
    'assetDataV2 must NOT contain sentinel after restore — restore did not revert the mutation',
  ).not.toBe(SENTINEL);
  // Compare as parsed JSON — DGraph does not guarantee key order in string fields.
  expect(
    JSON.parse(revertedValue ?? 'null'),
    `assetDataV2 must equal the original pre-mutation value after restore`,
  ).toEqual(JSON.parse(originalAssetData ?? 'null'));
  console.log(`[smoke] PASS — assetDataV2 correctly reverted to original value`);
  console.log('\n[smoke] ── All steps passed ────────────────────────────────\n');
});
