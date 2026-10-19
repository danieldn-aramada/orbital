import { defineConfig } from '@playwright/test';

// Docs screenshots — `make docs-screenshots`. Not part of `make test-e2e`: the
// default config's testDir is ./e2e and this spec lives beside the docs site.
//
// Expects the local stack seeded (`make seed` loads the docs-example namespace)
// and orbital started with the docs views overlay:
//
//   ORBITAL_VIEWS_OVERLAY_PATH=examples/docs/views-overlay.yaml make run-orbital
export default defineConfig({
  testDir: './docs/site/screenshots',
  globalSetup: './e2e/global-setup.ts',
  workers: 1,
  timeout: 30_000,
  expect: { timeout: 5_000 },
  use: {
    baseURL: 'http://localhost:8001',
    storageState: 'e2e/.auth.json',
    viewport: { width: 1440, height: 900 },
    colorScheme: 'light',
  },
});
