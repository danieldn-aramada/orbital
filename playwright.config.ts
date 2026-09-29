import { defineConfig } from '@playwright/test';

const headed = !!process.env.HEADED;

// One config runs the orbital and orb UI test suites as separate Playwright
// projects: orbital against :8001 with logged-in storage state, orb against
// :8010 unauthenticated. The release-check suite (e2e/release-check/**) is excluded —
// that's `make release-check` and uses its own config.
export default defineConfig({
  testDir: './e2e',
  globalSetup: './e2e/global-setup.ts',
  workers: 1,

  // Everything runs against a LOCAL stack, so a control that is going to appear
  // appears in well under a second. The defaults (30s per test, 5s per
  // assertion, no cap on an action) are sized for network-bound CI, and here
  // they only decide how long a BROKEN test takes to admit it.
  //
  // Measured: deleting the bespoke Server page left 32 specs pointing at
  // selectors that no longer exist, and the suite went from 2 minutes to 9 —
  // eight of those minutes were dead waiting. A failing run is the one you are
  // most often sitting in front of, so it is the one worth making fast.
  //
  // Raise per-assertion where a test genuinely waits on real work (a publish, an
  // export, a DGraph restore) with an explicit `{ timeout: … }` at the call —
  // visible at the assertion rather than hidden in a global default.
  timeout: 20_000,
  expect: { timeout: 3_000 },
  use: {
    actionTimeout: 5_000,
    navigationTimeout: 10_000,
    headless: !headed,
    launchOptions: {
      slowMo: headed ? 500 : 0,
    },
  },
  projects: [
    {
      name: 'orbital',
      testIgnore: ['**/orb.spec.ts', '**/release-check/**'],
      use: {
        baseURL: 'http://localhost:8001',
        storageState: 'e2e/.auth.json',
      },
    },
    {
      name: 'orb',
      testMatch: 'orb.spec.ts',
      use: {
        baseURL: 'http://localhost:8010',
      },
    },
  ],
});
