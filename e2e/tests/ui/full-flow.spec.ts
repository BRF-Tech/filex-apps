import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync } from 'node:fs';
import { expect, test, type APIRequestContext, type BrowserContext, type Page } from '@playwright/test';
import { ADMIN_REPO, BASE, identity, patientApi, patientGet, requireBackend, signInWithGitHub, verifyDetached, verifyEnvelope } from '../support';

// The whole publishing flow, once per browser, each browser as its own
// publisher, through the store's own pages:
// sign in with GitHub -> ask for a publisher (form) -> an administrator (write
// on the administration repository) signs in with GitHub too and activates it
// (Administration -> Publishers) -> then for
// each subject: submit a tag (form) -> the pipeline passes on both validators
// -> approve (the submission page) -> the catalog lists it for an anonymous
// visitor -> the signed index verifies (independently here, and with
// `fapps verify-index`) -> yank (the app page) publishes a new index that
// marks the version.
//
// Two subjects: a language pack (a manifest only) and an app with its own
// interface (a release asset pinned by sha256). A page keeps every one of its
// dialogs in the document (one shown at a time), so a dialog's controls are
// picked among the visible ones. The API is read to assert
// what the pages did (status, checks, index), never to act in their place.

test.describe.configure({ mode: 'serial' });

type Subject = { what: string; repo: string; tag: string; version: string; name: string; label: string };

function subjects(browserName: string): Subject[] {
  const id = identity(browserName);
  const variant = id.label.replace(/^E2E Hello /, '');
  return [
    { what: 'a language pack', repo: `${id.githubLogin}/lang-e2e`, tag: 'v0.1.0', version: '0.1.0',
      name: `lang-e2e-${browserName}`, label: `E2E language pack ${variant}` },
    { what: 'an app with an interface', repo: id.repo, tag: 'v1.0.0', version: '1.0.0', name: id.appName, label: id.label },
  ];
}

let pubCtx: BrowserContext;
let adminCtx: BrowserContext;
let pub: Page;
let admin: Page;

test.beforeAll(async ({ browser, playwright }) => {
  const probe = await playwright.request.newContext({ baseURL: BASE, ignoreHTTPSErrors: true });
  await requireBackend(probe);
  await probe.dispose();
  pubCtx = await browser.newContext({ baseURL: BASE, ignoreHTTPSErrors: true });
  adminCtx = await browser.newContext({ baseURL: BASE, ignoreHTTPSErrors: true });
  await patientApi(pubCtx);
  await patientApi(adminCtx);
  pub = await pubCtx.newPage();
  admin = await adminCtx.newPage();
});

test.afterAll(async () => {
  await pubCtx?.close();
  await adminCtx?.close();
});

test('the publisher signs in with GitHub and asks for a publisher', async ({ browserName }) => {
  const id = identity(browserName);
  await signInWithGitHub(pub, id.githubLogin);
  await pub.goto('/publish/new-publisher');
  await pub.getByTestId('np-id').fill(id.publisherId);
  await pub.getByTestId('np-name').fill(`E2E ${browserName}`);
  await pub.getByTestId('np-owner').fill(id.githubLogin);
  await pub.getByTestId('np-send').click();
  await expect(pub.getByTestId('publisher-requested')).toBeVisible();
});

test('an administrator signs in with GitHub and activates it', async ({ browserName }) => {
  const id = identity(browserName);
  await signInWithGitHub(admin, 'e2e-admin');
  const me = await (await patientGet(admin.request, '/api/v1/me')).json();
  expect(me.identity.provider).toBe('github');
  expect(me.roles).toEqual(['admin']);
  expect(me.role_source).toBe(`github repo permission ${ADMIN_REPO}: write`);
  await admin.getByTestId('nav-admin').click();
  await expect(admin.getByTestId('admin-publishers')).toBeVisible();
  await admin.getByTestId(`pub-actions-${id.publisherId}`).click();
  await admin.getByRole('menuitem', { name: 'Activate', exact: true }).click();
  await expect
    .poll(async () => publisherStatus(admin.request, id.publisherId), { timeout: 15_000 })
    .toBe('active');
});

