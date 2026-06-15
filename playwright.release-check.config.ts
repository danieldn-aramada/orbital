import { defineConfig } from '@playwright/test';

// Release-check specs mutate shared orbital + orb state (backup, export,
// publish, restore, divergence ingest). They must run serially to avoid
// stepping on each other. fullyParallel:false + workers:1 ensures specs
// execute in alphabetical file order one at a time.
//
// Naming convention to enforce intra-file ordering (alphabetical):
//   - datacenter-edit.spec.ts          — UI smoke, independent
//   - release-checklist.spec.ts        — backup/export/publish/restore (publishes the
//                                        first artifact for colo-galleon, which the
//                                        divergence ingester needs to discover)
//   - z-divergence-pipeline.spec.ts    — divergence intake → S3 → orbital ingest
//                                        (depends on release-checklist's published
//                                        artifact; prefixed with `z-` to run after)
export default defineConfig({
  testDir: './e2e/release-check',
  globalSetup: './e2e/global-setup.ts',
  timeout: 60000,
  fullyParallel: false,
  workers: 1,
  use: {
    baseURL: 'http://localhost:8001',
    storageState: 'e2e/.auth.json',
  },
  projects: [
    {
      name: 'chromium',
      use: { browserName: 'chromium' },
    },
  ],
});
