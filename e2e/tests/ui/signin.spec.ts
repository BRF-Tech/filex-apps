import { expect, test } from '@playwright/test';
import { ADMIN_REPO, BASE, MOCK, identity, patientApi, patientGet, requireBackend, signInWithGitHub, startSignIn, stateHeaders } from '../support';

// The one sign-in, GitHub, through the store's sign-in page and the mock's
// page, in each browser. Reviewer and administrator come from the account's
// permission on the administration repository (e2e-org/store-admins in the
// mock), read by the store with its own token at every sign-in. Few sign-ins
// on purpose: the store allows 10 starts a minute per address and the whole
// run shares one.

test.beforeEach(async ({ request, context }) => {
  await patientApi(context);
  await requireBackend(request);
});

test('the sign-in page has one door, GitHub, and no Keycloak', async ({ page }) => {
  await page.goto('/sign-in');
  await expect(page.getByTestId('sign-in-github')).toHaveAttribute('href', /^\/auth\/github\/login/);
  await expect(page.getByTestId('sign-in-oidc')).toHaveCount(0);
  await expect(page.locator('a[href^="/auth/"]')).toHaveCount(1);
  await expect(page.locator('body')).not.toContainText(/keycloak/i);
  const me = await patientGet(page.request, '/api/v1/me');
  expect(me.status()).toBe(401);
  expect((await me.json()).providers).toEqual({ github: true });
});

test.describe('staff roles from the administration repository', () => {
  test('write on it makes an administrator', async ({ page }) => {
    await signInWithGitHub(page, 'e2e-admin');
    const me = await (await patientGet(page.request, '/api/v1/me')).json();
    expect(me.identity).toEqual({ provider: 'github', login: 'e2e-admin' });
    expect(me.roles).toEqual(['admin']);
    expect(me.role_source).toBe(`github repo permission ${ADMIN_REPO}: write`);
    await expect(page.getByTestId('nav-admin')).toBeVisible();
    await expect(page.getByTestId('nav-review')).toBeVisible();
    const health = await patientGet(page.request, '/api/v1/admin/health');
    expect(health.status()).toBe(200);
    const h = (await health.json()).roles;
    expect(h).toMatchObject({ repo: ADMIN_REPO, token_configured: true, status: 'ok' });
    // The validators the store reports are the stack's (docker-compose.yml,
    // production's image lines): both filex instances answer with their
    // version, and the storage plugin validator's heartbeat reaches the store
    // through the shared folder, though it has no network.
    const vr = await patientGet(page.request, '/api/v1/admin/validators');
    expect(vr.status()).toBe(200);
    const v = await vr.json();
    const items = (v.items as any[]).map((i) => ({ label: i.label, reachable: i.reachable, version: !!i.filex_version }));
    expect(items.sort((a, b) => a.label.localeCompare(b.label)), JSON.stringify(v.items)).toEqual([
      { label: 'floor', reachable: true, version: true },
      { label: 'latest', reachable: true, version: true },
    ]);
    expect(v.plugin_validators, JSON.stringify(v.plugin_validators)).toMatchObject([
      { label: 'plugins', kind: 'storage', reachable: true, platform: 'linux/amd64' },
    ]);
    expect(v.plugin_validators[0].filex_version, 'the plugin validator runs filex 0.55 or later').toMatch(/^v?0\.(5[5-9]|[6-9]\d)\./);
  });

  test('read on it gives no role, and the administration stays closed', async ({ page }) => {
    await signInWithGitHub(page, 'e2e-nobody');
    const me = await (await patientGet(page.request, '/api/v1/me')).json();
    expect(me.roles).toEqual([]);
    expect(me.role_source).toBe(`github repo permission ${ADMIN_REPO}: read`);
    await expect(page.getByTestId('nav-admin')).toHaveCount(0);
    await expect(page.getByTestId('nav-review')).toHaveCount(0);
    await page.goto('/admin');
    await expect(page.getByTestId('access-gate')).toHaveAttribute('data-state', 'role');
    for (const path of ['/api/v1/admin/publishers', '/api/v1/admin/health', '/api/v1/review/queue']) {
      const r = await patientGet(page.request, path);
      expect(r.status(), path).toBe(403);
      expect((await r.json()).error, path).toBe('forbidden');
    }
  });

  test('triage on it makes a reviewer: cookie flags, CSRF refusal, sign-out', async ({ page, context }) => {
    await signInWithGitHub(page, 'e2e-reviewer');
    const me = await (await patientGet(page.request, '/api/v1/me')).json();
    expect(me.roles).toEqual(['reviewer']);
    expect(me.role_source).toBe(`github repo permission ${ADMIN_REPO}: triage`);
    await expect(page.getByTestId('nav-review')).toBeVisible();
    await expect(page.getByTestId('nav-admin')).toHaveCount(0);
    expect((await patientGet(page.request, '/api/v1/review/queue')).status()).toBe(200);
    expect((await patientGet(page.request, '/api/v1/admin/publishers')).status()).toBe(403);

    const c = (await context.cookies(BASE)).find((x) => x.name === 'fapps_session');
    expect(c, 'fapps_session cookie').toBeTruthy();
    expect(c!.httpOnly).toBe(true);
    expect(c!.secure).toBe(true);
    expect(c!.sameSite).toBe('Lax');
    // A session with a role is short (12 hours).
    const hours = (c!.expires * 1000 - Date.now()) / 3_600_000;
    expect(hours).toBeGreaterThan(11);
    expect(hours).toBeLessThanOrEqual(12);

    // A state change with the right Origin but no token, then with a token
    // but another Origin: both refused, and the session survives them.
    const noToken = await page.request.post('/auth/logout', { headers: { Origin: BASE }, failOnStatusCode: false });
    expect(noToken.status()).toBe(403);
    expect((await noToken.json()).error).toBe('csrf_failed');
    const { 'X-CSRF-Token': token } = await stateHeaders(page);
    const otherOrigin = await page.request.post('/auth/logout', {
      headers: { 'X-CSRF-Token': token, Origin: 'https://evil.e2e.test' },
      failOnStatusCode: false,
    });
    expect(otherOrigin.status()).toBe(403);
    expect((await patientGet(page.request, '/api/v1/me')).status()).toBe(200);

    // The page's own "Sign out".
    await page.getByTestId('sign-out').click();
    await expect(page.getByTestId('sign-in')).toBeVisible();
    expect((await patientGet(page.request, '/api/v1/me')).status()).toBe(401);
  });
});

test.describe('GitHub sign-in (publishers)', () => {
  test('a publisher signs in with their GitHub account', async ({ page, browserName }) => {
    const id = identity(browserName);
    await signInWithGitHub(page, id.githubLogin);
    const me = await (await patientGet(page.request, '/api/v1/me')).json();
    expect(me.identity.provider).toBe('github');
    expect(me.identity.login).toBe(id.githubLogin);
    // Not a collaborator of the administration repository: no store role,
    // and a publisher's session lasts 7 days.
    expect(me.roles).toEqual([]);
    expect(me.role_source).toBe(`github repo permission ${ADMIN_REPO}: none`);
    await expect(page.getByTestId('who')).toContainText(id.githubLogin);
  });

  test('cancelling at GitHub leaves nobody signed in', async ({ page }) => {
    await startSignIn(page);
    await expect(page).toHaveURL(new RegExp(`^${MOCK}/github/login/oauth/authorize`));
    await page.getByTestId('mock-github-deny').click();
    await page.waitForURL((u) => u.origin === BASE);
    expect((await patientGet(page.request, '/api/v1/me')).status()).toBe(401);
  });
});
