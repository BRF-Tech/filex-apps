// Browser measurements of the store's pages: real engines (Chromium,
// Firefox, WebKit), real layout at 1280 and 375 px, light and dark, axe
// accessibility rules, and screenshots for review.
// Runs in the official Playwright image (not a workstation's own
// browsers): `node node_modules/@playwright/test/cli.js test -c browser/playwright.config.ts`.
//
// Two targets:
// - default: the built bundle (`vite preview`) over the development mock
//   (mock/server.ts), every page and flow with realistic data;
// - FAPPS_REAL_URL=<origin>: the real Go server, which serves the embedded
//   bundle itself, with its own headers (CSP); only real.spec.ts runs.
// The end-to-end flows against the full stack are the deploy lane's (e2e/).
import { defineConfig, devices } from '@playwright/test';
import { fileURLToPath } from 'node:url';

const WEB = fileURLToPath(new URL('..', import.meta.url));
const REAL = process.env.FAPPS_REAL_URL ?? '';

export default defineConfig({
  testDir: '.',
  testMatch: REAL ? ['real.spec.ts'] : ['pages.spec.ts', 'flows.spec.ts'],
  outputDir: './out/results',
  timeout: 90_000,
  expect: { timeout: 10_000 },
  workers: 1,
  fullyParallel: false,
  reporter: [['list'], ['json', { outputFile: REAL ? 'out/report-real.json' : 'out/report.json' }]],
  use: { baseURL: REAL || 'http://127.0.0.1:4173', trace: 'off', locale: 'en-US', timezoneId: 'UTC' },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
    { name: 'firefox', use: { ...devices['Desktop Firefox'] } },
    { name: 'webkit', use: { ...devices['Desktop Safari'] } },
  ],
  webServer: REAL
    ? undefined
    : [
        {
          command: 'node --experimental-strip-types mock/server.ts',
          cwd: WEB,
          url: 'http://127.0.0.1:8080/healthz',
          env: { MOCK_PORT: '8080', MOCK_STEP_MS: '1200' },
          reuseExistingServer: false,
          timeout: 30_000,
        },
        {
          command: 'node node_modules/vite/bin/vite.js preview --host 127.0.0.1 --port 4173 --strictPort',
          cwd: WEB,
          url: 'http://127.0.0.1:4173/',
          env: { FAPPS_DEV_BACKEND: 'http://127.0.0.1:8080' },
          reuseExistingServer: false,
          timeout: 30_000,
        },
      ],
});
