import { defineConfig } from '@playwright/test';

const headed = !!process.env.HEADED;

export default defineConfig({
  testDir: './e2e',
  testIgnore: ['**/orb.spec.ts', '**/smoke/**'],
  globalSetup: './e2e/global-setup.ts',
  workers: 1,
  use: {
    baseURL: 'http://localhost:8001',
    storageState: 'e2e/.auth.json',
    headless: !headed,
    launchOptions: {
      slowMo: headed ? 500 : 0,
    },
  },
  projects: [
    {
      name: 'chromium',
      use: { browserName: 'chromium' },
    },
  ],
});
