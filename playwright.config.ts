import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './e2e',
  testIgnore: '**/orb.spec.ts',
  globalSetup: './e2e/global-setup.ts',
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
