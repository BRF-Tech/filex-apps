import { expect, test } from '@playwright/test';
import { MOCK, sha256Hex } from '../support';

// The stack itself: the store behind its HTTPS edge, the way a real store is
// reached, with a hosting platform's environment. These hold on the skeleton
// and must keep holding as the lanes land.

test('the store answers /healthz through the HTTPS edge', async ({ request }) => {
  const r = await request.get('/healthz');
  expect(r.status()).toBe(200);
  const body = await r.json();
  expect(body.status).toBe('ok');
  expect(body.version).toBe('e2e');
});

test('every answer carries the security headers', async ({ request }) => {
  for (const path of ['/', '/healthz', '/api/v1/no-such-thing']) {
    const r = await request.get(path, { failOnStatusCode: false });
    const h = r.headers();
    expect(h['x-content-type-options'], path).toBe('nosniff');
    expect(h['x-frame-options'], path).toBe('DENY');
    expect(h['content-security-policy'], path).toContain("frame-ancestors 'none'");
    expect(h['content-security-policy'], path).toContain("script-src 'self'");
  }
});

test('an unknown API path is a JSON 404, not the web page', async ({ request }) => {
  for (const path of ['/api/v1/no-such-thing', '/v1/no-such-thing']) {
    const r = await request.get(path, { failOnStatusCode: false });
    expect(r.status(), path).toBe(404);
    expect((await r.json()).error, path).toBe('not_found');
  }
});

test('install links need a session; a token the store never made is a 404', async ({ request }) => {
  const r = await request.post('/api/v1/install-intents', { data: {}, failOnStatusCode: false });
  expect(r.status()).toBe(401);
  expect((await r.json()).error).toBe('unauthorized');
  const t = await request.get('/v1/install/' + 'A'.repeat(43), { failOnStatusCode: false });
  expect(t.status()).toBe(404);
  const v = await request.post('/v1/licenses/verify', { data: { key: 'nonsense', app: 'hello', instance_id: 'e2e' }, failOnStatusCode: false });
  expect(v.status()).toBe(200);
  expect((await v.json()).payload.result).toBe('invalid');
});

test('the web interface is built into the image', async ({ request }) => {
  const r = await request.get('/');
  expect(r.status()).toBe(200);
  expect(r.headers()['content-type']).toContain('text/html');
  expect(r.headers()['cache-control']).toBe('no-cache');
  const html = await r.text();
  const asset = html.match(/\/assets\/[^"']+\.js/)?.[0];
  expect(asset, 'a hashed script in index.html').toBeTruthy();
  const a = await request.get(asset!);
  expect(a.status()).toBe(200);
  expect(a.headers()['cache-control']).toContain('immutable');
});

test('the signed index is never an empty document', async ({ request }) => {
  const r = await request.get('/v1/index.json', { failOnStatusCode: false });
  if (r.status() === 503) {
    expect((await r.json()).error).toBe('index_not_ready');
    const s = await request.get('/v1/index.json.sig', { failOnStatusCode: false });
    expect(s.status()).toBe(503);
    return;
  }
  expect(r.status()).toBe(200);
  const body = await r.body();
  expect(r.headers()['etag']).toContain(sha256Hex(body));
  const doc = JSON.parse(body.toString('utf8'));
  expect(doc.schema).toBe(1);
  expect(doc.serial).toBeGreaterThanOrEqual(1);
  expect(String(r.headers()['x-index-serial'])).toBe(String(doc.serial));
});

test('the app received the platform-shaped environment', async ({ request }) => {
  const r = await request.get('/readyz', { failOnStatusCode: false });
  expect([200, 503]).toContain(r.status());
  const body = await r.json();
  const missing: string[] = body.missing ?? [];
  // What the platform injects and what the run's secrets provide must have
  // reached the process: none of these may be reported missing.
  for (const name of ['DATABASE_URL', 'FAPPS_PUBLIC_URL or APP_URL', 'FAPPS_SESSION_KEY', 'FAPPS_VALIDATORS']) {
    expect(missing, `readyz says ${name} is missing`).not.toContain(name);
  }
  // The migrations ran with a platform's narrow grants (the role owns its
  // database and nothing more): the database is connected and migrated.
  expect(missing.filter((m) => m.startsWith('database')), JSON.stringify(missing)).toEqual([]);
});

// The store's own name, logo and operator (e2e/stack.env), from its own
// origin: the page shell before any script, GET /api/v1/store, the logo.
test('the store presents itself with the name, logo and operator it was given', async ({ request }) => {
  const shell = await (await request.get('/')).text();
  expect(shell).toContain('<title>Example Apps</title>');
  expect(shell).toContain('<meta name="application-name" content="Example Apps"');
  const manifest = await (await request.get('/manifest.webmanifest')).json();
  expect(manifest.name).toBe('Example Apps');
  const r = await request.get('/api/v1/store');
  expect(r.status()).toBe(200);
  const info = await r.json();
  expect(info).toMatchObject({
    name: 'Example Apps',
    operator_name: 'Example Org',
    operator_url: 'https://www.example.com/',
    contact: 'mailto:store@example.com',
    terms_url: 'https://www.example.com/terms',
    privacy_url: 'https://www.example.com/privacy',
  });
  expect(info.logo_url).toMatch(/^\/api\/v1\/store\/logo\?v=[0-9a-f]{16}$/);
  const logo = await request.get(info.logo_url);
  expect(logo.status()).toBe(200);
  expect(logo.headers()['content-type']).toBe('image/svg+xml');
  expect(logo.headers()['content-security-policy']).toContain('sandbox');
  expect((await logo.body()).toString('utf8')).toContain('<svg');
});

test('the role check is set up: the store sees its administration repository, a visitor does not', async ({ request }) => {
  // FAPPS_ADMIN_TOKEN reached the app and its sidecar sees e2e-org/store-admins:
  // /readyz carries no warning (an unset token, a repository the token does not
  // see or a failing GitHub would each put one there).
  const ready = await (await request.get('/readyz', { failOnStatusCode: false })).json();
  expect(ready.warnings ?? [], JSON.stringify(ready.warnings)).toEqual([]);
  // The repository is private: without the server's token it does not exist.
  const r = await request.get(`${MOCK}/github-api/repos/e2e-org/store-admins`, { failOnStatusCode: false });
  expect(r.status()).toBe(404);
  // The Keycloak sign-in is gone from the store.
  const kc = await request.get('/auth/oidc/login', { failOnStatusCode: false, maxRedirects: 0 });
  expect(kc.status()).toBe(404);
});
