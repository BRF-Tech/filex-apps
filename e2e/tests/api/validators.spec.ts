import { expect, test } from '@playwright/test';
import { MOCK, sha256Hex, validators } from '../support';

// The validators are real filex instances (0.56.0 "latest", 0.55.0 "floor").
// Before the store's pipeline asks them anything, this proves the wiring the
// pipeline relies on: each one signs the seeded admin in, reaches "GitHub"
// (the mock on 127.0.0.1:443 inside its namespace, over HTTPS with the run's
// CA), and answers the install dry run with the hashes of the bytes the mock
// serves. If a validator could reach the real GitHub, or skipped the sha256
// pin, these would not hold.

const email = process.env.E2E_VALIDATOR_EMAIL ?? 'validator@local';
const password = process.env.E2E_VALIDATOR_PASSWORD ?? '';

const cases = [
  { what: 'an interface-only app', repo: 'e2e-pub-chromium/hello', ref: 'v1.0.0', kind: 'app' },
  { what: 'a language pack', repo: 'e2e-pub-chromium/lang-e2e', ref: 'v0.1.0', kind: 'language_pack' },
];

for (const v of validators()) {
  test.describe(`validator ${v.label}`, () => {
    let token = '';

    test.beforeAll(async ({ playwright }) => {
      const ctx = await playwright.request.newContext();
      const r = await ctx.post(`${v.url}/api/auth/login`, { data: { email, password } });
      expect(r.status(), await r.text()).toBe(200);
      token = (await r.json()).token;
      expect(token).toBeTruthy();
      await ctx.dispose();
    });

    for (const c of cases) {
      test(`dry-runs ${c.what} from the fixture GitHub`, async ({ request }) => {
        const manifest = await request.get(`${MOCK}/raw/${c.repo}/${c.ref}/filex-app.json`);
        expect(manifest.status()).toBe(200);
        const want = sha256Hex(await manifest.body());

        const r = await request.post(`${v.url}/api/admin/app-plugins?dry_run=1`, {
          headers: { Authorization: `Bearer ${token}` },
          data: { github_repo: c.repo, ref: c.ref },
          failOnStatusCode: false,
        });
        const body = await r.json();
        expect(r.status(), JSON.stringify(body)).toBe(200);
        expect(body.kind).toBe(c.kind);
        expect(body.manifest_sha256).toBe(want);
        if (body.compat) expect(body.compat.ok).toBe(true);
      });
    }

    test('refuses a ref the repository does not have', async ({ request }) => {
      const r = await request.post(`${v.url}/api/admin/app-plugins?dry_run=1`, {
        headers: { Authorization: `Bearer ${token}` },
        data: { github_repo: 'e2e-pub-chromium/hello', ref: 'v9.9.9' },
        failOnStatusCode: false,
      });
      expect(r.status()).toBeGreaterThanOrEqual(400);
    });
  });
}
