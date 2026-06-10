import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './e2e/release-check',
  globalSetup: './e2e/global-setup.ts',
  timeout: 60000,
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
