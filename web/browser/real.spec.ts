// The interface on the real Go server (FAPPS_REAL_URL): the server serves
// the embedded bundle with its own headers, so this measures what a visitor
// gets - the CSP (script-src 'self', no inline script), the real answers of
// the public routes, sign-in when it is not configured, and the gates for
// signed-out visitors. Signed-in flows need the identity provider: the
// deploy lane's e2e stack (mock GitHub) drives those.
import { mkdirSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';

mkdirSync('browser/out/real', { recursive: true });

function watch(page: Page) {
  const problems: string[] = [];
  page.on('pageerror', (e) => {
    if (!/ResizeObserver loop/.test(e.message)) problems.push('pageerror: ' + e.message);
  });
  page.on('console', (m) => {
    const t = m.text();
    if (/Content[- ]Security[- ]Policy|Refused to (load|execute|apply)/i.test(t)) problems.push('csp: ' + t);
  });
  const store = new URL(String(process.env.FAPPS_REAL_URL)).origin;
  page.on('request', (r) => {
    const u = new URL(r.url());
    if (u.protocol.startsWith('http') && u.origin !== store) problems.push('foreign request: ' + r.url());
  });
  return problems;
}

for (const theme of ['light', 'dark'] as const) {
  for (const width of [1280, 375]) {
    test.describe(`${theme} ${width}px`, () => {
      test.use({ viewport: { width, height: 900 }, colorScheme: theme });

      test('the catalog answers from the real server', async ({ page, browserName }) => {
        const problems = watch(page);
        const resp = await page.goto('/');
        expect(resp?.status()).toBe(200);
        expect(resp?.headers()['content-security-policy']).toContain("script-src 'self'");
        await expect(page.locator('h1')).toHaveText('Apps for filex');
        await expect(page.locator('[data-testid="catalog-results"], [data-testid="catalog-note"]').first()).toBeVisible();
        await page.waitForLoadState('networkidle');
        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        expect(over).toBeLessThanOrEqual(0);
        // No stored choice here: the page follows the system ("System").
        const sys = await page.evaluate(() => ({
          mq: matchMedia('(prefers-color-scheme: dark)').matches,
          dark: document.documentElement.classList.contains('dark'),
        }));
        // The page follows what the engine reports. Playwright's Firefox
        // (1.59) does not report the emulated scheme to matchMedia here
        // (measured 2026-10-04: matches=false under colorScheme 'dark'), so
        // only the following is asserted there; the explicit Light/Dark
        // choice is measured in every engine by pages.spec.ts.
        expect(sys.dark, `prefers-color-scheme: dark matches=${sys.mq}`).toBe(sys.mq);
        if (browserName !== 'firefox') expect(sys.mq).toBe(theme === 'dark');
        expect(problems).toEqual([]);
        await page.screenshot({ path: `browser/out/real/catalog-${theme}-${width}.png`, fullPage: true });
      });
    });
  }
}

test('an app that does not exist says so', async ({ page }) => {
  const problems = watch(page);
  await page.goto('/apps/no-such-app');
  await expect(page.locator('h1')).toHaveText('Not found');
  await expect(page.locator('[data-testid="page-not-found"]')).toBeVisible();
  expect(problems).toEqual([]);
});

test('a sign-in the server has not configured is said on load, without probing /auth', async ({ page, request }) => {
  const problems = watch(page);
  const auth: string[] = [];
  page.on('request', (r) => {
    if (new URL(r.url()).pathname.startsWith('/auth/')) auth.push(r.url());
  });
  const me = await (await request.get('/api/v1/me', { failOnStatusCode: false })).json();
  await page.goto('/sign-in');
  await page.waitForLoadState('networkidle');
  for (const [id, on] of [['github', me.providers?.github]] as const) {
    const link = page.locator(`[data-testid="sign-in-${id}"]`);
    if (on === false) {
      await expect(link).toHaveAttribute('aria-disabled', 'true');
      await expect(page.locator(`[data-testid="sign-in-${id}-note"]`)).toContainText('not configured');
    } else {
      await expect(link).toHaveAttribute('href', new RegExp(`^/auth/${id}/login`));
    }
  }
  expect(auth, 'the page requested a sign-in route by itself').toEqual([]);
  expect(problems).toEqual([]);
});

test('signed-out visitors are asked to sign in, not shown an error', async ({ page }) => {
  const problems = watch(page);
  for (const path of ['/publish', '/submit', '/review', '/admin/publishers']) {
    await page.goto(path);
    await expect(page.locator('[data-testid="access-gate"]')).toHaveAttribute('data-state', 'sign-in');
  }
  expect(problems).toEqual([]);
});

test('the footer\'s links are the store\'s own signed index and keys', async ({ page, request }) => {
  await page.goto('/');
  for (const href of ['/v1/index.json', '/v1/index.json.sig', '/v1/keys.json']) {
    await expect(page.locator(`footer a[href="${href}"]`)).toBeVisible();
    const r = await request.get(href, { failOnStatusCode: false });
    expect([200, 503], `${href} answered ${r.status()}`).toContain(r.status());
  }
});
