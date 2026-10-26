import { defineConfig, devices, type Project } from '@playwright/test';

// The store's end-to-end tests. They run inside the e2e stack
// (docker-compose.e2e.yml, service "playwright"), started by
// scripts/e2e.sh, never on a workstation's own browsers.
//
// Projects:
// - "api": HTTP-level checks of the stack (no browser), run once;
// - one project per browser for the pages and the full flow. Each browser
//   publishes its own app as its own GitHub account (e2e-pub-<browser>), so
//   the three never step on each other's data.
//
// E2E_BROWSERS selects browsers (chromium,firefox,webkit); E2E_GREP narrows
// the tests; E2E_OUT is where reports go.

const out = process.env.E2E_OUT ?? 'out';
const browsers: Record<string, Project['use']> = {
  chromium: { ...devices['Desktop Chrome'] },
  firefox: { ...devices['Desktop Firefox'] },
  webkit: { ...devices['Desktop Safari'] },
};
const wanted = (process.env.E2E_BROWSERS ?? 'chromium,firefox,webkit')
  .split(',')
  .map((s) => s.trim())
  .filter(Boolean);
for (const name of wanted) {
  if (!(name in browsers)) throw new Error(`E2E_BROWSERS: unknown browser "${name}"`);
}

export default defineConfig({
  testDir: './tests',
  outputDir: `${out}/test-results`,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: true,
  timeout: 120_000,
  expect: { timeout: 15_000 },
  grep: process.env.E2E_GREP ? new RegExp(process.env.E2E_GREP) : undefined,
  reporter: [
    ['list'],
    ['json', { outputFile: `${out}/results.json` }],
    ['html', { outputFolder: `${out}/report`, open: 'never' }],
  ],
  use: {
    baseURL: process.env.E2E_BASE_URL ?? 'https://fapps.e2e.test',
    // The stack's certificate comes from a CA made for the run.
    ignoreHTTPSErrors: true,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'api', testMatch: /api\/.*\.spec\.ts$/ },
    ...wanted.map((name) => ({ name, testMatch: /ui\/.*\.spec\.ts$/, use: browsers[name] })),
  ],
});