for (const index of [0, 1]) {
  test.describe(`subject ${index + 1}`, () => {
    let s: Subject;
    let submissionId = 0;
    let approvedSerial = 0;

    test.beforeAll(({ browserName }) => {
      s = subjects(browserName)[index];
    });

    test(`${index === 0 ? 'a language pack' : 'an app with an interface'}: submitted with the form`, async ({ browserName }) => {
      const id = identity(browserName);
      await pub.goto('/submit');
      await expect(pub.getByTestId('submit-form'), 'the submit form (an active publisher)').toBeVisible();
      await pub.getByTestId('sb-publisher').click();
      await pub.getByTestId(`sb-publisher-option-${id.publisherId}`).click();
      await expect(pub.getByTestId('sb-publisher')).toHaveAttribute('data-value', id.publisherId);
      await pub.getByTestId('sb-repo').fill(s.repo);
      await pub.getByTestId('sb-ref').fill(s.tag);
      await pub.getByTestId('sb-send').click();
      await pub.waitForURL(/\/submissions\/\d+$/);
      submissionId = Number(new URL(pub.url()).pathname.split('/').pop());
      expect(submissionId).toBeGreaterThan(0);
    });

    test('the pipeline passes on both validators', async () => {
      test.setTimeout(10 * 60_000);
      let last: any = null;
      const deadline = Date.now() + 9 * 60_000;
      while (Date.now() < deadline) {
        const r = await pub.request.get(`/api/v1/submissions/${submissionId}`, { failOnStatusCode: false });
        if (r.status() === 200) {
          last = await r.json();
          if (['needs_review', 'failed', 'rejected', 'withdrawn'].includes(last.status)) break;
        }
        await pub.waitForTimeout(3000);
      }
      const failed = (last?.checks ?? []).filter((c: any) => c.status === 'failed');
      expect(last?.status, JSON.stringify(failed.length ? failed : last, null, 2)).toBe('needs_review');
      // SubmissionDetail.checks (api/openapi.yaml Check): kind, validator, status.
      const checks: any[] = last.checks;
      expect(Array.isArray(checks) && checks.length > 0, 'the submission lists its checks').toBe(true);
      expect(failed, 'no failed check').toEqual([]);
      // The fixtures' range (>=0.49.0) admits both validators: one passed dry run each.
      const dry = checks.filter((c) => c.kind === 'dry_run');
      expect(dry.map((c) => `${c.validator}:${c.status}`).sort()).toEqual(['floor:passed', 'latest:passed']);
      await expect(pub.getByTestId('submission-status')).toContainText('Needs review');
    });

    test('the administrator approves it on the submission page', async () => {
      await admin.goto(`/submissions/${submissionId}`);
      await expect(admin.getByTestId('submission-status')).toContainText('Needs review');
      if (index === 1) {
        // The reviewer sees what the listing says the app converts, checked
        // against the manifest's engines, before deciding.
        await expect(admin.getByTestId('listing-conversions')).toContainText('2 entries, 5 from-to pairs.');
        await admin.getByTestId('listing-conversions').getByText('Every entry as declared').click();
        await expect(admin.getByTestId('listing-conversions').getByRole('listitem')).toHaveText(['.e2etxt, .docx → .pdf, .txt', '.e2edoc → .e2etxt']);
      }
      await admin.getByTestId('approve').click();
      await admin.getByTestId('dialog-confirm').filter({ visible: true }).click();
      const done = admin.getByTestId('action-done');
      await expect(done).toBeVisible();
      approvedSerial = Number((await done.innerText()).match(/serial (\d+)/)?.[1] ?? 0);
      expect(approvedSerial, 'the page names the new serial').toBeGreaterThanOrEqual(1);
    });

    test('the catalog lists it for an anonymous visitor', async ({ browser }) => {
      const ctx = await browser.newContext({ baseURL: BASE, ignoreHTTPSErrors: true });
      const page = await ctx.newPage();
      await page.goto('/');
      const link = page.getByTestId(`app-link-${s.name}`);
      await expect(link).toBeVisible();
      await link.click();
      await page.waitForURL(new RegExp(`/apps/${s.name}`));
      await expect(page.getByTestId('app-title')).toContainText(s.label);
      await expect(page.getByTestId('install')).toBeVisible();
      await ctx.close();
    });

    test('the signed index verifies and pins the tag commit', async ({ request }) => {
      const { doc } = await fetchVerifiedIndex(request);
      expect(doc.serial).toBeGreaterThanOrEqual(approvedSerial);
      const app = doc.apps.find((a: any) => a.name === s.name);
      expect(app, 'listed in the index').toBeTruthy();
      expect(app.repo).toBe(s.repo);
      const v = app.versions.find((x: any) => x.version === s.version);
      expect(v.commit).toBe(fixtureCommit(s.repo, s.tag));
      expect(v.yanked).toBeNull();
      // What a listing says the app converts is the store's, not filex's: the index does not carry it.
      expect(JSON.stringify(app)).not.toContain('conversions');
    });

    test('fapps verify-index accepts the published index', async ({ request }) => {
      test.skip(!existsSync('/opt/fapps/fapps'), 'backend/cmd/fapps is not on main yet');
      // A filex trusts the key it was configured with, never the index's own
      // list; here the demo's throw-away key from /v1/keys.json stands in for it.
      const keys = (await (await request.get('/v1/keys.json')).json()).keys as any[];
      const key = keys.find((k) => k.use === 'index' && k.status === 'active').ed25519;
      const out = execFileSync(
        'fapps',
        ['verify-index', '-key', key, '-min-serial', String(approvedSerial), `${BASE}/v1/index.json`],
        { encoding: 'utf8' },
      );
      expect(out).toMatch(/^ok: serial \d+/);
      test.info().annotations.push({ type: 'fapps verify-index', description: out.trim() });
    });

    if (index === 1) {
      test('a file-type search finds it by the kinds its manifest opens, and says what filex does by itself', async ({ browser }) => {
        // The approval recorded what the app works with (its viewer opens
        // .e2etxt, .drawio and .docx); filex 0.51 opens .docx itself only
        // with ONLYOFFICE, and nobody opens .xyz.
        test.setTimeout(4 * 60_000);
        const patient = { timeout: 90_000 };
        const ctx = await browser.newContext({ baseURL: BASE, ignoreHTTPSErrors: true });
        await patientApi(ctx);
        const page = await ctx.newPage();

        await page.goto('/?q=.docx');
        await expect(page.getByTestId('ft-builtin-docx')).toContainText('.docx: filex opens it with ONLYOFFICE', patient);
        await expect(page.getByTestId('ft-builtin-docx')).not.toContainText('out of the box');
        const apps = page.getByTestId('ft-apps');
        await expect(apps.getByRole('heading', { name: 'Apps that work with .docx' })).toBeVisible();
        await expect(apps.getByTestId(`app-use-${s.name}-docx`)).toContainText('Opens');
        // its listing also says it converts .docx: the card line says into what
        await expect(apps.getByTestId(`app-use-${s.name}-docx`)).toContainText('Converts .docx → .pdf, .txt');

        // a kind only its listing's conversions name: the app is named for
        // it (among the apps for it, not apart), and the count agrees
        await page.goto('/?q=.e2edoc');
        await expect(page.getByTestId('ft-apps').getByTestId(`app-use-${s.name}-e2edoc`)).toHaveText('.e2edocConverts .e2edoc → .e2etxt', patient);
        await expect(page.getByTestId('ft-any').getByTestId(`app-card-${s.name}`)).toHaveCount(0);
        await expect(page.getByTestId('catalog-count')).toContainText(/\d+ apps? works? with \.e2edoc/);
        await expect(page.getByTestId('catalog-count')).not.toContainText('No app works');

        await page.goto('/?q=drawio');
        await expect(page.getByTestId('ft-apps').getByTestId(`app-use-${s.name}-drawio`)).toContainText('Opens', patient);
        await expect(page.getByTestId('ft-builtin-drawio')).toContainText('.drawio: filex opens it with draw.io');

        // the same search in the list view: filex's table, with what each app does
        await page.getByTestId('catalog-view-list').click();
        await expect(page).toHaveURL(/view=list/);
        await expect(page.getByTestId('catalog-table')).toContainText('With .drawio', patient);
        await expect(page.getByTestId('catalog-table').getByRole('row').filter({ hasText: s.label })).toContainText('.drawio: Opens');

        await page.goto('/?q=.xyz');
        await expect(page.getByTestId('ft-empty')).toContainText('filex does not open .xyz by itself and no app in the store handles it yet', patient);
        await expect(page.getByTestId('ft-empty').getByRole('link', { name: 'Publish an app' })).toHaveAttribute('href', '/publish');
        await expect(page.getByTestId('ft-panel')).toHaveCount(0);

        // the app's page says what it works with, each kind a link to the search
        await page.goto(`/apps/${s.name}`);
        const works = page.getByTestId('works-with');
        await expect(works.getByTestId('works-with-e2etxt')).toContainText('Opens', patient);
        // and, apart, the table of what its listing says it converts
        await works.getByTestId('conversions').locator('summary').click();
        const conv = works.locator('[data-table-id="store.app.conversions"]');
        await expect(conv.getByRole('row').filter({ has: page.getByRole('link', { name: 'Find every app that works with .e2edoc', exact: true }) })).toContainText('.e2etxt');
        await expect(conv.getByRole('row').filter({ has: page.getByRole('link', { name: 'Find every app that works with .docx', exact: true }) })).toContainText('.pdf, .txt');
        await works.getByTestId('conversions').locator('summary').click();
        await works.getByTestId('works-with-docx').getByRole('link', { name: 'Find every app that works with .docx' }).click();
        await expect(page).toHaveURL(/\?q=\.docx$/);
        await expect(page.getByTestId('ft-builtin-docx')).toBeVisible(patient);

        // the API says the same, and an ordinary word stays a word
        const api = await (await patientGet(ctx.request, '/api/v1/apps?q=*.DOCX')).json();
        expect(api.file_types.extensions).toEqual(['docx']);
        const mine = api.items.find((a: any) => a.name === s.name);
        expect(mine.matched_extensions).toEqual([
          { ext: 'docx', roles: [{ role: 'open', id: 'viewer', label: { en: 'E2E Hello' } }, { role: 'convert', to: ['pdf', 'txt'] }] },
        ]);
        const detail = await (await patientGet(ctx.request, `/api/v1/apps/${s.name}/versions/${s.version}`)).json();
        expect(detail.conversions).toEqual([
          { from: ['e2etxt', 'docx'], roles: [{ role: 'convert', to: ['pdf', 'txt'] }] },
          { from: ['e2edoc'], roles: [{ role: 'convert', to: ['e2etxt'] }] },
        ]);
        const words = await (await patientGet(ctx.request, `/api/v1/apps?q=${encodeURIComponent(s.label)}`)).json();
        expect(words.file_types).toBeUndefined();
        await ctx.close();
      });

      test('a paid app: price, a license key shown once, the signed check, an install link', async ({ browserName, request }) => {
        test.setTimeout(8 * 60_000);
        const id = identity(browserName);
        const keys = (await (await patientGet(request, '/v1/keys.json')).json()).keys as any[];
        const licenseKey = keys.find((k) => k.use === 'license' && k.status === 'active');
        const indexKey = keys.find((k) => k.use === 'index' && k.status === 'active');
        expect(licenseKey, 'a license key in /v1/keys.json').toBeTruthy();

        // the publisher sets a price and makes a key on the app's license page
        await pub.goto(`/publish/apps/${s.name}`);
        await expect(pub.getByTestId('pricing-form')).toBeVisible({ timeout: 90_000 });
        await pub.getByTestId('pricing-paid').check();
        await pub.getByTestId('pricing-text').fill('$29 per instance');
        await pub.getByTestId('pricing-save').click();
        await expect(pub.getByTestId('pricing-saved')).toBeVisible();
        await pub.getByTestId('licence-new').click();
        await pub.getByTestId('licence-licensee').fill(`E2E buyer ${browserName}`);
        await pub.getByTestId('licence-seats').fill('1');
        await pub.getByTestId('dialog-confirm').filter({ visible: true }).click();
        const keyCell = pub.getByTestId('licence-key-value');
        await expect(keyCell).toBeVisible();
        const key = (await keyCell.innerText()).trim();
        expect(key).toMatch(/^FXL-[0-9A-HJKMNP-TV-Z]{4}(-[0-9A-HJKMNP-TV-Z]{4}){5}-[0-9A-HJKMNP-TV-Z]{2}$/);
        await pub.getByTestId('dialog-confirm').filter({ visible: true }).click();
        await expect(pub.getByTestId('licence-key-value')).toHaveCount(0);
        expect(await pub.content(), 'the key is gone from the page').not.toContain(key);
        await expect(pub.getByTestId('licences')).toContainText(key.slice(0, 8));

        // a filex server checks it: the verdict is signed with the license key
        const check = async (k: string, app: string, instance: string) => {
          const r = await request.post('/v1/licenses/verify', { data: { key: k, app, instance_id: instance }, failOnStatusCode: false });
          expect(r.status()).toBe(200);
          const env = await r.json();
          expect(env.key_id).toBe(licenseKey.id);
          expect(verifyEnvelope(env, licenseKey.ed25519), 'license verdict signature (Node, independent)').toBe(true);
          return env.payload;
        };
        const ok = await check(key, s.name, `e2e-${browserName}-1`);
        expect(ok).toMatchObject({ result: 'valid', app: s.name, seats: 1, seats_used: 1, licensee: `E2E buyer ${browserName}` });
        expect((await check(key, s.name, `e2e-${browserName}-2`)).result).toBe('seats_exhausted');
        const wrong = await check(key, subjects(browserName)[0].name, `e2e-${browserName}-1`);
        expect(wrong.result).toBe('wrong_app');
        expect(wrong.licensee).toBeUndefined();
        expect((await check('FXL-0000-0000-0000-0000-0000-0000-00', s.name, 'x')).result).toBe('invalid');

        // Install: a magic link to the person's own filex, with the key
        await pub.goto(`/apps/${s.name}`);
        await expect(pub.getByTestId('app-paid')).toContainText('$29 per instance', { timeout: 90_000 });
        await pub.getByTestId('install-open').click();
        const dialog = pub.locator('dialog[open]');
        await expect(dialog).toBeVisible();
        await dialog.getByTestId('install-url').fill('https://filex.e2e.test');
        await dialog.getByTestId('install-label').fill(`E2E filex ${browserName}`);
        await dialog.getByTestId('install-license').fill(key.toLowerCase());
        await dialog.getByTestId('dialog-confirm').click();
        const linkText = (await dialog.getByTestId('magic-link').innerText()).trim();
        const prefix = `https://filex.e2e.test/admin/store-install#store=${encodeURIComponent(new URL(BASE).origin)}&intent=`;
        expect(linkText.startsWith(prefix), linkText).toBe(true);
        const token = linkText.slice(prefix.length);
        expect(token).toMatch(/^[A-Za-z0-9_-]{43}$/);
        await expect(dialog.getByTestId('install-link-open')).toHaveAttribute('href', linkText);
        await dialog.getByTestId('dialog-confirm').click();

        // what that filex reads: signed with the index key, the approved version's pins
        const read = await request.get(`/v1/install/${token}`, { failOnStatusCode: false });
        expect(read.status()).toBe(200);
        const env = await read.json();
        expect(env.key_id).toBe(indexKey.id);
        expect(verifyEnvelope(env, indexKey.ed25519), 'install intent signature (Node, independent)').toBe(true);
        expect(env.payload).toMatchObject({ store: new URL(BASE).origin, filex_origin: 'https://filex.e2e.test', app: s.name, version: s.version, repo: s.repo, ref: s.tag,
          commit: fixtureCommit(s.repo, s.tag), paid: true, license_key: key, kind: 'app' });
        expect(env.payload.ui_sha256).toMatch(/^[0-9a-f]{64}$/);
        expect(env.payload.manifest_sha256).toMatch(/^[0-9a-f]{64}$/);
        const done = await request.post(`/v1/install/${token}/complete`, { data: { instance_id: `e2e-${browserName}-1`, result: 'installed' }, failOnStatusCode: false });
        expect(done.status()).toBe(200);
        expect((await request.post(`/v1/install/${token}/complete`, { data: { instance_id: 'x', result: 'installed' }, failOnStatusCode: false })).status()).toBe(410);
        expect((await request.get(`/v1/install/${token}`, { failOnStatusCode: false })).status()).toBe(410);

        // the saved instance is the publisher's own
        await pub.goto('/me/instances');
        await expect(pub.getByTestId('instances')).toContainText('https://filex.e2e.test', { timeout: 90_000 });
        const mine = await (await patientGet(admin.request, '/api/v1/me/instances')).json();
        expect(JSON.stringify(mine)).not.toContain('filex.e2e.test');

        // revoke on the license page: the next check says so
        await pub.goto(`/publish/apps/${s.name}`);
        await pub.getByTestId(`licence-actions-${key.slice(0, 8)}`).click();
        await pub.getByRole('menuitem', { name: 'Revoke…', exact: true }).click();
        await pub.getByTestId('dialog-confirm').filter({ visible: true }).click();
        await expect(pub.getByTestId('licences')).toContainText('Revoked', { timeout: 90_000 });
        expect((await check(key, s.name, `e2e-${browserName}-1`)).result).toBe('revoked');
      });
    }

    test('a yank on the app page publishes a new index that marks the version', async ({ request }) => {
      // the page's reads may wait out the run's shared API limit (Retry-After up to a minute)
      test.setTimeout(4 * 60_000);
      await admin.goto(`/apps/${s.name}`);
      await expect(admin.getByTestId('app-admin')).toBeVisible({ timeout: 90_000 });
      await admin.getByTestId('versions-table').locator('button[aria-haspopup="menu"]').first().click();
      await admin.getByRole('menuitem', { name: 'Yank…' }).click();
      await admin.getByTestId('dialog-reason').filter({ visible: true }).fill('e2e: yank check');
      await admin.getByTestId('dialog-confirm').filter({ visible: true }).click();
      await expect(admin.getByTestId('version-yanked')).toBeVisible({ timeout: 90_000 });
      const { doc } = await fetchVerifiedIndex(request);
      expect(doc.serial).toBeGreaterThan(approvedSerial);
      const v = doc.apps.find((a: any) => a.name === s.name).versions.find((x: any) => x.version === s.version);
      expect(v.yanked?.reason).toBe('e2e: yank check');
    });
  });
}

