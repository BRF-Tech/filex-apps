import { expect, test } from '@playwright/test';

// The public catalog page in each browser: it loads from the store's own
// origin, runs no script the policy refuses, and throws nothing.

test('the catalog page loads without script errors or policy violations', async ({ page }) => {
  const problems: string[] = [];
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
  page.on('console', (m) => {
    const t = m.text();
    if (/Content[- ]Security[- ]Policy|Refused to (load|execute|apply)/i.test(t)) problems.push(`csp: ${t}`);
  });
  const resp = await page.goto('/');
  expect(resp?.status()).toBe(200);
  await expect(page).toHaveTitle(/Example Apps/);
  await expect(page.getByRole('link', { name: 'Example Apps' })).toBeVisible();
  // Either the list (the DataTable, labelled "Apps") or the sentence that
  // says why there is none yet.
  await expect(page.locator('[aria-label="Apps"], [data-testid="catalog-note"]').first()).toBeVisible();
  expect(problems).toEqual([]);
});

test('every request of the page stays on the store origin', async ({ page, baseURL }) => {
  const foreign: string[] = [];
  page.on('request', (r) => {
    const u = new URL(r.url());
    if (u.protocol.startsWith('http') && u.origin !== new URL(baseURL!).origin) foreign.push(r.url());
  });
  await page.goto('/');
  await page.waitForLoadState('networkidle');
  expect(foreign).toEqual([]);
});

// The store's own brand (e2e/stack.env): its logo, served from its own origin
// under the page's policy, and the operator's line in the footer.
test('the page shows the store\'s own logo and who runs it', async ({ page }) => {
  await page.goto('/');
  const logo = page.getByTestId('brand-logo');
  await expect(logo).toBeVisible();
  await expect.poll(() => logo.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0)).toBe(true);
  const about = page.getByRole('navigation', { name: 'About this store' });
  await expect(about).toBeVisible();
  await expect(about.getByTestId('footer-operator')).toHaveText('Operated by Example Org');
  await expect(about.getByRole('link', { name: 'Example Org' })).toHaveAttribute('href', 'https://www.example.com/');
  await expect(about.getByRole('link', { name: 'Contact' })).toHaveAttribute('href', 'mailto:store@example.com');
  await expect(about.getByRole('link', { name: 'Terms of service' })).toHaveAttribute('href', 'https://www.example.com/terms');
  await expect(about.getByRole('link', { name: 'Privacy policy' })).toHaveAttribute('href', 'https://www.example.com/privacy');
});
