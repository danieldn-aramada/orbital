/**
 * Pre-release integration test: divergence pipeline (orb → S3 → orbital).
 *
 * Validates the bug class that surfaced during local e2e on 2026-06-13:
 *   - Orb publishes a divergence snapshot to S3 under one prefix
 *   - Orbital's ingester polls under a different prefix → silent no-op
 *
 * This test deliberately bypasses cb-controller (which lives in another repo
 * and requires minikube + CRDs to run). Instead it POSTs a synthetic override
 * directly to orb's /api/v1/divergence intake, then walks the rest of the
 * pipeline end-to-end:
 *
 *   orb intake → orb store → orb publish to S3 → orbital ingester poll →
 *   orbital divergence-entries table → exposed via /api/v1/divergence
 *
 * Prerequisites (must be running before this test — same as release-checklist):
 *   make up                # MinIO, orbital DGraph, postgres
 *   make run-orbital       # http://localhost:8001 (ingester polls every 10s)
 *   make run-orb           # http://localhost:8010 (orb's divergence intake + publisher)
 *   make seed              # data center + servers in orbital DGraph
 *
 * Also requires at least one published artifact for colo-galleon so orbital's
 * ingester knows what S3 prefix to poll — release-checklist runs first and
 * leaves one behind, OR run `make release-check` which runs both specs.
 */

import { test, expect } from '@playwright/test';

const ORB_BASE = 'http://localhost:8010';

// Sentinel override — uses the seeded colo-galleon DC + a known server.
// `colo:JQK3V64-idrac` is the iDRAC settings node's orbId, which is what
// cb-controller's divergence reporter naturally produces for sshEnabled.
const ENTRY_ORB_ID = 'colo:JQK3V64-idrac';
const SENTINEL_WHO = 'e2e:divergence-spec';
const SENTINEL_FIELD = 'sshEnabled';

async function pollUntil<T>(
  fn: () => Promise<T>,
  predicate: (val: T) => boolean,
  timeoutMs: number,
  intervalMs = 2000,
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

test('divergence pipeline: orb intake → S3 publish → orbital ingest', async ({ page }) => {
  test.setTimeout(2 * 60 * 1000); // 2 minutes — ingester polls every 10s

  // ── Step 1: POST a synthetic override to orb's intake ───────────────────────
  console.log('\n[divergence] ── Step 1: POST to orb /api/v1/divergence ──');
  const intakeResp = await page.request.post(`${ORB_BASE}/api/v1/divergence`, {
    data: {
      overrides: [
        {
          orbId: ENTRY_ORB_ID,
          type: 'IdracSettings',
          field: SENTINEL_FIELD,
          intendedValue: false,
          overrideValue: true,
          who: SENTINEL_WHO,
          when: new Date().toISOString(),
        },
      ],
    },
    headers: { 'Content-Type': 'application/json' },
  });
  expect(intakeResp.status(), 'orb intake: expect 200').toBe(200);
  const intakeBody = await intakeResp.json();
  expect(intakeBody.stored, 'orb intake: expect stored=1').toBe(1);

  // ── Step 2: assert orb stored the override ──────────────────────────────────
  console.log('[divergence] ── Step 2: assert orb has the entry ──');
  const orbList = await page.request.get(`${ORB_BASE}/api/v1/divergence`);
  expect(orbList.status()).toBe(200);
  const orbEntries: any[] = await orbList.json();
  const orbMatch = orbEntries.find(e => e.orbId === ENTRY_ORB_ID && e.field === SENTINEL_FIELD);
  expect(orbMatch, `orb /api/v1/divergence should include ${ENTRY_ORB_ID}/${SENTINEL_FIELD}`).toBeTruthy();
  expect(orbMatch.who).toBe(SENTINEL_WHO);

  // ── Step 3: trigger orb publish to S3 ──────────────────────────────────────
  console.log('[divergence] ── Step 3: POST orb /api/v1/divergence/publish ──');
  const publishResp = await page.request.post(`${ORB_BASE}/api/v1/divergence/publish`);
  expect(publishResp.status(), 'publish: expect 200').toBe(200);
  const { key: publishedKey } = await publishResp.json();
  expect(publishedKey, 'publish: expect S3 key').toMatch(/^divergence\//);
  console.log(`[divergence] Published key: ${publishedKey}`);

  // ── Step 4: wait for orbital ingester to pick up the snapshot ───────────────
  // Ingester polls every 10s; allow up to 25s for two cycles (one to confirm
  // discoverDCs picks up the artifact's repo, another to process the snapshot).
  // This is the gate that caught the registry-prefix bug — silent no-op until
  // ingester's prefix matched orb's published prefix.
  console.log('[divergence] ── Step 4: wait for orbital ingester ──');
  const ingestedEntries = await pollUntil(
    async () => {
      // Endpoint is /divergences (plural) — orbital's REST convention for
      // collections. Do NOT collapse to /divergence: the singular form 404s and
      // silently masks a working ingest pipeline as "empty result".
      const r = await page.request.get(
        `/api/v1/divergences?orbId=${encodeURIComponent(ENTRY_ORB_ID)}`,
      );
      if (!r.ok()) return [];
      const body = await r.json();
      return Array.isArray(body) ? body : (body.rows ?? []);
    },
    (entries) => entries.some((e: any) =>
      e.entryOrbId === ENTRY_ORB_ID && e.field === SENTINEL_FIELD,
    ),
    30_000,
  );
  const orbitalMatch = ingestedEntries.find(
    (e: any) => e.entryOrbId === ENTRY_ORB_ID && e.field === SENTINEL_FIELD,
  );
  expect(orbitalMatch, 'orbital should ingest the entry from S3').toBeTruthy();
  expect(orbitalMatch.who, 'who should round-trip from orb to orbital').toBe(SENTINEL_WHO);
  expect(orbitalMatch.overrideValue, 'overrideValue should round-trip').toBe(true);
  expect(orbitalMatch.intendedValue, 'intendedValue should round-trip').toBe(false);

  console.log('[divergence] ✓ full pipeline verified: orb intake → S3 → orbital ingest');
});