test('Administration > Apps lists unlisted and revoked apps and filters by status', async ({ browserName }) => {
  // Each action reloads the list, which may wait out the run's shared API
  // limit (120 a minute per address; Retry-After up to a minute).
  test.setTimeout(6 * 60_000);
  const patient = { timeout: 90_000 };
  const [pack, ui] = subjects(browserName);
  const labelOf = (name: string) => (name === pack.name ? pack.label : ui.label);
  const rowOf = (name: string) => admin.getByTestId('admin-apps').getByRole('row').filter({ hasText: labelOf(name) });
  const search = async (name: string) => {
    await admin.getByLabel('Search').fill(name);
    await expect(rowOf(name).first()).toBeVisible(patient);
  };

  await admin.goto('/admin/apps');
  await expect(admin.getByTestId('admin-apps')).toBeVisible(patient);
  await search(pack.name);
  await expect(rowOf(pack.name)).toContainText('Listed', patient);

  await admin.getByTestId(`app-actions-${pack.name}`).click();
  await admin.getByRole('menuitem', { name: 'Unlist', exact: true }).click();
  await expect(rowOf(pack.name)).toContainText('Unlisted', patient);

  await search(ui.name);
  await admin.getByTestId(`app-actions-${ui.name}`).click();
  await admin.getByRole('menuitem', { name: 'Revoke…', exact: true }).click();
  await admin.getByTestId('dialog-reason').filter({ visible: true }).fill('e2e: revoke check');
  await admin.getByTestId('dialog-confirm').filter({ visible: true }).click();
  await expect(rowOf(ui.name)).toContainText('Revoked', patient);

  await admin.getByLabel('Search').fill('');
  await admin.getByTestId('aa-status-unlisted').click();
  await expect(rowOf(pack.name).first()).toBeVisible(patient);
  await expect(rowOf(ui.name)).toHaveCount(0, patient);
  await admin.getByTestId('aa-status-revoked').click();
  await expect(rowOf(ui.name).first()).toBeVisible(patient);
  await expect(rowOf(pack.name)).toHaveCount(0, patient);
  await admin.getByTestId('aa-status-listed').click();
  await expect(rowOf(pack.name)).toHaveCount(0, patient);
  await expect(rowOf(ui.name)).toHaveCount(0, patient);

  const anon = await patientGet(admin.request, '/api/v1/apps');
  const names = ((await anon.json()).items as any[]).map((a) => a.name);
  expect(names, 'unlisted and revoked apps are not in the public catalog').not.toContain(pack.name);
  expect(names).not.toContain(ui.name);
});

