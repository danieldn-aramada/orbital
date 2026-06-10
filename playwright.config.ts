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
  use: {
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
