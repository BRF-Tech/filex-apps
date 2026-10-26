import { createHash, createPublicKey, verify } from 'node:crypto';
import { expect, test, type APIRequestContext, type APIResponse, type BrowserContext, type Page } from '@playwright/test';

// Shared helpers of the end-to-end specs.

export const MOCK = process.env.E2E_MOCK_URL ?? 'https://mock-idp:9443';
export const BASE = process.env.E2E_BASE_URL ?? 'https://fapps.e2e.test';

/** The validators of the stack, from E2E_VALIDATORS ("label=url,..."). */
export function validators(): { label: string; url: string }[] {
  return (process.env.E2E_VALIDATORS ?? '')
    .split(',')
    .map((p) => p.trim())
    .filter(Boolean)
    .map((p) => {
      const [label, url] = p.split('=');
      return { label, url: url.replace(/\/+$/, '') };
    });
}

/** Each browser acts as its own publisher, on its own fixture repository. */
export function identity(browserName: string) {
  const variant = { chromium: 'Chromium', firefox: 'Firefox', webkit: 'WebKit' }[browserName] ?? browserName;
  return {
    githubLogin: `e2e-pub-${browserName}`,
    publisherId: `e2e-${browserName}`,
    repo: `e2e-pub-${browserName}/hello`,
    appName: `e2e-hello-${browserName}`,
    label: `E2E Hello (${variant})`,
  };
}

export function sha256Hex(b: Buffer | string): string {
  return createHash('sha256').update(b).digest('hex');
}

/**
 * filex's detached-signature scheme (plugin.VerifyDetached): ed25519 over the
 * lower-hex sha256 TEXT of the signed bytes; the signature in hex.
 */
export function verifyDetached(body: Buffer, sigText: string, publicHex: string): boolean {
  const spki = Buffer.concat([Buffer.from('302a300506032b6570032100', 'hex'), Buffer.from(publicHex, 'hex')]);
  const key = createPublicKey({ key: spki, format: 'der', type: 'spki' });
  return verify(null, Buffer.from(sha256Hex(body)), key, Buffer.from(sigText.trim(), 'hex'));
}

/**
 * A signed answer of the store that is not the index (a license verdict, an
 * install intent), checked the way a filex does but written here
 * independently of the Go code: the payload's canonical JSON (keys sorted,
 * no whitespace), its lower-hex sha256, ed25519.
 */
export function verifyEnvelope(env: { payload: unknown; signature: string }, publicHex: string): boolean {
  const sorted = (v: unknown): unknown => {
    if (Array.isArray(v)) return v.map(sorted);
    if (v && typeof v === 'object') {
      const o = v as Record<string, unknown>;
      return Object.fromEntries(Object.keys(o).sort().map((k) => [k, sorted(o[k])]));
    }
    return v;
  };
  return verifyDetached(Buffer.from(JSON.stringify(sorted(env.payload)), 'utf8'), env.signature, publicHex);
}

/**
 * Skips the current test while the backend's routes are not on the branch
 * (the skeleton answered every /api path with JSON 404 not_found). Probes
 * /api/v1/me (401 once it exists), never an /auth route: those count against
 * the sign-in rate limit.
 */
export async function requireBackend(request: APIRequestContext) {
  const r = await request.get('/api/v1/me', { failOnStatusCode: false });
  let missing = false;
  if (r.status() === 404) missing = (await r.json().catch(() => ({})))?.error === 'not_found';
  test.skip(missing, 'the backend routes are not on this branch yet (skeleton fallback)');
}

/**
 * Starts a sign-in the way a person does: the sign-in page's button. The
 * page learns which sign-ins exist from /api/v1/me (no /auth probe), so one
 * click is one start. The store still allows only 10 starts a minute per
 * address and every test of the run shares one, so a refusal (JSON
 * `rate_limited` page) waits the time it names and tries again; any other
 * outcome fails the test.
 */