// ---------------------------------------------------------------- helpers

async function publisherStatus(request: APIRequestContext, publisherId: string) {
  const r = await patientGet(request, '/api/v1/admin/publishers?per_page=100');
  if (r.status() !== 200) return `HTTP ${r.status()}`;
  return ((await r.json()).items as any[]).find((p) => p.id === publisherId)?.status ?? 'absent';
}

/** The commit the mock gives a tag (e2e/mock-idp fakeSHA): what the store must record. */
function fixtureCommit(repo: string, tag: string) {
  return createHash('sha1').update(`commit\0${repo.toLowerCase()}\0${tag}`).digest('hex');
}

/** Fetches the index and its signature, checks it with the active index key. */
async function fetchVerifiedIndex(request: APIRequestContext) {
  const body = await (await request.get('/v1/index.json')).body();
  const sig = await (await request.get('/v1/index.json.sig')).text();
  const keys = (await (await request.get('/v1/keys.json')).json()).keys as any[];
  const doc = JSON.parse(body.toString('utf8'));
  const active = keys.filter((k) => k.use === 'index' && k.status === 'active');
  expect(active.length, 'one active index key').toBe(1);
  expect(verifyDetached(body, sig, active[0].ed25519), 'index signature').toBe(true);
  expect(Date.parse(doc.expires_at)).toBeGreaterThan(Date.now());
  return { doc, body };
}
