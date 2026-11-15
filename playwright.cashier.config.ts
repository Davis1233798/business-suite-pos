import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './tests/browser',
  testMatch: 'pos-cashier.spec.ts',
  workers: 1,
  retries: 0,
  use: { baseURL: 'http://127.0.0.1:18095', browserName: 'chromium', trace: 'retain-on-failure' },
  reporter: 'list',
  webServer: {
    command: 'npm run web:dev -- --host 127.0.0.1 --port 18095 --strictPort',
    url: 'http://127.0.0.1:18095',
    reuseExistingServer: !process.env.CI,
    timeout: 30000,
  },
});