export async function startSignIn(page: Page) {
  const mockOrigin = new URL(MOCK).origin;
  for (let attempt = 1; ; attempt++) {
    await page.goto('/sign-in');
    await page.getByTestId('sign-in-github').click();
    const refused = page.getByText('rate_limited');
    const left = await Promise.race([
      page.waitForURL((u) => u.origin === mockOrigin, { timeout: 20_000 }).then(() => true),
      refused.waitFor({ timeout: 20_000 }).then(() => false),
    ]);
    if (left) return;
    const text = await page.locator('body').innerText();
    const secs = Number(text.match(/try again in (\d+) seconds/)?.[1] ?? 0);
    if (!secs || attempt >= 4) throw new Error(`the sign-in start was refused: ${text}`);
    test.info().annotations.push({ type: 'rate limit', description: `waited ${secs + 1}s: ${text}` });
    await page.waitForTimeout((secs + 1) * 1000);
  }
}

/**
 * Every test of the run comes from one address, and the store allows 120 API
 * calls a minute per address: a page that gets a 429 from an /api call is
 * asked again after the Retry-After it names, as a person reloading would.
 * (Calls made through page.request are not routed; those are the tests' own
 * reads.)
 */
export async function patientApi(context: BrowserContext) {
  await context.route('**/api/**', async (route) => {
    try {
      for (let attempt = 0; ; attempt++) {
        const res = await route.fetch();
        if (res.status() !== 429 || attempt >= 4) return await route.fulfill({ response: res });
        const secs = Number(res.headers()['retry-after'] ?? 1);
        await new Promise((r) => setTimeout(r, (secs + 0.5) * 1000));
      }
    } catch {
      // While a 429 was waited out the page moved on (a navigation, a
      // sign-in redirect) or the test ended: nobody is left to answer, and
      // throwing here would fail whichever test runs at that moment.
    }
  });
}

/**
 * The tests' own reads (page.request / request, which patientApi does not
 * route) wait out a 429 the same way: the whole run shares the store's 120
 * API calls a minute per address.
 */
export async function patientGet(request: APIRequestContext, path: string): Promise<APIResponse> {
  for (let attempt = 0; ; attempt++) {
    const res = await request.get(path, { failOnStatusCode: false });
    if (res.status() !== 429 || attempt >= 4) return res;
    const secs = Number(res.headers()['retry-after'] ?? 1);
    test.info().annotations.push({ type: 'rate limit', description: `${path}: waited ${secs + 0.5}s` });
    await new Promise((r) => setTimeout(r, (secs + 0.5) * 1000));
  }
}

/**
 * Signs in through the store's sign-in page and the mock GitHub: the one
 * sign-in, for publishers and staff alike. The staff accounts of the mock
 * (e2e/fixtures/repos.json, e2e-org/store-admins): e2e-admin has write
 * (administrator), e2e-reviewer triage (reviewer), e2e-nobody read (no role).
 */
export async function signInWithGitHub(page: Page, login: string) {
  await startSignIn(page);
  await expect(page).toHaveURL(new RegExp(`^${MOCK}/github/login/oauth/authorize`));
  await page.getByTestId(`mock-github-user-${login}`).click();
  await page.waitForURL((u) => u.origin === BASE);
  // The page's own /api/v1/me may wait out a 429 first (patientApi honours
  // Retry-After, up to a minute): longer than the default 15 seconds.
  await expect(page.getByTestId('who')).toBeVisible({ timeout: 90_000 });
}

/** The administration repository of the e2e stack (e2e/stack.env FAPPS_ADMIN_REPO). */
export const ADMIN_REPO = 'e2e-org/store-admins';

/** The session's CSRF token and the headers a state change needs. */
export async function stateHeaders(page: Page) {
  const me = await patientGet(page.request, '/api/v1/me');
  expect(me.status(), 'signed in').toBe(200);
  const token = (await me.json()).csrf_token as string;
  expect(token, 'csrf_token').toBeTruthy();
  return { 'X-CSRF-Token': token, Origin: BASE };
}
