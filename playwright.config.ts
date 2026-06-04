import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './e2e',
  testIgnore: ['**/orb.spec.ts', '**/smoke/**'],
  globalSetup: './e2e/global-setup.ts',
  workers: 1,
  use: {
    baseURL: 'http://localhost:8001',
    storageState: 'e2e/.auth.json',
    headless: false,
    launchOptions: {
      slowMo: 500,
    },
  },
  projects: [
    {
      name: 'chromium',
      use: { browserName: 'chromium' },
    },
  ],
});
